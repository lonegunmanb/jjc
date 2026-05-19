package prompts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lonegunmanb/jjc/internal/app/kanban"
	"github.com/lonegunmanb/jjc/internal/app/prompttmpl"
)

func TestEmbeddedFilesNonEmpty(t *testing.T) {
	cases := map[string]string{
		"BOOTSTRAP": Bootstrap,
		"IDENTITY":  Identity,
		"WORKER":    Worker,
		"TOOLS":     Tools,
		"USER":      User,
	}
	for name, content := range cases {
		if strings.TrimSpace(content) == "" {
			t.Fatalf("embedded %s.md is empty", name)
		}
	}
}

func TestDefaultsCoversFiveSkeletonPrompts(t *testing.T) {
	d := Defaults()
	want := []string{"BOOTSTRAP.md", "IDENTITY.md", "WORKER.md", "TOOLS.md", "USER.md"}
	for _, name := range want {
		body, ok := d[name]
		if !ok {
			t.Fatalf("Defaults() missing %s", name)
		}
		if strings.TrimSpace(body) == "" {
			t.Fatalf("Defaults()[%s] is empty", name)
		}
	}
	if len(d) != len(want) {
		t.Fatalf("expected %d entries, got %d (%v)", len(want), len(d), d)
	}
}

func TestEmbeddedWorkerMatchesPackageVar(t *testing.T) {
	if EmbeddedWorker() != Worker {
		t.Fatal("EmbeddedWorker should return the package-level Worker string")
	}
}

// sampleResolved builds a kanban.Resolved with every spec key populated
// to a non-empty marker. Used by the guard tests below to drive the
// renderer over the embedded prompts end-to-end.
func sampleResolved() *kanban.Resolved {
	return &kanban.Resolved{
		BoardID: "test-board",
		Plan:    kanban.Role{ID: "plan-id", Name: "PlanList"},
		Action:  kanban.Role{ID: "action-id", Name: "ActionList"},
		Done:    kanban.Role{ID: "done-id", Name: "DoneList"},
		Wait: kanban.WaitRoles{
			PlanReview:   kanban.Role{ID: "wpr-id", Name: "WaitPlanReview"},
			ActionReview: kanban.Role{ID: "war-id", Name: "WaitActionReview"},
			Generic:      kanban.Role{ID: "wg-id", Name: "WaitGeneric"},
			Exception:    kanban.Role{ID: "we-id", Name: "WaitException"},
		},
		AgentCommentPrefixes: []string{"[claw]:"},
		PlanListIDs:          []string{"plan-id"},
		ActionListIDs:        []string{"action-id"},
		DoneListIDs:          []string{"done-id"},
		WaitListIDs:          []string{"wpr-id", "war-id", "wg-id", "we-id"},
	}
}

// TestEmbeddedPromptsRenderWithoutUnknownKanbanKeys is the load-bearing
// guard for PR 2b: every `{{kanban.<key>}}` reference in the embedded
// prompts must resolve against the canonical PromptVars schema. A typo
// or a key drift causes prompttmpl.New to return *RenderError with
// reason=unknown_kanban_key; this test surfaces it at `go test` time
// rather than at gateway startup.
//
// Implementation: write the embedded prompts into a temp dir and let
// the renderer process them with KanbanVars derived from a fully
// populated sample Resolved. Any failure inside the renderer is a
// test failure.
func TestEmbeddedPromptsRenderWithoutUnknownKanbanKeys(t *testing.T) {
	src := t.TempDir()
	for name, body := range Defaults() {
		if werr := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); werr != nil {
			t.Fatalf("write %s: %v", name, werr)
		}
	}

	r, err := prompttmpl.New(prompttmpl.Options{
		PlaybooksDir: src,
		KanbanVars:   sampleResolved().PromptVars(),
	})
	if err != nil {
		t.Fatalf("renderer rejected embedded prompts: %v", err)
	}
	defer func() { _ = r.Cleanup() }()
}

