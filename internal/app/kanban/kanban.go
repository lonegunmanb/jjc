// Package kanban owns the gateway's resolved view of the Trello board
// the operator wants the worker to act on.
//
// At startup the gateway parses a `kanban {}` block out of router.hcl
// (the "names" layer — what the human types) and then turns every
// human-readable list name into a stable Trello list ID by querying the
// configured board (the "ids" layer — what the router actually matches
// on). The product of both is a *Resolved view that the routing layer
// and the per-card worker CARD CONTEXT consume.
//
// Failure semantics: any role declared in `kanban {}` that cannot be
// matched to exactly one open list on the configured board causes
// LoadAndResolve to return an error containing both the missing roles
// and the ambiguous roles. There is no "partial-resolution" state; the
// gateway is expected to log the error and exit non-zero.
//
// Board lists that are not claimed by any role default to the `wait`
// category. This is deliberately safer than dropping events for such
// lists: cards moved into an unclaimed list trigger `notify_departure`
// (worker winds down) instead of being silently ignored. Every
// unclaimed list is logged once at startup.
//
// This package only owns the `kanban {}` block of router.hcl. The
// broader `route {}` / `rule {}` engines live in separate packages and
// are out of scope here; the HCL decoder ignores any unknown top-level
// block so a router.hcl that mixes kanban with later route/rule blocks
// still loads.
package kanban

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Role holds the resolved name+id pair for a single role declared in
// `kanban {}`. Name is the trimmed-as-typed string from router.hcl
// (preserved for logs and prompt rendering); ID is the corresponding
// Trello list ID.
type Role struct {
	Name string
	ID   string
}

// WaitRoles bundles the four wait.* sub-roles. All of them collapse to
// category=wait at routing time.
type WaitRoles struct {
	PlanReview   Role
	ActionReview Role
	Generic      Role
	Exception    Role
}

// Resolved is the runtime-friendly product of HCL decode + Trello list
// resolution. It is the only kanban type that the routing layer, the
// runner's CARD CONTEXT builder, and any future prompt template see.
//
// Field invariants once LoadAndResolve returns nil error:
//   - BoardID is the configured --kanban-board-id (non-empty).
//   - Plan / Action / Done all have Name AND ID set.
//   - Wait.* (PlanReview, ActionReview, Generic, Exception) all have
//     Name AND ID set.
//   - The per-category *ListIDs slices contain every list ID that
//     should route into that category, including any unclaimed board
//     lists in WaitListIDs.
//   - AgentCommentPrefixes is the verbatim list from router.hcl, never
//     mutated.
type Resolved struct {
	BoardID string

	Plan   Role
	Action Role
	Done   Role
	Wait   WaitRoles

	AgentCommentPrefixes []string

	// Per-category list-id sets. The routing layer matches list IDs
	// against these sets to pick an action. WaitListIDs additionally
	// contains every board list that no role claimed (the "unclaimed
	// → wait" fallback).
	PlanListIDs   []string
	ActionListIDs []string
	WaitListIDs   []string
	DoneListIDs   []string

	// UnclaimedListNames lists every (open) board list whose name did
	// not match any role declaration, in board order. Surface for
	// logging only — routing already merged their IDs into WaitListIDs.
	UnclaimedListNames []string
}

// BoardList is the trimmed-down representation of a Trello list the
// resolver needs. Decoupled from any concrete SDK type so callers (and
// tests) can fake the upstream board without dragging in the full HTTP
// client surface.
type BoardList struct {
	ID   string
	Name string
}

// BoardListsFetcher is the single Trello operation LoadAndResolve
// requires. Production wires it to trelloclient.Client.ListBoardLists;
// tests pass a fake implementation.
type BoardListsFetcher interface {
	ListBoardLists(ctx context.Context, boardID string) ([]BoardList, error)
}

// BoardListsFetcherFunc adapts a plain function to BoardListsFetcher.
type BoardListsFetcherFunc func(ctx context.Context, boardID string) ([]BoardList, error)

