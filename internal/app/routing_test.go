package app

import (
	"testing"

	"github.com/lonegunmanb/trello-copilot/internal/app/kanban"
)

func TestRoute(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		want      RouteAction
		wantCard  string
		wantList  string
		wantReason string
	}{
		{
			name:     "no card id is dropped",
			body:     `{"action":{"type":"updateCard","data":{}}}`,
			want:     RouteDrop,
			wantReason: "no_card_id",
		},
		{
			name:     "updateCard without list move dropped",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"card1"}}}}`,
			want:     RouteDrop,
			wantCard: "card1",
			wantReason: "updateCard_no_list_move",
		},
		{
			name:     "updateCard moved to Analyze dispatches",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"card1"},"listAfter":{"name":"Analyze"}}}}`,
			want:     RouteDispatch,
			wantCard: "card1",
			wantList: "Analyze",
			wantReason: "moved_to_active_list",
		},
		{
			name:     "updateCard moved to In action dispatches",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"card2"},"listAfter":{"name":"In action"}}}}`,
			want:     RouteDispatch,
			wantCard: "card2",
			wantList: "In action",
			wantReason: "moved_to_active_list",
		},
		{
			name:     "updateCard moved to Done is terminate",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"card3"},"listAfter":{"name":"Done"}}}}`,
			want:     RouteTerminate,
			wantCard: "card3",
			wantList: "Done",
			wantReason: "moved_to_done",
		},
		{
			name:     "updateCard moved to other list is notify-departure",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"card4"},"listAfter":{"name":"Ready for review"}}}}`,
			want:     RouteNotifyDeparture,
			wantCard: "card4",
			wantList: "Ready for review",
			wantReason: "moved_to_non_active_list",
		},
		{
			name:     "createCard in active list dispatches",
			body:     `{"action":{"type":"createCard","data":{"card":{"id":"card5"},"list":{"name":"Analyze"}}}}`,
			want:     RouteDispatch,
			wantCard: "card5",
			wantList: "Analyze",
			wantReason: "created_in_active_list",
		},
		{
			name:     "createCard in non-active list dropped",
			body:     `{"action":{"type":"createCard","data":{"card":{"id":"card6"},"list":{"name":"Need Attention"}}}}`,
			want:     RouteDrop,
			wantCard: "card6",
			wantList: "Need Attention",
			wantReason: "created_in_non_active_list",
		},
		{
			name:     "agent self comment dropped",
			body:     `{"action":{"type":"commentCard","data":{"card":{"id":"card7"},"text":"[agent]: status update"}}}`,
			want:     RouteDrop,
			wantCard: "card7",
			wantReason: "agent_self_comment",
		},
		{
			name:     "human comment dispatched",
			body:     `{"action":{"type":"commentCard","data":{"card":{"id":"card8"},"text":"please retry"}}}`,
			want:     RouteDispatch,
			wantCard: "card8",
			wantReason: "human_comment",
		},
		{
			name:     "deleteCard is terminate",
			body:     `{"action":{"type":"deleteCard","data":{"card":{"id":"card9"}}}}`,
			want:     RouteTerminate,
			wantCard: "card9",
			wantReason: "card_deleted",
		},
		{
			name:     "deleteComment is dropped",
			body:     `{"action":{"type":"deleteComment","data":{"card":{"id":"card10"}}}}`,
			want:     RouteDrop,
			wantCard: "card10",
			wantReason: "deleteComment_not_handled",
		},
		{
			name:     "unknown action type dropped",
			body:     `{"action":{"type":"addLabelToCard","data":{"card":{"id":"card11"}}}}`,
			want:     RouteDrop,
			wantCard: "card11",
			wantReason: "unsupported_action_type",
		},
		{
			name:     "path traversal card id dropped",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"../../etc/passwd"},"listAfter":{"name":"Analyze"}}}}`,
			want:     RouteDrop,
			wantCard: "",
			wantReason: "invalid_card_id",
		},
		{
			name:     "windows absolute card id dropped",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"C:\\Windows\\System32"},"listAfter":{"name":"Analyze"}}}}`,
			want:     RouteDrop,
			wantCard: "",
			wantReason: "invalid_card_id",
		},
		{
			name:     "card id with slash dropped",
			body:     `{"action":{"type":"commentCard","data":{"card":{"id":"abc/def"},"text":"hi"}}}`,
			want:     RouteDrop,
			wantCard: "",
			wantReason: "invalid_card_id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Route([]byte(tc.body), nil)
			if got.Action != tc.want {
				t.Errorf("Action = %s, want %s", got.Action, tc.want)
			}
			if got.CardID != tc.wantCard {
				t.Errorf("CardID = %q, want %q", got.CardID, tc.wantCard)
			}
			if got.ListAfter != tc.wantList {
				t.Errorf("ListAfter = %q, want %q", got.ListAfter, tc.wantList)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// TestRouteWithKanbanView pins the issue #5 behaviour: Route consults
// the resolved kanban view (id-first, name-fallback) instead of the
// legacy hard-coded list names, and lists not claimed by any role
// collapse to category=wait (notify_departure) rather than being
// silently dropped.
func TestRouteWithKanbanView(t *testing.T) {
	view := &kanban.Resolved{
		BoardID:       "B1",
		Plan:          kanban.Role{Name: "Analyze", ID: "L_PLAN"},
		Action:        kanban.Role{Name: "In action", ID: "L_ACTION"},
		Done:          kanban.Role{Name: "Done", ID: "L_DONE"},
		Wait: kanban.WaitRoles{
			PlanReview:   kanban.Role{Name: "Ready for plan review", ID: "L_RPR"},
			ActionReview: kanban.Role{Name: "Ready for review", ID: "L_RR"},
			Generic:      kanban.Role{Name: "Pending PR", ID: "L_PPR"},
			Exception:    kanban.Role{Name: "Need Attention", ID: "L_NA"},
		},
		AgentCommentPrefixes: []string{"[agent]:", "[bot]:"},
		PlanListIDs:          []string{"L_PLAN"},
		ActionListIDs:        []string{"L_ACTION"},
		DoneListIDs:          []string{"L_DONE"},
		WaitListIDs:          []string{"L_RPR", "L_RR", "L_PPR", "L_NA", "L_INBOX"},
		UnclaimedListNames:   []string{"Inbox"},
	}

	cases := []struct {
		name       string
		body       string
		want       RouteAction
		wantReason string
	}{
		{
			name:       "id-based match dispatches to action category",
			body:       `{"action":{"type":"updateCard","data":{"card":{"id":"card01"},"listAfter":{"id":"L_ACTION","name":"In action"}}}}`,
			want:       RouteDispatch,
			wantReason: "moved_to_active_list",
		},
		{
			name:       "id-based match terminates for done",
			body:       `{"action":{"type":"updateCard","data":{"card":{"id":"card02"},"listAfter":{"id":"L_DONE","name":"Done"}}}}`,
			want:       RouteTerminate,
			wantReason: "moved_to_done",
		},
		{
			name:       "unclaimed list collapses to wait (notify_departure)",
			body:       `{"action":{"type":"updateCard","data":{"card":{"id":"card03"},"listAfter":{"id":"L_INBOX","name":"Inbox"}}}}`,
			want:       RouteNotifyDeparture,
			wantReason: "moved_to_non_active_list",
		},
		{
			name:       "name-only fallback still works for legacy payloads",
			body:       `{"action":{"type":"updateCard","data":{"card":{"id":"card04"},"listAfter":{"name":"In action"}}}}`,
			want:       RouteDispatch,
			wantReason: "moved_to_active_list",
		},
		{
			name:       "createCard in plan dispatches",
			body:       `{"action":{"type":"createCard","data":{"card":{"id":"card05"},"list":{"id":"L_PLAN","name":"Analyze"}}}}`,
			want:       RouteDispatch,
			wantReason: "created_in_active_list",
		},
		{
			name:       "createCard in wait list dropped",
			body:       `{"action":{"type":"createCard","data":{"card":{"id":"card06"},"list":{"id":"L_NA","name":"Need Attention"}}}}`,
			want:       RouteDrop,
			wantReason: "created_in_non_active_list",
		},
		{
			name:       "agent self-comment dropped via configured prefix list",
			body:       `{"action":{"type":"commentCard","data":{"card":{"id":"card07"},"text":"[bot]: heartbeat"}}}`,
			want:       RouteDrop,
			wantReason: "agent_self_comment",
		},
		{
			name:       "human comment still dispatches",
			body:       `{"action":{"type":"commentCard","data":{"card":{"id":"card08"},"text":"please retry"}}}`,
			want:       RouteDispatch,
			wantReason: "human_comment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Route([]byte(tc.body), view)
			if got.Action != tc.want {
				t.Errorf("Action = %s, want %s", got.Action, tc.want)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}
