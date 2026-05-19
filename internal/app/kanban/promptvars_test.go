package kanban

import (
	"strings"
	"testing"
)

func sampleResolved() *Resolved {
	r := &Resolved{
		BoardID: "board-1",
		Plan:    Role{ID: "p-id", Name: "Analyze"},
		Action:  Role{ID: "a-id", Name: "In action"},
		Done:    Role{ID: "d-id", Name: "Done"},
		Wait: WaitRoles{
			PlanReview:   Role{ID: "wpr-id", Name: "Ready for plan review"},
			ActionReview: Role{ID: "war-id", Name: "Ready for review"},
			Generic:      Role{ID: "wg-id", Name: "Pending PR"},
			Exception:    Role{ID: "we-id", Name: "Need Attention"},
		},
		AgentCommentPrefixes: []string{"[agent]:", "[claw]:"},
		PlanListIDs:          []string{"p-id"},
		ActionListIDs:        []string{"a-id"},
		DoneListIDs:          []string{"d-id"},
		WaitListIDs:          []string{"wpr-id", "war-id", "wg-id", "we-id", "unclaimed-1"},
		UnclaimedListNames:   []string{"Backlog"},
	}
	return r
}

func TestPromptVars_NilResolvedReturnsEmptyMap(t *testing.T) {
	var r *Resolved
	got := r.PromptVars()
	if got == nil {
		t.Fatal("expected empty map, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %d entries: %v", len(got), got)
	}
}

func TestPromptVars_ContainsEverySpecKey(t *testing.T) {
	r := sampleResolved()
	got := r.PromptVars()

	// docs/playbook-template-variables.md \u00a72 spec: complete enumeration.
	// Listing them here as a literal slice is the *point* \u2014 if a future
	// edit drops a key from PromptVars without adjusting the spec, this
	// test fails immediately.
	wantKeys := []string{
		PromptVarBoardID,
		PromptVarPlanID, PromptVarPlanName,
		PromptVarActionID, PromptVarActionName,
		PromptVarDoneID, PromptVarDoneName,
		PromptVarWaitPlanReviewID, PromptVarWaitPlanReviewName,
		PromptVarWaitActionReviewID, PromptVarWaitActionReviewName,
		PromptVarWaitGenericID, PromptVarWaitGenericName,
		PromptVarWaitExceptionID, PromptVarWaitExceptionName,
		PromptVarPlanListIDs, PromptVarActionListIDs,
		PromptVarWaitListIDs, PromptVarDoneListIDs,
		PromptVarAgentCommentPrefix, PromptVarAgentCommentPrefixes,
	}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing spec key %q", k)
		}
	}
	if len(got) != len(wantKeys) {
		t.Errorf("PromptVars returns %d keys but spec lists %d \u2014 spec drift?\nactual keys: %v",
			len(got), len(wantKeys), keysOf(got))
	}
}

func TestPromptVars_EveryKeyStartsWithPrefix(t *testing.T) {
	got := sampleResolved().PromptVars()
	for k := range got {
		if !strings.HasPrefix(k, PromptVarPrefix) {
			t.Errorf("key %q does not start with prefix %q", k, PromptVarPrefix)
		}
	}
}

