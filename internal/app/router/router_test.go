package router

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lonegunmanb/trello-copilot/internal/app/kanban"
)

// sampleRoutesHCL is a verbatim copy of the route-block portion of
// examples/router/router.hcl. Kept in-line (rather than read from
// disk) so tests fail loudly when the schema changes without the
// example being updated in lock-step.
const sampleRoutesHCL = `
route "no_card_id" {
  when   = action.card_id == ""
  do     = "drop"
  reason = "no_card_id"
}

route "updateCard_no_list_move" {
  when   = action.type == "updateCard" && action.list_after == ""
  do     = "drop"
  reason = "updateCard_no_list_move"
}

route "moved_to_done" {
  when   = action.type == "updateCard" && contains(kanban.done_lists, lower(action.list_after))
  do     = "terminate"
  reason = "moved_to_done"
}

route "moved_to_plan_list" {
  when   = action.type == "updateCard" && contains(kanban.plan_lists, lower(action.list_after))
  do     = "dispatch"
  reason = "moved_to_plan_list"
}

route "moved_to_action_list" {
  when   = action.type == "updateCard" && contains(kanban.action_lists, lower(action.list_after))
  do     = "dispatch"
  reason = "moved_to_action_list"
}

route "moved_to_wait_list" {
  when   = action.type == "updateCard" && contains(kanban.wait_lists, lower(action.list_after))
  do     = "notify_departure"
  reason = "moved_to_wait_list"
}

route "moved_to_unknown_list" {
  when   = action.type == "updateCard" && action.list_after != ""
  do     = "drop"
  reason = "moved_to_unknown_list"
}

route "created_in_plan_list" {
  when   = action.type == "createCard" && contains(kanban.plan_lists, lower(action.list_name))
  do     = "dispatch"
  reason = "created_in_plan_list"
}

route "created_in_action_list" {
  when   = action.type == "createCard" && contains(kanban.action_lists, lower(action.list_name))
  do     = "dispatch"
  reason = "created_in_action_list"
}

route "created_in_non_active_list" {
  when   = action.type == "createCard"
  do     = "drop"
  reason = "created_in_non_active_list"
}

route "agent_self_comment" {
  when   = action.type == "commentCard" && anytrue([for p in kanban.agent_comment_prefixes : startswith(trimspace(action.comment), p)])
  do     = "drop"
  reason = "agent_self_comment"
}

route "human_comment" {
  when   = action.type == "commentCard"
  do     = "dispatch"
  reason = "human_comment"
}

route "card_deleted" {
  when   = action.type == "deleteCard"
  do     = "terminate"
  reason = "card_deleted"
}

route "deleteComment_not_handled" {
  when   = action.type == "deleteComment"
  do     = "drop"
  reason = "deleteComment_not_handled"
}

route "unsupported_action_type" {
  when   = true
  do     = "drop"
  reason = "unsupported_action_type"
}
`

func sampleView() *kanban.Resolved {
	return &kanban.Resolved{
		BoardID: "B1",
		Plan:    kanban.Role{Name: "Analyze", ID: "L_PLAN"},
		Action:  kanban.Role{Name: "In action", ID: "L_ACTION"},
		Done:    kanban.Role{Name: "Done", ID: "L_DONE"},
		Wait: kanban.WaitRoles{
			PlanReview:   kanban.Role{Name: "Ready for plan review", ID: "L_RPR"},
			ActionReview: kanban.Role{Name: "Ready for review", ID: "L_RR"},
			Generic:      kanban.Role{Name: "Pending PR", ID: "L_PPR"},
			Exception:    kanban.Role{Name: "Need Attention", ID: "L_NA"},
		},
		AgentCommentPrefixes: []string{"[agent]:", "[bot]:"},
		UnclaimedListNames:   []string{"Inbox"},
	}
}

