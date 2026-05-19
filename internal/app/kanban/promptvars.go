package kanban

import "strings"

// PromptVarPrefix is the namespace under which every kanban template
// variable lives. The prompttmpl renderer treats any `{{...}}`
// reference whose trimmed body starts with this prefix as a kanban
// variable (validated against the map PromptVars returns); anything
// else is resolved as a cross-playbook basename reference.
//
// Keep in sync with docs/playbook-template-variables.md §2.
const PromptVarPrefix = "kanban."

// PromptVarKey enumerates the well-known template variable keys the
// renderer can substitute. They are public constants so callers (and
// tests) can refer to them by name rather than by string-literal,
// avoiding the "fat-finger typo silently breaks rendering" failure
// mode the spec was written against.
const (
	PromptVarBoardID = "kanban.board.id"

	PromptVarPlanID   = "kanban.plan.id"
	PromptVarPlanName = "kanban.plan.name"

	PromptVarActionID   = "kanban.action.id"
	PromptVarActionName = "kanban.action.name"

	PromptVarDoneID   = "kanban.done.id"
	PromptVarDoneName = "kanban.done.name"

	PromptVarWaitPlanReviewID   = "kanban.wait.plan_review.id"
	PromptVarWaitPlanReviewName = "kanban.wait.plan_review.name"

	PromptVarWaitActionReviewID   = "kanban.wait.action_review.id"
	PromptVarWaitActionReviewName = "kanban.wait.action_review.name"

	PromptVarWaitGenericID   = "kanban.wait.generic.id"
	PromptVarWaitGenericName = "kanban.wait.generic.name"

	PromptVarWaitExceptionID   = "kanban.wait.exception.id"
	PromptVarWaitExceptionName = "kanban.wait.exception.name"

	PromptVarPlanListIDs   = "kanban.plan.list_ids"
	PromptVarActionListIDs = "kanban.action.list_ids"
	PromptVarWaitListIDs   = "kanban.wait.list_ids"
	PromptVarDoneListIDs   = "kanban.done.list_ids"

	PromptVarAgentCommentPrefix   = "kanban.agent_comment_prefix"
	PromptVarAgentCommentPrefixes = "kanban.agent_comment_prefixes"
)

// promptVarJoinSep is the literal separator the spec mandates for the
// category-aggregate keys and for the descriptive "all prefixes" key
// (kanban.agent_comment_prefixes). Comma-and-space chosen so the
// joined string reads naturally inside a sentence and so a future
// strings.Split on ", " round-trips without surprises.
const promptVarJoinSep = ", "

// PromptVars returns the complete template-variable map a Resolved
// view exposes to the playbook renderer. The keys are the constants
// declared above and the values are drawn entirely from r — there is
// no other source of truth.
//
// Returns an empty map (not nil) when r is nil so a caller can pass
// the result to a renderer that is parameterised by an optional
// vars map without nil-checking at every site.
//
// This is the single source of truth for the kanban template
// schema; the renderer derives its accept-list from the keys this
// map contains. docs/playbook-template-variables.md §2 describes
// the contract for playbook authors and must be updated in lockstep
// with this function.
func (r *Resolved) PromptVars() map[string]string {
	if r == nil {
		return map[string]string{}
	}
	return map[string]string{
		PromptVarBoardID: r.BoardID,

		PromptVarPlanID:   r.Plan.ID,
		PromptVarPlanName: r.Plan.Name,

		PromptVarActionID:   r.Action.ID,
		PromptVarActionName: r.Action.Name,

		PromptVarDoneID:   r.Done.ID,
		PromptVarDoneName: r.Done.Name,

		PromptVarWaitPlanReviewID:   r.Wait.PlanReview.ID,
		PromptVarWaitPlanReviewName: r.Wait.PlanReview.Name,

		PromptVarWaitActionReviewID:   r.Wait.ActionReview.ID,
		PromptVarWaitActionReviewName: r.Wait.ActionReview.Name,

		PromptVarWaitGenericID:   r.Wait.Generic.ID,
		PromptVarWaitGenericName: r.Wait.Generic.Name,

		PromptVarWaitExceptionID:   r.Wait.Exception.ID,
		PromptVarWaitExceptionName: r.Wait.Exception.Name,

		PromptVarPlanListIDs:   strings.Join(r.PlanListIDs, promptVarJoinSep),
		PromptVarActionListIDs: strings.Join(r.ActionListIDs, promptVarJoinSep),
		PromptVarWaitListIDs:   strings.Join(r.WaitListIDs, promptVarJoinSep),
		PromptVarDoneListIDs:   strings.Join(r.DoneListIDs, promptVarJoinSep),

		PromptVarAgentCommentPrefix:   r.ActiveAgentCommentPrefix(),
		PromptVarAgentCommentPrefixes: strings.Join(r.AgentCommentPrefixes, promptVarJoinSep),
	}
}

// ActiveAgentCommentPrefix returns the prefix every worker comment
// must start with so the gateway can recognise the comment as
// agent-authored and break the self-comment feedback loop.
//
// Per docs/playbook-template-variables.md §2.4 (the spec calls this
// value the "first entry" / "active prefix"), the value is the first
// entry of r.AgentCommentPrefixes; for defence against a malformed
// router.hcl that accidentally declared an empty string at index 0,
// the implementation walks forward to the first non-empty entry.
// Returns "" when r is nil or no usable prefix was configured (a
// state the route engine treats as "no prefix", matching the existing
// IsAgentComment behaviour).
//
// Important: there is deliberately no hard-coded fallback such as
// "[agent]:". The whole point of agent_comment_prefixes living in
// router.hcl is so operators can change it; a hard-coded fallback
// would paper over a configuration bug instead of surfacing it.
//
// This is the single source of truth for the active prefix on the
// Go side. The kanban template variable kanban.agent_comment_prefix
// resolves to the same value via PromptVars, so playbook authors and
// Go-side callers cannot disagree about which prefix is in effect.
func (r *Resolved) ActiveAgentCommentPrefix() string {
	if r == nil {
		return ""
	}
	return firstNonEmpty(r.AgentCommentPrefixes)
}

// firstNonEmpty returns the first non-empty entry of xs, or "" when
// the slice is empty or contains only empty entries. Used to expose
// the "active" agent comment prefix as a scalar: the spec mandates
// the first entry, but defends against a malformed router.hcl that
// accidentally declared an empty string at index 0.
func firstNonEmpty(xs []string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
