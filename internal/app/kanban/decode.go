package kanban

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// Config is the decoded shape of the `kanban {}` block in router.hcl.
// All seven role blocks are required; HCL decode rejects a Config that
// is missing any of them with a diagnostic carrying the offending file
// and line number.
type Config struct {
	Plan                 NamedRole     `hcl:"plan,block"`
	Action               NamedRole     `hcl:"action,block"`
	Wait                 WaitConfig    `hcl:"wait,block"`
	Done                 NamedRole     `hcl:"done,block"`
	AgentCommentPrefixes []string      `hcl:"agent_comment_prefixes"`
}

// NamedRole is the HCL shape used by `plan`, `action`, `done`, and each
// `wait.*` sub-block. The single `name = "..."` attribute carries the
// human-readable Trello list name.
type NamedRole struct {
	Name string `hcl:"name"`
}

// WaitConfig is the four-sub-block container for the `wait {}` block.
// All four sub-blocks are required.
type WaitConfig struct {
	PlanReview   NamedRole `hcl:"plan_review,block"`
	ActionReview NamedRole `hcl:"action_review,block"`
	Generic      NamedRole `hcl:"generic,block"`
	Exception    NamedRole `hcl:"exception,block"`
}

// Validate enforces the cross-role uniqueness rule (`name` values must
// be unique case-insensitively after trimming) and rejects empty
// names. It is called by Resolve before any board lookup so a bad
// router.hcl fails fast.
func (c Config) Validate() error {
	type pair struct {
		key  string
		name string
	}
	all := []pair{
		{"plan", c.Plan.Name},
		{"action", c.Action.Name},
		{"wait.plan_review", c.Wait.PlanReview.Name},
		{"wait.action_review", c.Wait.ActionReview.Name},
		{"wait.generic", c.Wait.Generic.Name},
		{"wait.exception", c.Wait.Exception.Name},
		{"done", c.Done.Name},
	}
	seen := map[string]string{} // normalised -> role key that first declared it
	for _, p := range all {
		trimmed := strings.TrimSpace(p.name)
		if trimmed == "" {
			return fmt.Errorf("kanban: role %q has an empty name", p.key)
		}
		key := strings.ToLower(trimmed)
		if other, dup := seen[key]; dup {
			return fmt.Errorf("kanban: roles %q and %q both declare name %q (case-insensitive collision)",
				other, p.key, trimmed)
		}
		seen[key] = p.key
	}
	return nil
}

// LoadConfig parses the `kanban {}` block out of the HCL file at path.
// Other top-level blocks in the same file (route, rule, ...) are
// silently ignored so the same router.hcl can grow `route` / `rule`
// blocks in subsequent issues without breaking this loader.
//
// Returns a diagnostic-bearing error when the file is missing,
// unparseable, or has zero / more-than-one `kanban` block.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, errors.New("kanban: hcl path is empty")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("kanban: read %s: %w", path, err)
	}
	return DecodeConfig(src, filepath.Base(path))
}

// DecodeConfig is the bytes-in/Config-out core LoadConfig wraps. It is
// exported so tests can avoid touching the filesystem.
//
// The function uses hcl.Body.PartialContent so unknown top-level blocks
// (route, rule, ...) are tolerated; only a `kanban {}` block (exactly
// one) is required.
func DecodeConfig(src []byte, filename string) (Config, error) {
	parser := hclparse.NewParser()
	f, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return Config{}, fmt.Errorf("kanban: parse %s: %s", filename, diags.Error())
	}
	schema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "kanban"},
		},
	}
	content, _, diags := f.Body.PartialContent(schema)
	if diags.HasErrors() {
		return Config{}, fmt.Errorf("kanban: scan %s: %s", filename, diags.Error())
	}
	var blocks []*hcl.Block
	for _, b := range content.Blocks {
		if b.Type == "kanban" {
			blocks = append(blocks, b)
		}
	}
	switch len(blocks) {
	case 0:
		return Config{}, fmt.Errorf("kanban: %s contains no `kanban {}` block", filename)
	case 1:
		// happy path
	default:
		return Config{}, fmt.Errorf("kanban: %s contains %d `kanban {}` blocks; exactly one is required",
			filename, len(blocks))
	}
	var cfg Config
	if d := gohcl.DecodeBody(blocks[0].Body, nil, &cfg); d.HasErrors() {
		return Config{}, fmt.Errorf("kanban: decode kanban block in %s: %s", filename, d.Error())
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
