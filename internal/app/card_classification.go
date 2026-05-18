package app

import "github.com/lonegunmanb/trello-copilot/internal/app/router"

// GitHubItemKind distinguishes the two GitHub URL flavours a card may point at.
type GitHubItemKind string

const (
	GitHubItemKindIssue   GitHubItemKind = "issue"
	GitHubItemKindPR      GitHubItemKind = "pr"
	GitHubItemKindUnknown GitHubItemKind = ""
)

// GitHubRef carries the GitHub-specific reference parsed out of a Trello
// card's first description line. Its zero value means "this card has no
// GitHub reference"; callers should branch on Present().
type GitHubRef struct {
	Owner    string
	Repo     string
	ItemKind GitHubItemKind
	Number   string
	URL      string
}

func (g GitHubRef) Present() bool {
	return g.Owner != "" && g.Repo != ""
}

// CardClassification is the GitHub reference and rule metadata derived for a
// Trello card before its worker session starts.
type CardClassification struct {
	RuleName string
	GitHub   GitHubRef
}

func classifyGitHubRef(firstLine string) CardClassification {
	issue := router.ParseGitHubIssue(firstLine)
	if !issue.Present() {
		return CardClassification{}
	}
	return CardClassification{
		GitHub: GitHubRef{
			Owner:    issue.Owner,
			Repo:     issue.Repo,
			Number:   issue.Number,
			URL:      issue.URL,
			ItemKind: GitHubItemKind(issue.Kind),
		},
	}
}
