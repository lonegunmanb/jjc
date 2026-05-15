package app

import (
	"fmt"
	"regexp"
)

// cardIDPattern is the conservative shape we accept for a Trello card id.
// Production Trello ids are 24-char hex; the pattern is kept slightly
// looser so synthetic ids used in unit tests, the REPL, and demo
// fixtures (e.g. "card-1", "test_card") still fit. The intent is to
// reject anything that could escape a filesystem path: path separators
// (`/`, `\`), parent-dir traversal (`..`), Windows drive prefixes (`:`),
// shell metacharacters, NULs, whitespace, and the like. Allowed: ASCII
// alphanumerics plus `-` and `_`, length 3-64.
var cardIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)

// ValidateCardID returns an error when id is not a syntactically safe
// Trello card id. The check is intentionally cheap and side-effect-free:
// it must run on every webhook before id flows into a filesystem path,
// log line, or external process argument.
func ValidateCardID(id string) error {
	if id == "" {
		return fmt.Errorf("card id is empty")
	}
	if !cardIDPattern.MatchString(id) {
		return fmt.Errorf("card id %q is not a safe slug (expected %s)",
			id, cardIDPattern.String())
	}
	// Defence in depth: even if a future regex tweak accidentally lets
	// `..` through, an explicit ContainsAny / `..` check makes path
	// traversal impossible to introduce silently.
	if containsPathTraversal(id) {
		return fmt.Errorf("card id %q contains path traversal characters", id)
	}
	return nil
}

// containsPathTraversal explicitly screens for the byte sequences that
// matter to filepath.Join. Belt-and-braces with cardIDPattern.
func containsPathTraversal(id string) bool {
	if id == "." || id == ".." {
		return true
	}
	for _, r := range id {
		switch r {
		case '/', '\\', ':', 0:
			return true
		}
	}
	return false
}

// IsValidCardID is the boolean form of ValidateCardID, handy in routing
// expressions where the caller only needs a yes/no answer.
func IsValidCardID(id string) bool { return ValidateCardID(id) == nil }