// ListBoardLists implements BoardListsFetcher.
func (f BoardListsFetcherFunc) ListBoardLists(ctx context.Context, boardID string) ([]BoardList, error) {
	return f(ctx, boardID)
}

// ResolveError carries the structured details of a failed resolution
// pass. Both MissingRoles and AmbiguousRoles can be populated in the
// same error when several roles fail in different ways; the calling
// site is expected to log both and exit.
type ResolveError struct {
	BoardID         string
	MissingRoles    []string            // role keys (e.g. "plan", "wait.plan_review") that matched zero open lists
	AmbiguousRoles  []string            // role keys that matched more than one open list
	AmbiguousDetail map[string][]string // role key -> the >1 list names that matched
}

// Error renders the resolve failure in a single line suitable for the
// gateway's structured log (event=kanban_resolve_failed missing_roles=...
// ambiguous_roles=...).
func (e *ResolveError) Error() string {
	missing := "[]"
	if len(e.MissingRoles) > 0 {
		missing = "[" + strings.Join(e.MissingRoles, ",") + "]"
	}
	ambig := "[]"
	if len(e.AmbiguousRoles) > 0 {
		ambig = "[" + strings.Join(e.AmbiguousRoles, ",") + "]"
	}
	return fmt.Sprintf("kanban resolve failed: board_id=%s missing_roles=%s ambiguous_roles=%s",
		e.BoardID, missing, ambig)
}

// Resolve maps the human-readable role names in cfg against the open
// lists on the board. It is the pure-logic core; LoadAndResolve wraps
// it with HCL decoding + the Trello fetch.
//
// `lists` is the open-lists slice returned by the BoardListsFetcher and
// is consumed as-is (case-insensitive match after trimming both sides).
// `boardID` is recorded on the returned Resolved for logging.
//
// On any role mismatch Resolve returns a *ResolveError with every
// problem populated. It does NOT return a partial Resolved view.
func Resolve(boardID string, cfg Config, lists []BoardList) (*Resolved, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Build a name (trim+lower) -> []BoardList index of every open list
	// the board returned so we can do exact-match + ambiguity detection
	// in one pass.
	byName := make(map[string][]BoardList, len(lists))
	for _, l := range lists {
		key := normaliseName(l.Name)
		byName[key] = append(byName[key], l)
	}

	type roleSpec struct {
		key  string // dotted role identifier used in errors
		name string // raw list name from cfg
		role *Role  // pointer into the Resolved we are building
	}

	r := &Resolved{
		BoardID:              boardID,
		AgentCommentPrefixes: append([]string(nil), cfg.AgentCommentPrefixes...),
	}
	specs := []roleSpec{
		{"plan", cfg.Plan.Name, &r.Plan},
		{"action", cfg.Action.Name, &r.Action},
		{"wait.plan_review", cfg.Wait.PlanReview.Name, &r.Wait.PlanReview},
		{"wait.action_review", cfg.Wait.ActionReview.Name, &r.Wait.ActionReview},
		{"wait.generic", cfg.Wait.Generic.Name, &r.Wait.Generic},
		{"wait.exception", cfg.Wait.Exception.Name, &r.Wait.Exception},
		{"done", cfg.Done.Name, &r.Done},
	}

	var rerr ResolveError
	rerr.BoardID = boardID
	rerr.AmbiguousDetail = map[string][]string{}

	claimed := map[string]struct{}{}
	for _, s := range specs {
		key := normaliseName(s.name)
		matches := byName[key]
		switch len(matches) {
		case 0:
			rerr.MissingRoles = append(rerr.MissingRoles, s.key)
		case 1:
			s.role.Name = strings.TrimSpace(s.name)
			s.role.ID = matches[0].ID
			claimed[matches[0].ID] = struct{}{}
		default:
			rerr.AmbiguousRoles = append(rerr.AmbiguousRoles, s.key)
			names := make([]string, len(matches))
			for i, m := range matches {
				names[i] = m.Name
			}
			rerr.AmbiguousDetail[s.key] = names
		}
	}

	if len(rerr.MissingRoles) > 0 || len(rerr.AmbiguousRoles) > 0 {
		sort.Strings(rerr.MissingRoles)
		sort.Strings(rerr.AmbiguousRoles)
		return nil, &rerr
	}

	// Per-category list-id sets. Every wait sub-role collapses to
	// category=wait; unclaimed board lists also collapse to wait per
	// the issue's "unclaimed → wait" decision.
	r.PlanListIDs = []string{r.Plan.ID}
	r.ActionListIDs = []string{r.Action.ID}
	r.DoneListIDs = []string{r.Done.ID}
	r.WaitListIDs = []string{
		r.Wait.PlanReview.ID,
		r.Wait.ActionReview.ID,
		r.Wait.Generic.ID,
		r.Wait.Exception.ID,
	}

	for _, l := range lists {
		if _, ok := claimed[l.ID]; ok {
			continue
		}
		r.WaitListIDs = append(r.WaitListIDs, l.ID)
		r.UnclaimedListNames = append(r.UnclaimedListNames, l.Name)
	}

	return r, nil
}