// TestEmbeddedPromptsHaveNoUnsubstitutedKanbanRefs walks each rendered
// embedded prompt and asserts no literal `{{kanban.` substring remains
// in the output. Combined with the renderer's strict-mode error on
// unknown keys, this gives end-to-end confidence that the rewrite is
// complete: every `{{kanban.*}}` reference embedded in the .md files
// is in the spec schema, and the renderer resolved every one.
func TestEmbeddedPromptsHaveNoUnsubstitutedKanbanRefs(t *testing.T) {
	src := t.TempDir()
	for name, body := range Defaults() {
		if werr := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); werr != nil {
			t.Fatalf("write %s: %v", name, werr)
		}
	}

	r, err := prompttmpl.New(prompttmpl.Options{
		PlaybooksDir: src,
		KanbanVars:   sampleResolved().PromptVars(),
	})
	if err != nil {
		t.Fatalf("renderer setup: %v", err)
	}
	defer func() { _ = r.Cleanup() }()

	for _, name := range r.Files() {
		body, rerr := r.Read(name)
		if rerr != nil {
			t.Fatalf("read rendered %s: %v", name, rerr)
		}
		if strings.Contains(body, "{{kanban.") {
			// Report the first 80 chars after each surviving marker
			// so a failure points at the offending location quickly.
			idx := strings.Index(body, "{{kanban.")
			end := idx + 80
			if end > len(body) {
				end = len(body)
			}
			t.Errorf("rendered %s still contains `{{kanban.` at offset %d: %q",
				name, idx, body[idx:end])
		}
	}
}

// TestEmbeddedWorkerHasNoHardCodedListNames is the spec-§5.6 reviewer
// checklist baked into CI. After rewriting, the embedded WORKER.md must
// not anchor any rule on a default English Trello list name; per the
// spec the only acceptable form is `{{kanban.<role>.name}}` / `.id`.
//
// We allow the literals to appear inside:
//   - a `{{kanban...}}` template body itself (impossible, since those
//     don't contain English words like "Analyze"),
//   - a parenthetical "默认列名 ..." annotation (the spec explicitly
//     permits this as a reading aid).
//
// To keep the matcher simple and false-positive-free, we walk each
// line and skip lines that contain the marker "默认列名". Any other
// occurrence of one of the seven defaults is a regression.
func TestEmbeddedWorkerHasNoHardCodedListNames(t *testing.T) {
	defaults := []string{
		"Analyze",
		"In action",
		"Ready for plan review",
		"Ready for review",
		"Pending PR",
		"Need Attention",
	}
	// `Done` deliberately omitted: the English word "done" is too
	// common in narrative to flag mechanically; the §2.4.1-style
	// guard above (unknown_kanban_key on render) catches `{{kanban.done.name}}`
	// drift. Reviewers are still expected to catch English `Done`
	// that means the Trello list, per the §5.6 checklist.

	for _, name := range defaults {
		// Word-boundary match so substrings inside other words (e.g.
		// "Pending PR" inside a longer GitHub label) are not false
		// positives. The Chinese-character context the embedded
		// prompts use means \b works correctly around the literals.
		pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		for lineNum, line := range strings.Split(Worker, "\n") {
			if !pattern.MatchString(line) {
				continue
			}
			if strings.Contains(line, "默认列名") {
				continue // §2.4.1-style reading-aid annotation
			}
			t.Errorf("embedded WORKER.md line %d still hard-codes default list name %q: %s",
				lineNum+1, name, strings.TrimSpace(line))
		}
	}
}

// TestEmbeddedWorkerHasNoHardCodedAgentPrefix is the partner guard for
// the §2.4.1 default-mention rule on the agent comment prefix. The only
// permitted survival is the explicit "默认 `["[agent]:"]`" qualifier in
// the CARD CONTEXT description; everything else must be templated.
func TestEmbeddedWorkerHasNoHardCodedAgentPrefix(t *testing.T) {
	// Match `[agent]:` including any optional trailing space inside the
	// inline-code form `` `[agent]:` ``.
	pattern := regexp.MustCompile(`\[agent\]:`)
	for lineNum, line := range strings.Split(Worker, "\n") {
		if !pattern.MatchString(line) {
			continue
		}
		// §2.4.1 exception #3: line explicitly qualifies the literal
		// as the default value of agent_comment_prefixes.
		if strings.Contains(line, "agent_comment_prefixes") &&
			strings.Contains(line, `["[agent]:"]`) {
			continue
		}
		t.Errorf("embedded WORKER.md line %d still hard-codes `[agent]:`: %s",
			lineNum+1, strings.TrimSpace(line))
	}
}
