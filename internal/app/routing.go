package app

// RouteAction is the outcome of inspecting a Trello webhook event.
// Historically owned by Route() in this file; today the dispatcher
// hands the raw payload to the HCL `route {}` engine
// (internal/app/router) and translates the engine's string `do`
// token (drop / dispatch / terminate / notify_departure) back into
// this enum so the rest of the gateway keeps the same switch surface
// it had before the engine landed.
type RouteAction int

const (
	// RouteDrop means the event must be ignored (no card id, invalid
	// card id, agent self-comment, ...).
	RouteDrop RouteAction = iota
	// RouteDispatch means the event should be handed to the per-card
	// worker (spawning one if none exists).
	RouteDispatch
	// RouteTerminate means the card is leaving the active flow (moved
	// to Done or deleted): notify the worker if any so it can clean up
	// and self-exit, or perform a no-worker cleanup directly.
	RouteTerminate
	// RouteNotifyDeparture means the card moved away from the active
	// lists but is not terminal. If a worker exists it must be told to
	// wind down its in-flight experiments; if there is no worker we
	// drop the event.
	RouteNotifyDeparture
)

func (a RouteAction) String() string {
	switch a {
	case RouteDrop:
		return "drop"
	case RouteDispatch:
		return "dispatch"
	case RouteTerminate:
		return "terminate"
	case RouteNotifyDeparture:
		return "notify_departure"
	default:
		return "unknown"
	}
}

// RouteDecision is the structured outcome of Dispatcher.evaluateRoute.
// CardID is populated for events the engine accepted as well-formed;
// drops on `no_card_id` / `invalid_card_id` leave it empty so the
// gateway log line is unambiguous about the reason. Reason is the
// `route.reason` string from the matched HCL block (or
// "router_no_route_matched" when the engine fell through).
type RouteDecision struct {
	Action    RouteAction
	CardID    string
	ListAfter string // populated for updateCard moves
	Reason    string
}