// Category is the routing-level kind a list falls into. It is the
// "门" side of the two-level taxonomy: roles are the user-facing
// classification, categories are what Route uses to pick an action.
type Category int

const (
	// CategoryUnknown is the zero value, returned when a list ID
	// matches no resolved role and no unclaimed list. Callers
	// (routing.go) treat it as "drop / log only".
	CategoryUnknown Category = iota
	CategoryPlan
	CategoryAction
	CategoryWait
	CategoryDone
)

// String renders the category to its lowercase log token.
func (c Category) String() string {
	switch c {
	case CategoryPlan:
		return "plan"
	case CategoryAction:
		return "action"
	case CategoryWait:
		return "wait"
	case CategoryDone:
		return "done"
	default:
		return "unknown"
	}
}

// CategoryForListID returns the category a list ID resolves to. List
// IDs are matched first against the role-specific sets and finally
// against the wait set (which already includes the unclaimed lists).
func (r *Resolved) CategoryForListID(id string) Category {
	if r == nil || id == "" {
		return CategoryUnknown
	}
	if contains(r.PlanListIDs, id) {
		return CategoryPlan
	}
	if contains(r.ActionListIDs, id) {
		return CategoryAction
	}
	if contains(r.DoneListIDs, id) {
		return CategoryDone
	}
	if contains(r.WaitListIDs, id) {
		return CategoryWait
	}
	return CategoryUnknown
}

// CategoryForListName falls back to a case-insensitive name match for
// Trello webhook payloads that only carry the list name (older
// payloads or fields the upstream did not populate). The match is the
// same trim+lower used at resolution time.
func (r *Resolved) CategoryForListName(name string) Category {
	if r == nil || strings.TrimSpace(name) == "" {
		return CategoryUnknown
	}
	key := normaliseName(name)
	switch key {
	case normaliseName(r.Plan.Name):
		return CategoryPlan
	case normaliseName(r.Action.Name):
		return CategoryAction
	case normaliseName(r.Done.Name):
		return CategoryDone
	case normaliseName(r.Wait.PlanReview.Name),
		normaliseName(r.Wait.ActionReview.Name),
		normaliseName(r.Wait.Generic.Name),
		normaliseName(r.Wait.Exception.Name):
		return CategoryWait
	}
	// Unclaimed list names also collapse to wait.
	for _, n := range r.UnclaimedListNames {
		if normaliseName(n) == key {
			return CategoryWait
		}
	}
	return CategoryUnknown
}

// IsAgentComment reports whether `text` (already a raw comment body)
// should be treated as an agent self-comment per the configured prefix
// list. The trimspace + HasPrefix semantics match the issue's spec.
func (r *Resolved) IsAgentComment(text string) bool {
	if r == nil {
		return false
	}
	trimmed := strings.TrimSpace(text)
	for _, p := range r.AgentCommentPrefixes {
		if p == "" {
			continue
		}
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

func normaliseName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