func newSampleEngine(t *testing.T) (*Engine, *bytes.Buffer) {
	t.Helper()
	cfg, err := DecodeConfig([]byte(sampleRoutesHCL), "sample.hcl")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	return NewEngine(cfg, sampleView(), logger), &buf
}

// TestEngineEvaluate pins the table of "input event -> decision" the
// HCL engine produces for the sample router rules. It is the contract
// the dispatcher will depend on once the routing.go switch is removed.
func TestEngineEvaluate(t *testing.T) {
	engine, _ := newSampleEngine(t)

	cases := []struct {
		name       string
		ev         Event
		wantRoute  string
		wantDo     string
		wantReason string
	}{
		{
			name:       "no card id",
			ev:         Event{Type: "updateCard"},
			wantRoute:  "no_card_id",
			wantDo:     ActionDrop,
			wantReason: "no_card_id",
		},
		{
			name:       "updateCard without list move",
			ev:         Event{Type: "updateCard", CardID: "c1"},
			wantRoute:  "updateCard_no_list_move",
			wantDo:     ActionDrop,
			wantReason: "updateCard_no_list_move",
		},
		{
			name:       "moved to plan list",
			ev:         Event{Type: "updateCard", CardID: "c1", ListAfter: "Analyze"},
			wantRoute:  "moved_to_plan_list",
			wantDo:     ActionDispatch,
			wantReason: "moved_to_plan_list",
		},
		{
			name:       "moved to action list (case-insensitive)",
			ev:         Event{Type: "updateCard", CardID: "c1", ListAfter: "In Action"},
			wantRoute:  "moved_to_action_list",
			wantDo:     ActionDispatch,
			wantReason: "moved_to_action_list",
		},
		{
			name:       "moved to done",
			ev:         Event{Type: "updateCard", CardID: "c1", ListAfter: "Done"},
			wantRoute:  "moved_to_done",
			wantDo:     ActionTerminate,
			wantReason: "moved_to_done",
		},
		{
			name:       "moved to wait list",
			ev:         Event{Type: "updateCard", CardID: "c1", ListAfter: "Ready for review"},
			wantRoute:  "moved_to_wait_list",
			wantDo:     ActionNotifyDeparture,
			wantReason: "moved_to_wait_list",
		},
		{
			name:       "moved to unclaimed list collapses to wait via UnclaimedListNames",
			ev:         Event{Type: "updateCard", CardID: "c1", ListAfter: "Inbox"},
			wantRoute:  "moved_to_wait_list",
			wantDo:     ActionNotifyDeparture,
			wantReason: "moved_to_wait_list",
		},
		{
			name:       "moved to list nobody knows",
			ev:         Event{Type: "updateCard", CardID: "c1", ListAfter: "Mystery"},
			wantRoute:  "moved_to_unknown_list",
			wantDo:     ActionDrop,
			wantReason: "moved_to_unknown_list",
		},
		{
			name:       "createCard in plan list",
			ev:         Event{Type: "createCard", CardID: "c1", ListName: "Analyze"},
			wantRoute:  "created_in_plan_list",
			wantDo:     ActionDispatch,
			wantReason: "created_in_plan_list",
		},
		{
			name:       "createCard in non-active list",
			ev:         Event{Type: "createCard", CardID: "c1", ListName: "Need Attention"},
			wantRoute:  "created_in_non_active_list",
			wantDo:     ActionDrop,
			wantReason: "created_in_non_active_list",
		},
		{
			name:       "agent self comment via [agent]: prefix",
			ev:         Event{Type: "commentCard", CardID: "c1", Comment: "[agent]: status"},
			wantRoute:  "agent_self_comment",
			wantDo:     ActionDrop,
			wantReason: "agent_self_comment",
		},
		{
			name:       "agent self comment via [bot]: prefix",
			ev:         Event{Type: "commentCard", CardID: "c1", Comment: "  [bot]: heartbeat"},
			wantRoute:  "agent_self_comment",
			wantDo:     ActionDrop,
			wantReason: "agent_self_comment",
		},
		{
			name:       "human comment dispatches",
			ev:         Event{Type: "commentCard", CardID: "c1", Comment: "please retry"},
			wantRoute:  "human_comment",
			wantDo:     ActionDispatch,
			wantReason: "human_comment",
		},
		{
			name:       "card deleted terminates",
			ev:         Event{Type: "deleteCard", CardID: "c1"},
			wantRoute:  "card_deleted",
			wantDo:     ActionTerminate,
			wantReason: "card_deleted",
		},
		{
			name:       "deleteComment not handled",
			ev:         Event{Type: "deleteComment", CardID: "c1"},
			wantRoute:  "deleteComment_not_handled",
			wantDo:     ActionDrop,
			wantReason: "deleteComment_not_handled",
		},
		{
			name:       "unknown action type hits catch-all",
			ev:         Event{Type: "addLabelToCard", CardID: "c1"},
			wantRoute:  "unsupported_action_type",
			wantDo:     ActionDrop,
			wantReason: "unsupported_action_type",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := engine.Evaluate(tc.ev)
			if got.Route != tc.wantRoute || got.Do != tc.wantDo || got.Reason != tc.wantReason {
				t.Errorf("Evaluate(%+v) = %+v; want route=%q do=%q reason=%q",
					tc.ev, got, tc.wantRoute, tc.wantDo, tc.wantReason)
			}
		})
	}
}

