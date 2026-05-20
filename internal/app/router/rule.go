package router

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/lonegunmanb/hclfuncs"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"

	"github.com/lonegunmanb/jjc/internal/app/kanban"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

// RuleConfig is the decoded `rule {}` side of router.hcl.
type RuleConfig struct {
	Rules []RuleBlock `hcl:"rule,block"`
}

// RuleBlock is one declarative `rule "<name>" {}` block.
type RuleBlock struct {
	Name    string         `hcl:"name,label"`
	When    hcl.Expression `hcl:"when"`
	Prompts []string       `hcl:"prompts"`

	DeclRange hcl.Range
}

// CardSignals carries the per-card variables visible to a rule's `when`.
type CardSignals struct {
	ID        string
	Name      string
	ListName  string
	FirstLine string
	Labels    []string
}

// RuleMatch is the structured result of evaluating rule blocks.
type RuleMatch struct {
	RuleName    string
	PromptNames []string
}

// RuleEngine evaluates configured `rule {}` blocks top-down.
type RuleEngine struct {
	rules  []RuleBlock
	logger sysevent.Sink
	funcs  map[string]function.Function
	kanban cty.Value
}

// LoadRuleConfig reads router.hcl at path and decodes its `rule {}` blocks.
func LoadRuleConfig(path, playbooksDir string) (RuleConfig, error) {
	if path == "" {
		return RuleConfig{}, errors.New("router: hcl path is empty")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return RuleConfig{}, fmt.Errorf("router: read %s: %w", path, err)
	}
	return DecodeRuleConfig(src, filepath.Base(path), playbooksDir)
}

// DecodeRuleConfig decodes only top-level `rule {}` blocks, ignoring sibling
// `kanban {}` and `route {}` blocks so one router.hcl can carry all layers.
func DecodeRuleConfig(src []byte, filename, playbooksDir string) (RuleConfig, error) {
	parser := hclparse.NewParser()
	f, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return RuleConfig{}, fmt.Errorf("router: parse %s: %s", filename, diags.Error())
	}
	schema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "rule", LabelNames: []string{"name"}},
		},
	}
	content, _, diags := f.Body.PartialContent(schema)
	if diags.HasErrors() {
		return RuleConfig{}, fmt.Errorf("router: scan %s: %s", filename, diags.Error())
	}

	cfg := RuleConfig{}
	seen := map[string]hcl.Range{}
	for _, b := range content.Blocks {
		if b.Type != "rule" {
			continue
		}
		var r RuleBlock
		if d := gohcl.DecodeBody(b.Body, nil, &r); d.HasErrors() {
			return RuleConfig{}, fmt.Errorf("router: decode rule %q in %s: %s",
				b.Labels[0], filename, d.Error())
		}
		r.Name = b.Labels[0]
		r.DeclRange = b.DefRange
		if r.Name == "" {
			return RuleConfig{}, fmt.Errorf("router: rule at %s has empty label", b.DefRange)
		}
		if prev, dup := seen[r.Name]; dup {
			return RuleConfig{}, fmt.Errorf("router: duplicate rule %q at %s (first declared at %s)",
				r.Name, b.DefRange, prev)
		}
		seen[r.Name] = b.DefRange
		if len(r.Prompts) > 0 && playbooksDir == "" {
			return RuleConfig{}, fmt.Errorf("router: rule %q prompts cannot be validated: playbooks dir is empty",
				r.Name)
		}
		for _, prompt := range r.Prompts {
			if err := validatePromptName(prompt); err != nil {
				return RuleConfig{}, fmt.Errorf("router: rule %q at %s has invalid prompt %q: %w",
					r.Name, b.DefRange, prompt, err)
			}
			path := filepath.Join(playbooksDir, prompt)
			info, err := os.Stat(path)
			if err != nil {
				return RuleConfig{}, fmt.Errorf("router: rule %q missing prompt %q under %s: %w",
					r.Name, prompt, playbooksDir, err)
			}
			if info.IsDir() {
				return RuleConfig{}, fmt.Errorf("router: rule %q prompt %q under %s is a directory",
					r.Name, prompt, playbooksDir)
			}
		}
		cfg.Rules = append(cfg.Rules, r)
	}
	if len(cfg.Rules) == 0 {
		return RuleConfig{}, fmt.Errorf("router: %s contains no `rule {}` blocks", filename)
	}
	return cfg, nil
}

// NewRuleEngine builds a rule engine. routerDir is passed to hclfuncs for
// standard function setup; github_issue is registered project-locally here.
func NewRuleEngine(cfg RuleConfig, routerDir string, view *kanban.Resolved, logger sysevent.Sink) *RuleEngine {
	if logger == nil {
		logger = sysevent.Default()
	}
	funcs := hclfuncs.Functions(routerDir)
	funcs["github_issue"] = githubIssueFunc
	return &RuleEngine{
		rules:  append([]RuleBlock(nil), cfg.Rules...),
		logger: logger,
		funcs:  funcs,
		kanban: buildKanbanValue(view),
	}
}

// Match returns the first rule whose `when` evaluates to true. Broken rules
// are skipped with structured log lines; no match is non-fatal.
func (e *RuleEngine) Match(card CardSignals) (RuleMatch, bool) {
	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"card":   buildCardValue(card),
			"kanban": e.kanban,
		},
		Functions: e.funcs,
	}

	for _, r := range e.rules {
		v, diags := r.When.Value(ctx)
		if diags.HasErrors() {
			sysevent.Emitf(e.logger, "rule_when_eval_error", "rule=%q diag=%q", r.Name, diags.Error())
			continue
		}
		if v.IsNull() || !v.Type().Equals(cty.Bool) {
			sysevent.Emitf(e.logger, "rule_when_type_error", "rule=%q diag=%q",
				r.Name, "when expression did not evaluate to bool")
			continue
		}
		if v.True() {
			return RuleMatch{
				RuleName:    r.Name,
				PromptNames: append([]string(nil), r.Prompts...),
			}, true
		}
	}

	sysevent.Emitf(e.logger, "rule_no_match", "card_id=%q", card.ID)
	return RuleMatch{}, false
}

func buildCardValue(card CardSignals) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"id":         cty.StringVal(card.ID),
		"name":       cty.StringVal(card.Name),
		"list_name":  cty.StringVal(card.ListName),
		"first_line": cty.StringVal(card.FirstLine),
		"labels":     stringList(card.Labels),
	})
}

func validatePromptName(name string) error {
	if name == "" {
		return errors.New("empty name")
	}
	if strings.ContainsAny(name, `/\`) {
		return errors.New("name contains a path separator")
	}
	if strings.Contains(name, "..") {
		return errors.New("name contains \"..\"")
	}
	return nil
}
