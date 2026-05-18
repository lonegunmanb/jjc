package kanban

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sampleHCL is the canonical `kanban {}` block from the issue. Tests
// reuse it so the decode path is exercised on the exact bytes a real
// operator would write.
const sampleHCL = `
kanban {
  plan   { name = "Analyze" }
  action { name = "In action" }
  wait {
    plan_review   { name = "Ready for plan review" }
    action_review { name = "Ready for review" }
    generic       { name = "Pending PR" }
    exception     { name = "Need Attention" }
  }
  done   { name = "Done" }
  agent_comment_prefixes = ["[agent]:"]
}
`

func sampleConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := DecodeConfig([]byte(sampleHCL), "router.hcl")
	if err != nil {
		t.Fatalf("DecodeConfig sample: %v", err)
	}
	return cfg
}

func happyBoardLists() []BoardList {
	return []BoardList{
		{ID: "L_PLAN", Name: "Analyze"},
		{ID: "L_ACTION", Name: "In action"},
		{ID: "L_RPR", Name: "Ready for plan review"},
		{ID: "L_RR", Name: "Ready for review"},
		{ID: "L_PPR", Name: "Pending PR"},
		{ID: "L_NA", Name: "Need Attention"},
		{ID: "L_DONE", Name: "Done"},
	}
}

func TestDecodeConfig_Sample(t *testing.T) {
	cfg := sampleConfig(t)
	if cfg.Plan.Name != "Analyze" {
		t.Fatalf("plan name: %q", cfg.Plan.Name)
	}
	if cfg.Wait.PlanReview.Name != "Ready for plan review" {
		t.Fatalf("wait.plan_review: %q", cfg.Wait.PlanReview.Name)
	}
	if got := cfg.AgentCommentPrefixes; len(got) != 1 || got[0] != "[agent]:" {
		t.Fatalf("agent_comment_prefixes: %v", got)
	}
}

func TestDecodeConfig_IgnoresUnknownBlocks(t *testing.T) {
	// router.hcl is going to grow `route` / `rule` blocks in later
	// issues; the kanban decoder MUST tolerate those.
	mixed := sampleHCL + `
route "x" {
  when = true
  do   = "drop"
  reason = "x"
}
rule "y" {
  when = true
  prompts = []
}
`
	if _, err := DecodeConfig([]byte(mixed), "router.hcl"); err != nil {
		t.Fatalf("decode with unknown top-level blocks: %v", err)
	}
}

func TestDecodeConfig_DuplicateNameRejected(t *testing.T) {
	dup := `
kanban {
  plan   { name = "Same" }
  action { name = "SAME" }
  wait {
    plan_review   { name = "rpr" }
    action_review { name = "rr" }
    generic       { name = "ppr" }
    exception     { name = "na" }
  }
  done { name = "done" }
  agent_comment_prefixes = ["[agent]:"]
}
`
	_, err := DecodeConfig([]byte(dup), "router.hcl")
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Fatalf("error should mention collision: %v", err)
	}
}

func TestDecodeConfig_MissingRoleBlockRejected(t *testing.T) {
	missing := `
kanban {
  plan   { name = "Analyze" }
  action { name = "In action" }
  wait {
    plan_review   { name = "Ready for plan review" }
    action_review { name = "Ready for review" }
    generic       { name = "Pending PR" }
    exception     { name = "Need Attention" }
  }
  agent_comment_prefixes = ["[agent]:"]
}
`
	if _, err := DecodeConfig([]byte(missing), "router.hcl"); err == nil {
		t.Fatal("expected error when `done` block is missing")
	}
}

func TestDecodeConfig_NoKanbanBlockRejected(t *testing.T) {
	if _, err := DecodeConfig([]byte(`route "x" { when = true do = "drop" reason = "x" }`), "router.hcl"); err == nil {
		t.Fatal("expected error when no kanban block is present")
	}
}

// ---- Resolve path: 4 scenarios from the issue's acceptance criteria ----

func TestResolve_HappyPath(t *testing.T) {
	cfg := sampleConfig(t)
	res, err := Resolve("B1", cfg, happyBoardLists())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.BoardID != "B1" {
		t.Fatalf("board id: %s", res.BoardID)
	}
	if res.Plan.ID != "L_PLAN" || res.Plan.Name != "Analyze" {
		t.Fatalf("plan: %+v", res.Plan)
	}
	if res.Done.ID != "L_DONE" {
		t.Fatalf("done id: %s", res.Done.ID)
	}
	if got := res.PlanListIDs; len(got) != 1 || got[0] != "L_PLAN" {
		t.Fatalf("plan ids: %v", got)
	}
	if got := res.WaitListIDs; len(got) != 4 {
		t.Fatalf("wait ids (no unclaimed): %v", got)
	}
	if len(res.UnclaimedListNames) != 0 {
		t.Fatalf("unclaimed: %v", res.UnclaimedListNames)
	}
	if res.CategoryForListID("L_ACTION") != CategoryAction {
		t.Fatal("L_ACTION should be CategoryAction")
	}
	if res.CategoryForListID("L_DONE") != CategoryDone {
		t.Fatal("L_DONE should be CategoryDone")
	}
	if res.CategoryForListID("L_NA") != CategoryWait {
		t.Fatal("L_NA should be CategoryWait")
	}
	if res.CategoryForListID("does-not-exist") != CategoryUnknown {
		t.Fatal("unknown id must map to CategoryUnknown")
	}
	if !res.IsAgentComment("[agent]: status") {
		t.Fatal("[agent]: prefix should mark comment as agent-authored")
	}
	if res.IsAgentComment("human comment") {
		t.Fatal("plain text should not be agent-authored")
	}
}