// TestEngineFirstMatchWins pins the "top-down, first when==true wins"
// semantic. The action_review wait list also literally contains the
// word "review" — making sure no later route ever overrides the
// notify_departure decision we want.
func TestEngineFirstMatchWins(t *testing.T) {
	src := `
route "first" {
  when   = action.card_id == "c1"
  do     = "dispatch"
  reason = "first_match"
}
route "second" {
  when   = action.card_id == "c1"
  do     = "drop"
  reason = "second_should_not_match"
}
`
	cfg, err := DecodeConfig([]byte(src), "fm.hcl")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	e := NewEngine(cfg, nil, log.New(&bytes.Buffer{}, "", 0))
	got := e.Evaluate(Event{CardID: "c1"})
	if got.Route != "first" || got.Reason != "first_match" {
		t.Errorf("first-match-wins broken: got %+v", got)
	}
}

// TestEngineSkipOnEvalError exercises the "rule errors at evaluation
// time -> log and continue" failure mode. The first rule references a
// missing attribute on action.* (HCL2 raises an unsupported-attribute
// diagnostic), the second rule must still get a chance to match.
func TestEngineSkipOnEvalError(t *testing.T) {
	src := `
route "broken" {
  when   = action.nonexistent_field == "x"
  do     = "dispatch"
  reason = "broken_rule_should_be_skipped"
}
route "good" {
  when   = action.type == "updateCard"
  do     = "dispatch"
  reason = "good_rule_matched"
}
`
	cfg, err := DecodeConfig([]byte(src), "skip.hcl")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	var buf bytes.Buffer
	e := NewEngine(cfg, nil, log.New(&buf, "", 0))
	got := e.Evaluate(Event{Type: "updateCard", CardID: "c1"})
	if got.Route != "good" {
		t.Errorf("expected fallthrough to good rule, got %+v", got)
	}
	if !strings.Contains(buf.String(), "event=route_when_eval_error") ||
		!strings.Contains(buf.String(), `rule="broken"`) {
		t.Errorf("expected route_when_eval_error log for broken rule, got: %q", buf.String())
	}
}

