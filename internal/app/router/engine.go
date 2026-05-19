package router

import (
	"log"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/hclfuncs"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"

	"github.com/lonegunmanb/jjc/internal/app/kanban"
)

// Event carries the per-webhook variables visible to a route's `when`
// expression. The field names match the dotted identifiers documented
// in examples/router/router.hcl §2 (action.type, action.card_id, ...).
//
// All fields are normalised by the caller: ListAfter / ListName /
// Comment / Type are passed through as-is, the engine lowercases what
// it exposes through `lower(...)` only when the expression asks for it
// (mirroring the example HCL).
type Event struct {
	Type        string // action.type
	CardID      string // action.card_id
	CardIDValid bool   // action.card_id_valid (false when the id failed the safety check)
	ListAfter   string // action.list_after
	ListAfterID string // action.list_after_id
	ListName    string // action.list_name
	ListID      string // action.list_id
	Comment     string // action.comment
}

// Decision is the structured outcome of Engine.Evaluate. Route is the
// label of the rule that matched (empty when no rule matched and the
// engine fell through to its default-drop path).
type Decision struct {
	Route  string
	Do     string
	Reason string
}

// Engine evaluates the configured `route {}` blocks against incoming
// events. It is safe for concurrent use after construction (the
// underlying *kanban.Resolved and the route slice are read-only at
// dispatch time).
type Engine struct {
	routes []Route
	view   *kanban.Resolved
	logger *log.Logger
	funcs  map[string]function.Function
	kanban cty.Value // pre-built kanban.* object reused on every Evaluate
}

// NewEngine builds an Engine from the decoded Config and the resolved
// kanban view. The view may be nil, in which case the `kanban.*`
// object exposes empty lists and empty role names; routes that depend
// on those values will simply not match. logger may be nil; the engine
// then defaults to log.Default() for the structured failure-mode log
// lines documented on the package doc.
func NewEngine(cfg Config, view *kanban.Resolved, logger *log.Logger) *Engine {
	if logger == nil {
		logger = log.Default()
	}
	e := &Engine{
		routes: append([]Route(nil), cfg.Routes...),
		view:   view,
		logger: logger,
		funcs:  hclfuncs.Functions(""),
	}
	e.kanban = buildKanbanValue(view)
	return e
}

// Evaluate walks the configured routes top-down, returning the first
// rule whose `when` expression yields true. Rules whose `when` errors
// at evaluation time are skipped with a structured log line and the
// walk continues. When no rule matches (the user removed the
// catch-all) the engine logs and returns a synthetic drop decision so
// the dispatcher always has something to act on.
func (e *Engine) Evaluate(ev Event) Decision {
	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"action": buildActionValue(ev),
			"kanban": e.kanban,
		},
		Functions: e.funcs,
	}

	for _, r := range e.routes {
		v, diags := r.When.Value(ctx)
		if diags.HasErrors() {
			// Skip-and-continue per the issue's failure-mode decision.
			// The dispatch must not abort because one rule has a typo.
			e.logger.Printf("event=route_when_eval_error rule=%q diag=%q",
				r.Name, diags.Error())
			continue
		}
		if v.IsNull() || !v.Type().Equals(cty.Bool) {
			// A non-bool `when` is treated like an eval error; skip.
			e.logger.Printf("event=route_when_eval_error rule=%q diag=%q",
				r.Name, "when expression did not evaluate to bool")
			continue
		}
		if v.True() {
			return Decision{Route: r.Name, Do: r.Do, Reason: r.Reason}
		}
	}

	e.logger.Printf("event=router_no_route_matched action_type=%q card_id=%q",
		ev.Type, ev.CardID)
	return Decision{Do: ActionDrop, Reason: "router_no_route_matched"}
}

