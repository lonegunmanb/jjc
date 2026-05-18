package app

import (
	"regexp"
	"strings"
)

// WorkType identifies which family of repository / workflow a Trello card
// targets. It selects the additional-instructions file that gets appended to
// the worker session's system prompt.
type WorkType string

const (
	WorkTypeAVMModule       WorkType = "terraform-avm-module"
	WorkTypeProviderAzureRM WorkType = "terraform-provider-azurerm"
	WorkTypeAzureProvider   WorkType = "Azure/terraform-provider"
	WorkTypeTerraformLegacy WorkType = "terraform-legacy-module"
	WorkTypeGeneric         WorkType = "generic"
)

// GitHubItemKind distinguishes the two GitHub URL flavours a card may
// point at. It is used together with WorkType to select the right
// additional instructions file (e.g. avm_issue.md vs avm_pr.md).
type GitHubItemKind string

const (
	GitHubItemKindIssue   GitHubItemKind = "issue"
	GitHubItemKindPR      GitHubItemKind = "pr"
	GitHubItemKindUnknown GitHubItemKind = ""
)

// GitHubRef carries the GitHub-specific reference parsed out of a Trello
// card's first description line. Its zero value means "this card has no
// GitHub reference"; callers should branch on Present() rather than
// inspecting individual fields.
type GitHubRef struct {
	Owner    string         // empty when no GitHub URL was matched
	Repo     string         // empty when no GitHub URL was matched
	ItemKind GitHubItemKind // "issue", "pr", or "" when no GitHub URL was matched
	Number   string         // issue / PR number, empty when no GitHub URL was matched
	URL      string         // the matched URL as seen in firstLine, empty if none
}

// Present reports whether the GitHubRef carries a usable owner/repo pair
// (i.e. a GitHub URL was matched on the card's first description line).
// Every call site that previously did
// `if c.Owner != "" && c.Repo != ""` should branch on this helper.
func (g GitHubRef) Present() bool {
	return g.Owner != "" && g.Repo != ""
}

// CardClassification is the result of inspecting a Trello card's first
// non-empty description line for a GitHub URL.
type CardClassification struct {
	WorkType WorkType
	// GitHub is the optional GitHub reference parsed from the card's
	// first description line. The zero value means "this card has no
	// GitHub reference"; use GitHub.Present() to gate consumers.
	GitHub GitHubRef
}

// githubURLPattern matches https?://github.com/{owner}/{repo}/(issues|pull)/{number}
// with the owner/repo segments restricted to GitHub's character set. The
// trailing number is captured greedy-digit so query strings or anchors don't
// pollute it.
var githubURLPattern = regexp.MustCompile(`https?://github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/(issues|pull)/(\d+)`)

// ClassifyCard derives the WorkType (and surrounding metadata) from the
// first line of a Trello card's description. The matching rules mirror the
// table in MANAGER.md §4 and are evaluated top-down; the first matching
// rule wins. firstLine is matched as-is — callers are expected to pass the
// raw firstLine returned by trello-get-card-info.ps1.
func ClassifyCard(firstLine string) CardClassification {
	m := githubURLPattern.FindStringSubmatch(firstLine)
	if m == nil {
		return CardClassification{WorkType: WorkTypeGeneric}
	}

	owner, repo, kindToken, number := m[1], m[2], m[3], m[4]
	kind := GitHubItemKindIssue
	if kindToken == "pull" {
		kind = GitHubItemKindPR
	}

	c := CardClassification{
		GitHub: GitHubRef{
			Owner:    owner,
			Repo:     repo,
			Number:   number,
			URL:      m[0],
			ItemKind: kind,
		},
	}

	repoLower := strings.ToLower(repo)
	ownerLower := strings.ToLower(owner)

	switch {
	// Rule 1: Azure/<...terraform...avm...>
	case ownerLower == "azure" &&
		strings.Contains(repoLower, "terraform") &&
		strings.Contains(repoLower, "avm"):
		c.WorkType = WorkTypeAVMModule

	// Rule 2: terraform-provider-azurerm (any owner — the canonical one is
	// hashicorp/terraform-provider-azurerm but the rule key is repo name).
	case repoLower == "terraform-provider-azurerm":
		c.WorkType = WorkTypeProviderAzureRM

	// Rule 3: Azure/terraform-provider-* (any other Azure-owned provider).
	case ownerLower == "azure" && strings.HasPrefix(repoLower, "terraform-provider-"):
		c.WorkType = WorkTypeAzureProvider

	// Rule 4: Azure/<...terraform...> that is neither AVM nor a provider.
	case ownerLower == "azure" && strings.Contains(repoLower, "terraform"):
		c.WorkType = WorkTypeTerraformLegacy

	default:
		c.WorkType = WorkTypeGeneric
	}

	return c
}

// EntryPlaybookFilename returns the filename of the additional-instructions
// "entry playbook" that the worker session should load up-front for the
// given classification, or "" when no playbook applies (e.g. generic cards
// or work types without a per-kind entry file). The returned name is just
// the basename — callers join it with the configured router directory.
//
// The mapping mirrors the table in WORKER.md §0 "首轮自举".
func EntryPlaybookFilename(c CardClassification) string {
	switch c.WorkType {
	case WorkTypeAVMModule:
		switch c.GitHub.ItemKind {
		case GitHubItemKindIssue:
			return "avm_issue.md"
		case GitHubItemKindPR:
			return "avm_pr.md"
		}
	case WorkTypeProviderAzureRM:
		switch c.GitHub.ItemKind {
		case GitHubItemKindIssue:
			return "azurerm_provider_issue.md"
		case GitHubItemKindPR:
			return "azurerm_provider_pr.md"
		}
	case WorkTypeTerraformLegacy:
		switch c.GitHub.ItemKind {
		case GitHubItemKindIssue:
			return "tfvm_issue.md"
		case GitHubItemKindPR:
			return "tfvm_pr.md"
		}
	}
	return ""
}
