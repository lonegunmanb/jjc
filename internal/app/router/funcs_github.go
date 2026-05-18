package router

import (
	"regexp"
	"strings"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// GitHubIssue is the structured form returned by github_issue(s) and
// reused by callers that need the same deterministic URL parsing outside
// HCL evaluation.
type GitHubIssue struct {
	Owner  string
	Repo   string
	Number string
	Kind   string
	URL    string
}

func (g GitHubIssue) Present() bool {
	return g.Owner != "" && g.Repo != ""
}

// githubURLPattern matches a GitHub issue/PR URL when evaluation starts at
// the URL boundary. ParseGitHubIssue scans for github.com URL starts first so
// embedded URLs still extract while the regex itself remains anchored.
var githubURLPattern = regexp.MustCompile(`^https?://github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/(issues|pull)/(\d+)`)

var githubIssueType = cty.Object(map[string]cty.Type{
	"owner":  cty.String,
	"repo":   cty.String,
	"number": cty.String,
	"kind":   cty.String,
	"url":    cty.String,
})

var githubIssueFunc = function.New(&function.Spec{
	Params: []function.Parameter{{Name: "s", Type: cty.String, AllowNull: true}},
	Type:   function.StaticReturnType(githubIssueType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		if args[0].IsNull() {
			return cty.NullVal(githubIssueType), nil
		}
		issue := ParseGitHubIssue(args[0].AsString())
		if !issue.Present() {
			return cty.NullVal(githubIssueType), nil
		}
		return cty.ObjectVal(map[string]cty.Value{
			"owner":  cty.StringVal(issue.Owner),
			"repo":   cty.StringVal(issue.Repo),
			"number": cty.StringVal(issue.Number),
			"kind":   cty.StringVal(issue.Kind),
			"url":    cty.StringVal(issue.URL),
		}), nil
	},
})

// ParseGitHubIssue extracts the first GitHub issue or PR URL in s. It
// returns the zero value when s contains no recognisable GitHub URL.
func ParseGitHubIssue(s string) GitHubIssue {
	lower := strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		if !strings.HasPrefix(lower[i:], "https://github.com/") &&
			!strings.HasPrefix(lower[i:], "http://github.com/") {
			continue
		}
		m := githubURLPattern.FindStringSubmatch(s[i:])
		if m == nil {
			continue
		}
		kind := "issue"
		if m[3] == "pull" {
			kind = "pr"
		}
		return GitHubIssue{
			Owner:  m[1],
			Repo:   m[2],
			Number: m[4],
			Kind:   kind,
			URL:    m[0],
		}
	}
	return GitHubIssue{}
}
