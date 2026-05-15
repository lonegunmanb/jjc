package app

import "strings"

// RouteAction is the outcome of inspecting a Trello webhook event against
// the routing rules from MANAGER.md §3.
type RouteAction int

const (
	// RouteDrop means the event must be ignored (rules 1, 2C, 2D, 4 from
	// MANAGER.md, or events without a card id).
	RouteDrop RouteAction = iota
	// RouteDispatch means the event should be handed to the per-card worker
	// (spawning one if none exists).
	RouteDispatch
	// RouteTerminate means the card is leaving the active flow (moved to
	// Done or deleted): notify the worker if any so it can clean up and
	// self-exit, or perform a no-worker cleanup directly.
	RouteTerminate
	// RouteNotifyDeparture means the card moved away from the active lists
	// (Analyze / In action / Done) but is not terminal. If a worker exists
	// it must be told to wind down its in-flight experiments; if there is
	// no worker we drop the event.
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

// RouteDecision is the structured outcome of Route. CardID is always set
// when the underlying event referenced a card; Reason is a short
// human-readable tag suitable for logging.
type RouteDecision struct {
	Action    RouteAction
	CardID    string
	ListAfter string // populated for updateCard moves
	Reason    string
}

// activeLists is the set of list names that are considered "active" for the
// purpose of routing. Moving INTO any of them dispatches the event to a
// worker; moving OUT of them while a worker exists triggers
// RouteNotifyDeparture.
var activeLists = map[string]struct{}{
	"Analyze":   {},
	"In action": {},
}

// Route classifies a Trello webhook payload according to MANAGER.md §3.
// It does NOT consult any per-card worker registry — that decision happens
// later in the dispatcher. The returned CardID is the one the rule-set
// considers responsible for this event.
//
// rawBody must be the unmodified JSON Trello sent (slimRawBody strips
// fields we still need here, like action.type and listAfter.name).
func Route(rawBody []byte) RouteDecision {
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
		listAfter := asString(asMap(data["listAfter"])["name"])
		if listAfter == "" {
			return RouteDecision{Action: RouteDrop, CardID: cardID, Reason: "updateCard_no_list_move"}
		}

		dec := RouteDecision{CardID: cardID, ListAfter: listAfter}
		switch {
		case strings.EqualFold(listAfter, "Done"):
			dec.Action = RouteTerminate
			dec.Reason = "moved_to_done"
		case isActiveList(listAfter):
			dec.Action = RouteDispatch
			dec.Reason = "moved_to_active_list"
		default:
			// Rule 1 / Rule 6: moved to some non-active, non-terminal
			// list. Worker (if any) should wind down; if no worker, drop.
			dec.Action = RouteNotifyDeparture
			dec.Reason = "moved_to_non_active_list"
		}
		return dec

	case "createCard":
		// Rule 2C: only dispatch when the card was created in an active
		// list. Trello's createCard payload puts the destination list in
		// data.list (not listAfter).
		listName := asString(asMap(data["list"])["name"])
		if isActiveList(listName) {
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
		// Rule 4: ignore comments authored by the agent itself.
		text := asString(data["text"])
		if strings.HasPrefix(strings.TrimSpace(text), "[agent]:") {
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

func isActiveList(name string) bool {
	_, ok := activeLists[name]
	return ok
}
