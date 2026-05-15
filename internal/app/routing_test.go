package app

import "testing"

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
			got := Route([]byte(tc.body))
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
