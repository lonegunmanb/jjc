package router

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

const sampleRulesHCL = `
rule "azurerm_provider_issue" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).repo) == "terraform-provider-azurerm"
      && github_issue(card.first_line).kind == "issue")
  prompts = ["azurerm_provider_issue.md"]
}

rule "azurerm_provider_pr" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).repo) == "terraform-provider-azurerm"
      && github_issue(card.first_line).kind == "pr")
  prompts = ["azurerm_provider_pr.md"]
}

rule "avm_issue" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && strcontains(lower(github_issue(card.first_line).repo), "terraform")
      && strcontains(lower(github_issue(card.first_line).repo), "avm")
      && github_issue(card.first_line).kind == "issue")
  prompts = ["avm_issue.md"]
}

rule "avm_pr" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && strcontains(lower(github_issue(card.first_line).repo), "terraform")
      && strcontains(lower(github_issue(card.first_line).repo), "avm")
      && github_issue(card.first_line).kind == "pr")
  prompts = ["avm_pr.md"]
}

rule "azure_other_provider" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && startswith(lower(github_issue(card.first_line).repo), "terraform-provider-"))
  prompts = []
}

rule "tfvm_issue" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && strcontains(lower(github_issue(card.first_line).repo), "terraform")
      && github_issue(card.first_line).kind == "issue")
  prompts = ["tfvm_issue.md"]
}

rule "tfvm_pr" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && strcontains(lower(github_issue(card.first_line).repo), "terraform")
      && github_issue(card.first_line).kind == "pr")
  prompts = ["tfvm_pr.md"]
}

rule "fallback" {
  when    = true
  prompts = []
}
`

var canonicalPromptNames = []string{
	"azurerm_provider_issue.md",
	"azurerm_provider_pr.md",
	"avm_issue.md",
	"avm_pr.md",
	"tfvm_issue.md",
	"tfvm_pr.md",
}

func tempPlaybooksDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range canonicalPromptNames {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newSampleRuleEngine(t *testing.T) (*RuleEngine, *bytes.Buffer) {
	t.Helper()
	cfg, err := DecodeRuleConfig([]byte(sampleRulesHCL), "rules.hcl", tempPlaybooksDir(t))
	if err != nil {
		t.Fatalf("DecodeRuleConfig: %v", err)
	}
	var buf bytes.Buffer
	return NewRuleEngine(cfg, "", sampleView(), log.New(&buf, "", 0)), &buf
}

func TestGitHubIssueFunc(t *testing.T) {
	cases := []struct {
		name string
		in   cty.Value
		null bool
		want GitHubIssue
	}{
		{
			name: "issue URL",
			in:   cty.StringVal("https://github.com/Azure/terraform-azurerm-vnet/issues/3"),
			want: GitHubIssue{Owner: "Azure", Repo: "terraform-azurerm-vnet", Number: "3", Kind: "issue", URL: "https://github.com/Azure/terraform-azurerm-vnet/issues/3"},
		},
		{
			name: "PR URL",
			in:   cty.StringVal("https://github.com/hashicorp/terraform-provider-azurerm/pull/99"),
			want: GitHubIssue{Owner: "hashicorp", Repo: "terraform-provider-azurerm", Number: "99", Kind: "pr", URL: "https://github.com/hashicorp/terraform-provider-azurerm/pull/99"},
		},
		{name: "plain text", in: cty.StringVal("nothing here"), null: true},
		{name: "null input", in: cty.NullVal(cty.String), null: true},
		{
			name: "embedded URL",
			in:   cty.StringVal("see https://github.com/Azure/terraform-avm-res-foo/issues/42, please"),
			want: GitHubIssue{Owner: "Azure", Repo: "terraform-avm-res-foo", Number: "42", Kind: "issue", URL: "https://github.com/Azure/terraform-avm-res-foo/issues/42"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := githubIssueFunc.Call([]cty.Value{tc.in})
			if err != nil {
				t.Fatalf("github_issue: %v", err)
			}
			if tc.null {
				if !got.IsNull() {
					t.Fatalf("github_issue = %#v, want null", got)
				}
				return
			}
			if got.IsNull() {
				t.Fatal("github_issue returned null")
			}
			if got.GetAttr("owner").AsString() != tc.want.Owner ||
				got.GetAttr("repo").AsString() != tc.want.Repo ||
				got.GetAttr("number").AsString() != tc.want.Number ||
				got.GetAttr("kind").AsString() != tc.want.Kind ||
				got.GetAttr("url").AsString() != tc.want.URL {
				t.Fatalf("github_issue = %#v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRuleEngineMatchesCanonicalWorktypeCases(t *testing.T) {
	engine, _ := newSampleRuleEngine(t)
	cases := []struct {
		name        string
		firstLine   string
		wantRule    string
		wantPrompts []string
	}{
		{"AVM module issue", "https://github.com/Azure/terraform-azurerm-avm-res-compute-diskencryptionset/issues/45", "avm_issue", []string{"avm_issue.md"}},
		{"AVM module pull request", "see PR https://github.com/Azure/terraform-azurerm-avm-res-network-virtualnetwork/pull/127 for context", "avm_pr", []string{"avm_pr.md"}},
		{"terraform-provider-azurerm trumps Azure rule", "https://github.com/hashicorp/terraform-provider-azurerm/issues/12345", "azurerm_provider_issue", []string{"azurerm_provider_issue.md"}},
		{"Azure-owned other provider", "https://github.com/Azure/terraform-provider-azapi/pull/9", "azure_other_provider", nil},
		{"Azure terraform legacy module (not AVM, not provider)", "https://github.com/Azure/terraform-azurerm-vnet/issues/3", "tfvm_issue", []string{"tfvm_issue.md"}},
		{"non-Azure non-provider repo falls through to generic", "https://github.com/lonegunmanb/some-other-tool/issues/1", "fallback", nil},
		{"no GitHub URL at all", "Investigate the flaky build on Friday", "fallback", nil},
		{"empty firstLine", "", "fallback", nil},
		{"URL is case-sensitive on owner but classification is case-insensitive", "https://github.com/azure/Terraform-AzureRM-AVM-Res-Group/pull/1", "avm_pr", []string{"avm_pr.md"}},
		{"URL embedded with trailing punctuation does not break number capture", "ref: https://github.com/Azure/terraform-azurerm-avm-res-foo/issues/42.", "avm_issue", []string{"avm_issue.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := engine.Match(CardSignals{ID: "card-1", FirstLine: tc.firstLine})
			if !ok {
				t.Fatal("expected a rule match")
			}
			if got.RuleName != tc.wantRule {
				t.Fatalf("RuleName = %q, want %q", got.RuleName, tc.wantRule)
			}
			if strings.Join(got.PromptNames, ",") != strings.Join(tc.wantPrompts, ",") {
				t.Fatalf("PromptNames = %#v, want %#v", got.PromptNames, tc.wantPrompts)
			}
		})
	}
}

func TestRuleEngineSkipsTypeAndEvalErrors(t *testing.T) {
	src := `
rule "type_error" {
  when    = "yes"
  prompts = []
}
rule "eval_error" {
  when    = card.missing == "x"
  prompts = []
}
rule "good" {
  when    = true
  prompts = []
}
`
	cfg, err := DecodeRuleConfig([]byte(src), "bad-when.hcl", tempPlaybooksDir(t))
	if err != nil {
		t.Fatalf("DecodeRuleConfig: %v", err)
	}
	var buf bytes.Buffer
	got, ok := NewRuleEngine(cfg, "", nil, log.New(&buf, "", 0)).Match(CardSignals{ID: "c1"})
	if !ok || got.RuleName != "good" {
		t.Fatalf("expected good rule after skipped errors, got %+v ok=%v", got, ok)
	}
	logs := buf.String()
	if !strings.Contains(logs, "event=rule_when_type_error") ||
		!strings.Contains(logs, "event=rule_when_eval_error") {
		t.Fatalf("expected type/eval logs, got %q", logs)
	}
}

func TestRuleEngineNoMatch(t *testing.T) {
	src := `
rule "nope" {
  when    = false
  prompts = []
}
`
	cfg, err := DecodeRuleConfig([]byte(src), "nomatch.hcl", tempPlaybooksDir(t))
	if err != nil {
		t.Fatalf("DecodeRuleConfig: %v", err)
	}
	var buf bytes.Buffer
	_, ok := NewRuleEngine(cfg, "", nil, log.New(&buf, "", 0)).Match(CardSignals{ID: "c42"})
	if ok {
		t.Fatal("expected no match")
	}
	if !strings.Contains(buf.String(), "event=rule_no_match") || !strings.Contains(buf.String(), `card_id="c42"`) {
		t.Fatalf("expected no-match log, got %q", buf.String())
	}
}

func TestDecodeRuleConfigPromptValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt string
		want   string
	}{
		{"empty", "", "empty name"},
		{"slash", "dir/file.md", "path separator"},
		{"backslash", `dir\file.md`, "path separator"},
		{"dotdot", "bad..md", `..`},
		{"missing", "missing.md", "missing prompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := `rule "bad" {
  when = true
  prompts = ["` + promptLiteral(tc.prompt) + `"]
}`
			_, err := DecodeRuleConfig([]byte(src), "bad.hcl", tempPlaybooksDir(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("DecodeRuleConfig error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func promptLiteral(s string) string {
	return strings.ReplaceAll(s, `\`, `\\`)
}

func TestLoadRuleConfigFromExampleFile(t *testing.T) {
	path := filepath.Join("..", "..", "..", "examples", "router", "router.hcl")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("example router.hcl not found at %s: %v", path, err)
	}
	cfg, err := LoadRuleConfig(path, tempPlaybooksDir(t))
	if err != nil {
		t.Fatalf("LoadRuleConfig(%s): %v", path, err)
	}
	if len(cfg.Rules) != 8 {
		t.Fatalf("expected 8 canonical rules, got %d", len(cfg.Rules))
	}
	if cfg.Rules[len(cfg.Rules)-1].Name != "fallback" {
		t.Fatalf("expected fallback as last rule, got %q", cfg.Rules[len(cfg.Rules)-1].Name)
	}
}
