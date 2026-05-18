package app

import (
	"strings"

	"github.com/lonegunmanb/trello-copilot/internal/app/kanban"
)

// RouteAction is the outcome of inspecting a Trello webhook event
// against the routing rules described by MANAGER.md §3 and now by the
// resolved kanban view (internal/app/kanban). The semantics each value
// carries are unchanged from earlier revisions; only the source of the
// list-name → category mapping moved out of this file.
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

// RouteDecision is the structured outcome of Route. CardID is always
// set when the underlying event referenced a card; Reason is a short
// human-readable tag suitable for logging.
type RouteDecision struct {
	Action    RouteAction
	CardID    string
	ListAfter string // populated for updateCard moves
	Reason    string
}

// Route classifies a Trello webhook payload according to MANAGER.md §3
// using the resolved kanban view. It does NOT consult any per-card
// worker registry — that decision happens later in the dispatcher.
//
// rawBody must be the unmodified JSON Trello sent (slimRawBody strips
// fields we still need here, like action.type, listAfter.id and
// listAfter.name).
//
// view may be nil; that path exists strictly so unit tests targeting
// the JSON-parsing / card-id-validation half of Route can construct a
// router without standing up a Trello board. With nil view all
// category-based decisions fall back to the legacy hard-coded names
// (Analyze / In action / Done / agent prefix `[agent]:`) so the
// historical Route(rawBody) test corpus continues to behave
// identically.
func Route(rawBody []byte, view *kanban.Resolved) RouteDecision {
	root := parseRawBody(rawBody)
	action := asMap(root["action"])
	actionType := asString(action["type"])
	data := asMap(action["data"])
	card := asMap(data["card"])
	cardID := asString(card["id"])

	if cardID == "" {
		return RouteDecision{Action: RouteDrop, Reason: "no_card_id"}
	}
	// Defence in depth: a malformed cardID will eventually reach
	// filepath.Join(baseDir, cardID) in WorkDirPreparer.Prepare. Reject
	// it here so the dispatcher never even spawns a worker for an
	// untrusted id. The webhook is internet-exposed; cardID comes
	// straight from the Trello payload (which a forged callback could
	// supply).
	if !IsValidCardID(cardID) {
		return RouteDecision{Action: RouteDrop, Reason: "invalid_card_id"}
	}

	switch actionType {
	case "updateCard":
		// updateCard with a listAfter.name is a list move. updateCard
		// without one is just a card-attribute change (rename, position
		// shuffle within the same list, etc.) — those don't trigger
		// routing on their own.
		listAfterMap := asMap(data["listAfter"])
		listAfter := asString(listAfterMap["name"])
		listAfterID := asString(listAfterMap["id"])
		if listAfter == "" && listAfterID == "" {
			return RouteDecision{Action: RouteDrop, CardID: cardID, Reason: "updateCard_no_list_move"}
		}

		dec := RouteDecision{CardID: cardID, ListAfter: listAfter}
		cat := categoryFor(view, listAfterID, listAfter)
		switch cat {
		case kanban.CategoryDone:
			dec.Action = RouteTerminate
			dec.Reason = "moved_to_done"
		case kanban.CategoryPlan, kanban.CategoryAction:
			dec.Action = RouteDispatch
			dec.Reason = "moved_to_active_list"
		case kanban.CategoryWait:
			// Per the issue: any wait sub-role AND any unclaimed list
			// collapse to notify_departure (worker winds down). No more
			// "moved_to_unknown_list" drop.
			dec.Action = RouteNotifyDeparture
			dec.Reason = "moved_to_non_active_list"
		default:
			// view==nil only path (tests): fall back to legacy
			// non-active behaviour.
			dec.Action = RouteNotifyDeparture
			dec.Reason = "moved_to_non_active_list"
		}
		return dec

	case "createCard":
		// Rule 2C: only dispatch when the card was created in a plan
		// or action list. Trello's createCard payload puts the
		// destination list in data.list (not listAfter).
		listMap := asMap(data["list"])
		listName := asString(listMap["name"])
		listID := asString(listMap["id"])
		cat := categoryFor(view, listID, listName)
		if cat == kanban.CategoryPlan || cat == kanban.CategoryAction {
			return RouteDecision{
				Action:    RouteDispatch,
				CardID:    cardID,
				ListAfter: listName,
				Reason:    "created_in_active_list",
			}
		}
		return RouteDecision{
			Action:    RouteDrop,
			CardID:    cardID,
			ListAfter: listName,
			Reason:    "created_in_non_active_list",
		}

	case "commentCard":
		text := asString(data["text"])
		if isAgentComment(view, text) {
			return RouteDecision{Action: RouteDrop, CardID: cardID, Reason: "agent_self_comment"}
		}
		return RouteDecision{Action: RouteDispatch, CardID: cardID, Reason: "human_comment"}

	case "deleteCard":
		return RouteDecision{Action: RouteTerminate, CardID: cardID, Reason: "card_deleted"}

	case "deleteComment":
		// Rule 2D: not handled.
		return RouteDecision{Action: RouteDrop, CardID: cardID, Reason: "deleteComment_not_handled"}

	default:
		return RouteDecision{Action: RouteDrop, CardID: cardID, Reason: "unsupported_action_type"}
	}
}

// categoryFor consults the resolved kanban view, preferring an ID
// match (stable across Trello renames) and falling back to a
// case-insensitive name match. When view is nil it falls back to the
// legacy hard-coded names so historic tests keep passing.
func categoryFor(view *kanban.Resolved, listID, listName string) kanban.Category {
	if view != nil {
		if cat := view.CategoryForListID(listID); cat != kanban.CategoryUnknown {
			return cat
		}
		if cat := view.CategoryForListName(listName); cat != kanban.CategoryUnknown {
			return cat
		}
		return kanban.CategoryUnknown
	}
	// Fallback path: no resolved view, use the legacy hard-coded names
	// so unit tests that build a Route call without a Trello board
	// continue to observe the prior behaviour.
	switch strings.ToLower(strings.TrimSpace(listName)) {
	case "analyze", "in action":
		return kanban.CategoryAction // both dispatch — Plan/Action are equivalent in routing
	case "done":
		return kanban.CategoryDone
	case "":
		return kanban.CategoryUnknown
	default:
		return kanban.CategoryWait
	}
}

// isAgentComment uses the resolved view's prefix list; when view is
// nil it falls back to the historical hard-coded "[agent]:" prefix so
// the legacy Route(rawBody) test corpus and any caller that hasn't
// wired in a kanban view yet keep behaving identically.
func isAgentComment(view *kanban.Resolved, text string) bool {
	if view != nil {
		return view.IsAgentComment(text)
	}
	return strings.HasPrefix(strings.TrimSpace(text), "[agent]:")
}