func TestResolve_MissingRole(t *testing.T) {
	cfg := sampleConfig(t)
	lists := happyBoardLists()
	// Drop the "Done" list to simulate the role having zero matches.
	var trimmed []BoardList
	for _, l := range lists {
		if l.Name != "Done" {
			trimmed = append(trimmed, l)
		}
	}
	_, err := Resolve("B1", cfg, trimmed)
	if err == nil {
		t.Fatal("expected resolve error for missing role")
	}
	var rerr *ResolveError
	if !errors.As(err, &rerr) {
		t.Fatalf("error is not *ResolveError: %T", err)
	}
	if want := []string{"done"}; !equalStrings(rerr.MissingRoles, want) {
		t.Fatalf("missing roles: %v want %v", rerr.MissingRoles, want)
	}
	if !strings.Contains(rerr.Error(), "missing_roles=[done]") {
		t.Fatalf("Error() should be log-friendly: %s", rerr.Error())
	}
}

func TestResolve_AmbiguousRole(t *testing.T) {
	cfg := sampleConfig(t)
	lists := happyBoardLists()
	// Two open lists both named "Done" (different IDs) -> ambiguous.
	lists = append(lists, BoardList{ID: "L_DONE_DUP", Name: "done"})
	_, err := Resolve("B1", cfg, lists)
	if err == nil {
		t.Fatal("expected resolve error for ambiguous role")
	}
	var rerr *ResolveError
	if !errors.As(err, &rerr) {
		t.Fatalf("error is not *ResolveError: %T", err)
	}
	if want := []string{"done"}; !equalStrings(rerr.AmbiguousRoles, want) {
		t.Fatalf("ambiguous roles: %v want %v", rerr.AmbiguousRoles, want)
	}
	if got := rerr.AmbiguousDetail["done"]; len(got) != 2 {
		t.Fatalf("ambiguous detail for done: %v", got)
	}
}

func TestResolve_UnclaimedListFallsToWait(t *testing.T) {
	cfg := sampleConfig(t)
	lists := append(happyBoardLists(), BoardList{ID: "L_INBOX", Name: "Inbox"})
	res, err := Resolve("B1", cfg, lists)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !contains(res.WaitListIDs, "L_INBOX") {
		t.Fatalf("WaitListIDs should include the unclaimed list: %v", res.WaitListIDs)
	}
	if !contains(res.UnclaimedListNames, "Inbox") {
		t.Fatalf("UnclaimedListNames should record Inbox: %v", res.UnclaimedListNames)
	}
	if res.CategoryForListID("L_INBOX") != CategoryWait {
		t.Fatal("unclaimed list must route as wait")
	}
}

// ---- LoadAndResolve integration ----

type stubFetcher struct {
	lists []BoardList
	err   error
	calls int
}

func (s *stubFetcher) ListBoardLists(_ context.Context, _ string) ([]BoardList, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return append([]BoardList(nil), s.lists...), nil
}

func writeHCL(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "router.hcl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write router.hcl: %v", err)
	}
	return path
}

func TestLoadAndResolve_HappyPath(t *testing.T) {
	path := writeHCL(t, sampleHCL)
	fetcher := &stubFetcher{lists: happyBoardLists()}
	res, err := LoadAndResolve(context.Background(), path, "B1", fetcher,
		log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("LoadAndResolve: %v", err)
	}
	if res.Plan.ID != "L_PLAN" || res.Done.ID != "L_DONE" {
		t.Fatalf("unexpected resolved roles: %+v", res)
	}
}

func TestLoadAndResolve_UnclaimedListLogged(t *testing.T) {
	path := writeHCL(t, sampleHCL)
	lists := append(happyBoardLists(), BoardList{ID: "L_INBOX", Name: "Inbox"})
	fetcher := &stubFetcher{lists: lists}

	var buf strings.Builder
	logger := log.New(&buf, "", 0)

	res, err := LoadAndResolve(context.Background(), path, "B1", fetcher, logger)
	if err != nil {
		t.Fatalf("LoadAndResolve: %v", err)
	}
	if !contains(res.WaitListIDs, "L_INBOX") {
		t.Fatalf("WaitListIDs should include L_INBOX: %v", res.WaitListIDs)
	}
	logs := buf.String()
	if !strings.Contains(logs, "event=kanban_unclaimed_list") ||
		!strings.Contains(logs, `name="Inbox"`) ||
		!strings.Contains(logs, "id=L_INBOX") ||
		!strings.Contains(logs, "fallback_category=wait") {
		t.Fatalf("expected unclaimed-list WARN line, got: %s", logs)
	}
}

func TestLoadAndResolve_BoardIDMissing(t *testing.T) {
	path := writeHCL(t, sampleHCL)
	_, err := LoadAndResolve(context.Background(), path, "",
		&stubFetcher{lists: happyBoardLists()}, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("expected error when board id is empty")
	}
}

func TestLoadAndResolve_FetchError(t *testing.T) {
	path := writeHCL(t, sampleHCL)
	fetcher := &stubFetcher{err: errors.New("trello 401")}
	_, err := LoadAndResolve(context.Background(), path, "B1", fetcher,
		log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("expected error when fetch fails")
	}
	if !strings.Contains(err.Error(), "fetch board lists") {
		t.Fatalf("error should mention fetch: %v", err)
	}
}

// ---- helpers ----

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
