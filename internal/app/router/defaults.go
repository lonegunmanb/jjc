package router

import "github.com/lonegunmanb/jjc/internal/app/kanban"

// DefaultRoutesHCL is the canonical `route {}` block set that mirrors
// the legacy Go switch in internal/app/routing.go::Route(). It is
// embedded as a string (rather than read from
// examples/router/router.hcl at runtime) for two reasons:
//
//  1. Unit tests and the dispatcher need a default rule set to fall
//     back on when no operator-provided router.hcl is wired in. Going
//     to disk for this would couple every test invocation to the
//     example file's path.
//
//  2. Keeping a verbatim copy here forces any change to the example
//     file to be mirrored — TestDefaultRoutesHCLMatchesExample
//     compares them byte-for-byte (for the `route {}` portion).
//
// The route order matters. It mirrors the order the legacy switch
// would have evaluated.
const DefaultRoutesHCL = `
route "no_card_id" {
  when   = action.card_id == ""
  do     = "drop"
  reason = "no_card_id"
}

route "invalid_card_id" {
  when   = action.card_id != "" && !action.card_id_valid
  do     = "drop"
  reason = "invalid_card_id"
}

route "updateCard_no_list_move" {
  when   = action.type == "updateCard" && action.list_after_id == "" && action.list_after == ""
  do     = "drop"
  reason = "updateCard_no_list_move"
}

route "moved_to_done" {
  when   = action.type == "updateCard" && (
             contains(kanban.done_list_ids, action.list_after_id)
             || contains(kanban.done_lists, lower(action.list_after))
           )
  do     = "terminate"
  reason = "moved_to_done"
}

route "moved_to_plan_list" {
  when   = action.type == "updateCard" && (
             contains(kanban.plan_list_ids, action.list_after_id)
             || contains(kanban.plan_lists, lower(action.list_after))
           )
  do     = "dispatch"
  reason = "moved_to_active_list"
}

route "moved_to_action_list" {
  when   = action.type == "updateCard" && (
             contains(kanban.action_list_ids, action.list_after_id)
             || contains(kanban.action_lists, lower(action.list_after))
           )
  do     = "dispatch"
  reason = "moved_to_active_list"
}

route "moved_to_wait_list" {
  when   = action.type == "updateCard" && (action.list_after_id != "" || action.list_after != "")
  do     = "notify_departure"
  reason = "moved_to_non_active_list"
}

route "created_in_plan_list" {
  when   = action.type == "createCard" && (
             contains(kanban.plan_list_ids, action.list_id)
             || contains(kanban.plan_lists, lower(action.list_name))
           )
  do     = "dispatch"
  reason = "created_in_active_list"
}

route "created_in_action_list" {
  when   = action.type == "createCard" && (
             contains(kanban.action_list_ids, action.list_id)
             || contains(kanban.action_lists, lower(action.list_name))
           )
  do     = "dispatch"
  reason = "created_in_active_list"
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

// DefaultLegacyKanbanView returns a *kanban.Resolved seeded with the
// hard-coded list names the legacy routing.go switch used when no
// resolved view was wired in. It exists so unit tests that exercise
// the dispatcher without standing up a real Trello board observe the
// same plan / action / done routing as they did before the engine
// landed.
//
// This intentionally does NOT populate list IDs — the default engine
// matches on names only, which is exactly what the legacy switch did.
// Production wires a real *kanban.Resolved (from kanban.LoadAndResolve)
// and never observes this fallback.
func DefaultLegacyKanbanView() *kanban.Resolved {
	return &kanban.Resolved{
		Plan:                 kanban.Role{Name: "Analyze"},
		Action:               kanban.Role{Name: "In action"},
		Done:                 kanban.Role{Name: "Done"},
		AgentCommentPrefixes: []string{"[agent]:"},
	}
}

// MustNewDefaultEngine builds an Engine seeded with DefaultRoutesHCL
// and DefaultLegacyKanbanView. It is the fallback the dispatcher uses
// when no operator-provided router.hcl has been loaded — primarily so
// unit tests that never go through main.go still get the same routing
// semantics they did before the engine landed.
//
// Returns the engine and panics on a programming error in
// DefaultRoutesHCL itself (which is a const, so any failure here is a
// build-time bug, not a runtime configuration problem).
func MustNewDefaultEngine() *Engine {
	cfg, err := DecodeConfig([]byte(DefaultRoutesHCL), "router/defaults.go::DefaultRoutesHCL")
	if err != nil {
		panic("router: DefaultRoutesHCL failed to decode: " + err.Error())
	}
	return NewEngine(cfg, DefaultLegacyKanbanView(), nil)
}