// TestEngineNoRouteMatched exercises the safety-net code path that
// fires when the user accidentally removes the catch-all. The engine
// must NOT panic / abort — it must return a synthetic drop decision
// and log the no-match event for the operator to notice.
func TestEngineNoRouteMatched(t *testing.T) {
	src := `
route "only_known_type" {
  when   = action.type == "updateCard"
  do     = "dispatch"
  reason = "only_known_type"
}
`
	cfg, err := DecodeConfig([]byte(src), "nomatch.hcl")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	var buf bytes.Buffer
	e := NewEngine(cfg, nil, log.New(&buf, "", 0))
	got := e.Evaluate(Event{Type: "createCard", CardID: "c42"})
	if got.Do != ActionDrop || got.Reason != "router_no_route_matched" {
		t.Errorf("expected synthetic drop on no-match, got %+v", got)
	}
	if !strings.Contains(buf.String(), "event=router_no_route_matched") ||
		!strings.Contains(buf.String(), `action_type="createCard"`) ||
		!strings.Contains(buf.String(), `card_id="c42"`) {
		t.Errorf("expected router_no_route_matched log line, got: %q", buf.String())
	}
}

// TestDecodeConfigRejectsInvalidDo validates the load-time guardrail:
// a typo in `do` must fail at startup, not silently degrade at the
// first matching event.
func TestDecodeConfigRejectsInvalidDo(t *testing.T) {
	src := `
route "typo" {
  when   = true
  do     = "dipsatch"
  reason = "typo"
}
`
	_, err := DecodeConfig([]byte(src), "bad.hcl")
	if err == nil {
		t.Fatal("expected error for invalid do, got nil")
	}
	if !strings.Contains(err.Error(), "invalid do") {
		t.Errorf("expected invalid-do error, got: %v", err)
	}
}

// TestDecodeConfigRejectsDuplicateRoute pins the "duplicate route
// label" guardrail. Two rules with the same label make the log line
// `rule=X` ambiguous which is bad for after-the-fact forensics.
func TestDecodeConfigRejectsDuplicateRoute(t *testing.T) {
	src := `
route "dup" {
  when   = true
  do     = "drop"
  reason = "one"
}
route "dup" {
  when   = true
  do     = "drop"
  reason = "two"
}
`
	_, err := DecodeConfig([]byte(src), "dup.hcl")
	if err == nil || !strings.Contains(err.Error(), "duplicate route") {
		t.Errorf("expected duplicate-route error, got: %v", err)
	}
}

// TestDecodeConfigIgnoresUnknownTopLevelBlocks pins the PartialContent
// guarantee: a router.hcl carrying `kanban {}` and (future) `rule {}`
// blocks alongside `route {}` blocks must load without error.
func TestDecodeConfigIgnoresUnknownTopLevelBlocks(t *testing.T) {
	src := `
kanban {
  plan { name = "Analyze" }
}

rule "future" {
  when    = true
  prompts = []
}

route "only_route" {
  when   = true
  do     = "drop"
  reason = "only_route"
}
`
	cfg, err := DecodeConfig([]byte(src), "mixed.hcl")
	if err != nil {
		t.Fatalf("DecodeConfig should ignore unknown top-level blocks: %v", err)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Name != "only_route" {
		t.Errorf("expected exactly one route 'only_route', got %+v", cfg.Routes)
	}
}

// TestLoadConfigFromExampleFile exercises the on-disk load path
// against the canonical examples/router/router.hcl. Catching schema
// drift between docs and engine at test time is exactly the failure
// mode the issue calls out (`Path is <router-dir>/router.hcl`).
func TestLoadConfigFromExampleFile(t *testing.T) {
	// repo-relative path from internal/app/router up to repo root.
	path := filepath.Join("..", "..", "..", "examples", "router", "router.hcl")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("example router.hcl not found at %s: %v", path, err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", path, err)
	}
	if len(cfg.Routes) == 0 {
		t.Fatal("expected at least one route from example router.hcl")
	}
	// The example file's last route is the catch-all; assert it stays
	// that way so future doc edits don't quietly remove the safety net.
	last := cfg.Routes[len(cfg.Routes)-1]
	if last.Name != "unsupported_action_type" || last.Do != ActionDrop {
		t.Errorf("expected catch-all unsupported_action_type drop as last route, got %+v", last)
	}
}