// buildActionValue produces the `action` cty object the `when`
// expressions reference. Every documented attribute is always present
// so an expression like `action.list_after == ""` never raises an
// "unsupported attribute" diagnostic on payloads where that field
// happens to be missing.
func buildActionValue(ev Event) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"type":          cty.StringVal(ev.Type),
		"card_id":       cty.StringVal(ev.CardID),
		"card_id_valid": cty.BoolVal(ev.CardIDValid),
		"list_after":    cty.StringVal(ev.ListAfter),
		"list_after_id": cty.StringVal(ev.ListAfterID),
		"list_name":     cty.StringVal(ev.ListName),
		"list_id":       cty.StringVal(ev.ListID),
		"comment":       cty.StringVal(ev.Comment),
	})
}

// buildKanbanValue mirrors the "engine-derived views over kanban {}"
// section of examples/router/router.hcl. Every list is lowercased so
// expressions of the form `contains(kanban.action_lists, lower(...))`
// work without further normalisation.
func buildKanbanValue(view *kanban.Resolved) cty.Value {
	planNames := []string{}
	actionNames := []string{}
	waitNames := []string{}
	doneNames := []string{}
	planIDs := []string{}
	actionIDs := []string{}
	waitIDs := []string{}
	doneIDs := []string{}
	prefixes := []string{}

	plan := cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("")})
	action := cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("")})
	done := cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("")})
	waitPR := cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("")})
	waitAR := cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("")})
	waitGen := cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("")})
	waitExc := cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("")})

	if view != nil {
		planNames = append(planNames, normaliseListName(view.Plan.Name))
		actionNames = append(actionNames, normaliseListName(view.Action.Name))
		doneNames = append(doneNames, normaliseListName(view.Done.Name))
		waitNames = append(waitNames,
			normaliseListName(view.Wait.PlanReview.Name),
			normaliseListName(view.Wait.ActionReview.Name),
			normaliseListName(view.Wait.Generic.Name),
			normaliseListName(view.Wait.Exception.Name),
		)
		for _, n := range view.UnclaimedListNames {
			waitNames = append(waitNames, normaliseListName(n))
		}
		planIDs = append(planIDs, view.PlanListIDs...)
		actionIDs = append(actionIDs, view.ActionListIDs...)
		waitIDs = append(waitIDs, view.WaitListIDs...)
		doneIDs = append(doneIDs, view.DoneListIDs...)
		prefixes = append(prefixes, view.AgentCommentPrefixes...)
		plan = cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal(view.Plan.Name)})
		action = cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal(view.Action.Name)})
		done = cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal(view.Done.Name)})
		waitPR = cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal(view.Wait.PlanReview.Name)})
		waitAR = cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal(view.Wait.ActionReview.Name)})
		waitGen = cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal(view.Wait.Generic.Name)})
		waitExc = cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal(view.Wait.Exception.Name)})
	}

	return cty.ObjectVal(map[string]cty.Value{
		"plan_lists":             stringList(planNames),
		"action_lists":           stringList(actionNames),
		"wait_lists":             stringList(waitNames),
		"done_lists":             stringList(doneNames),
		"plan_list_ids":          stringList(planIDs),
		"action_list_ids":        stringList(actionIDs),
		"wait_list_ids":          stringList(waitIDs),
		"done_list_ids":          stringList(doneIDs),
		"agent_comment_prefixes": stringList(prefixes),
		"plan":                   plan,
		"action":                 action,
		"done":                   done,
		"wait": cty.ObjectVal(map[string]cty.Value{
			"plan_review":   waitPR,
			"action_review": waitAR,
			"generic":       waitGen,
			"exception":     waitExc,
		}),
	})
}

// normaliseListName mirrors kanban.normaliseName so the
// `contains(kanban.action_lists, lower(...))` idiom in router.hcl
// hits on the same trim+lower spelling the kanban resolver used at
// startup.
func normaliseListName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func stringList(xs []string) cty.Value {
	if len(xs) == 0 {
		// cty.ListVal panics on empty; use ListValEmpty so
		// contains(kanban.plan_lists, x) still type-checks when no
		// view is configured.
		return cty.ListValEmpty(cty.String)
	}
	vs := make([]cty.Value, len(xs))
	for i, s := range xs {
		vs[i] = cty.StringVal(s)
	}
	return cty.ListVal(vs)
}