func TestPromptVars_RoleValuesMatchResolved(t *testing.T) {
	r := sampleResolved()
	got := r.PromptVars()

	checks := map[string]string{
		PromptVarBoardID:              "board-1",
		PromptVarPlanID:               "p-id",
		PromptVarPlanName:             "Analyze",
		PromptVarActionID:             "a-id",
		PromptVarActionName:           "In action",
		PromptVarDoneID:               "d-id",
		PromptVarDoneName:             "Done",
		PromptVarWaitPlanReviewID:     "wpr-id",
		PromptVarWaitPlanReviewName:   "Ready for plan review",
		PromptVarWaitActionReviewID:   "war-id",
		PromptVarWaitActionReviewName: "Ready for review",
		PromptVarWaitGenericID:        "wg-id",
		PromptVarWaitGenericName:      "Pending PR",
		PromptVarWaitExceptionID:      "we-id",
		PromptVarWaitExceptionName:    "Need Attention",
	}
	for k, want := range checks {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

func TestPromptVars_ListIDsAggregateJoinedByCommaSpace(t *testing.T) {
	r := sampleResolved()
	got := r.PromptVars()

	if got[PromptVarPlanListIDs] != "p-id" {
		t.Errorf("plan.list_ids = %q, want %q", got[PromptVarPlanListIDs], "p-id")
	}
	if got[PromptVarActionListIDs] != "a-id" {
		t.Errorf("action.list_ids = %q, want %q", got[PromptVarActionListIDs], "a-id")
	}
	if got[PromptVarDoneListIDs] != "d-id" {
		t.Errorf("done.list_ids = %q, want %q", got[PromptVarDoneListIDs], "d-id")
	}
	// wait.list_ids must include every wait sub-role AND the
	// unclaimed list ID that LoadAndResolve merged in, in the same
	// order WaitListIDs holds them.
	wantWait := "wpr-id, war-id, wg-id, we-id, unclaimed-1"
	if got[PromptVarWaitListIDs] != wantWait {
		t.Errorf("wait.list_ids = %q, want %q", got[PromptVarWaitListIDs], wantWait)
	}
}

func TestPromptVars_AgentCommentPrefixIsFirstEntry(t *testing.T) {
	r := sampleResolved()
	got := r.PromptVars()

	if got[PromptVarAgentCommentPrefix] != "[agent]:" {
		t.Errorf("agent_comment_prefix (singular) = %q, want first entry %q",
			got[PromptVarAgentCommentPrefix], "[agent]:")
	}
	if got[PromptVarAgentCommentPrefixes] != "[agent]:, [claw]:" {
		t.Errorf("agent_comment_prefixes (plural, joined) = %q, want %q",
			got[PromptVarAgentCommentPrefixes], "[agent]:, [claw]:")
	}
}

func TestPromptVars_AgentCommentPrefixSkipsEmptyFirstEntry(t *testing.T) {
	// Defence against a malformed router.hcl that declared an empty
	// string at index 0 \u2014 spec mandates the *first non-empty* prefix.
	r := sampleResolved()
	r.AgentCommentPrefixes = []string{"", "[claw]:"}
	got := r.PromptVars()
	if got[PromptVarAgentCommentPrefix] != "[claw]:" {
		t.Errorf("singular prefix = %q, want %q (first non-empty)",
			got[PromptVarAgentCommentPrefix], "[claw]:")
	}
}

func TestPromptVars_EmptyAgentPrefixesProduceEmptyStrings(t *testing.T) {
	r := sampleResolved()
	r.AgentCommentPrefixes = nil
	got := r.PromptVars()
	if got[PromptVarAgentCommentPrefix] != "" {
		t.Errorf("singular prefix with no entries = %q, want empty", got[PromptVarAgentCommentPrefix])
	}
	if got[PromptVarAgentCommentPrefixes] != "" {
		t.Errorf("plural prefix with no entries = %q, want empty", got[PromptVarAgentCommentPrefixes])
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDefaultAgentCommentPrefix_NilResolved(t *testing.T) {
	var r *Resolved
	if got := r.DefaultAgentCommentPrefix(); got != "" {
		t.Errorf("nil receiver = %q, want empty", got)
	}
}

func TestDefaultAgentCommentPrefix_FirstEntry(t *testing.T) {
	r := sampleResolved() // []string{"[agent]:", "[claw]:"}
	if got := r.DefaultAgentCommentPrefix(); got != "[agent]:" {
		t.Errorf("got %q, want first entry %q", got, "[agent]:")
	}
}

func TestDefaultAgentCommentPrefix_SkipsEmptyFirstEntry(t *testing.T) {
	r := sampleResolved()
	r.AgentCommentPrefixes = []string{"", "[claw]:"}
	if got := r.DefaultAgentCommentPrefix(); got != "[claw]:" {
		t.Errorf("got %q, want first non-empty %q", got, "[claw]:")
	}
}

func TestDefaultAgentCommentPrefix_EmptySliceReturnsEmpty(t *testing.T) {
	r := sampleResolved()
	r.AgentCommentPrefixes = nil
	if got := r.DefaultAgentCommentPrefix(); got != "" {
		t.Errorf("got %q, want empty when no prefixes configured", got)
	}
}

// TestDefaultAgentCommentPrefix_MatchesPromptVar is the load-bearing
// invariant: the Go-side helper and the kanban.agent_comment_prefix
// template variable must always agree. If they ever diverge, a
// playbook would render one prefix while Go-side callers (e.g.
// outgoing-comment validators) would assume another.
func TestDefaultAgentCommentPrefix_MatchesPromptVar(t *testing.T) {
	cases := []struct {
		name     string
		prefixes []string
	}{
		{"single", []string{"[agent]:"}},
		{"two", []string{"[agent]:", "[claw]:"}},
		{"empty-first", []string{"", "[claw]:"}},
		{"empty-slice", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := sampleResolved()
			r.AgentCommentPrefixes = tc.prefixes
			got := r.DefaultAgentCommentPrefix()
			fromVars := r.PromptVars()[PromptVarAgentCommentPrefix]
			if got != fromVars {
				t.Errorf("Go helper = %q, PromptVars[%s] = %q (must agree)",
					got, PromptVarAgentCommentPrefix, fromVars)
			}
		})
	}
}
