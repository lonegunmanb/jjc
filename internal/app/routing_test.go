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
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"c1"}}}}`,
			want:     RouteDrop,
			wantCard: "c1",
			wantReason: "updateCard_no_list_move",
		},
		{
			name:     "updateCard moved to Analyze dispatches",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"c1"},"listAfter":{"name":"Analyze"}}}}`,
			want:     RouteDispatch,
			wantCard: "c1",
			wantList: "Analyze",
			wantReason: "moved_to_active_list",
		},
		{
			name:     "updateCard moved to In action dispatches",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"c2"},"listAfter":{"name":"In action"}}}}`,
			want:     RouteDispatch,
			wantCard: "c2",
			wantList: "In action",
			wantReason: "moved_to_active_list",
		},
		{
			name:     "updateCard moved to Done is terminate",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"c3"},"listAfter":{"name":"Done"}}}}`,
			want:     RouteTerminate,
			wantCard: "c3",
			wantList: "Done",
			wantReason: "moved_to_done",
		},
		{
			name:     "updateCard moved to other list is notify-departure",
			body:     `{"action":{"type":"updateCard","data":{"card":{"id":"c4"},"listAfter":{"name":"Ready for review"}}}}`,
			want:     RouteNotifyDeparture,
			wantCard: "c4",
			wantList: "Ready for review",
			wantReason: "moved_to_non_active_list",
		},
		{
			name:     "createCard in active list dispatches",
			body:     `{"action":{"type":"createCard","data":{"card":{"id":"c5"},"list":{"name":"Analyze"}}}}`,
			want:     RouteDispatch,
			wantCard: "c5",
			wantList: "Analyze",
			wantReason: "created_in_active_list",
		},
		{
			name:     "createCard in non-active list dropped",
			body:     `{"action":{"type":"createCard","data":{"card":{"id":"c6"},"list":{"name":"Need Attention"}}}}`,
			want:     RouteDrop,
			wantCard: "c6",
			wantList: "Need Attention",
			wantReason: "created_in_non_active_list",
		},
		{
			name:     "agent self comment dropped",
			body:     `{"action":{"type":"commentCard","data":{"card":{"id":"c7"},"text":"[agent]: status update"}}}`,
			want:     RouteDrop,
			wantCard: "c7",
			wantReason: "agent_self_comment",
		},
		{
			name:     "human comment dispatched",
			body:     `{"action":{"type":"commentCard","data":{"card":{"id":"c8"},"text":"please retry"}}}`,
			want:     RouteDispatch,
			wantCard: "c8",
			wantReason: "human_comment",
		},
		{
			name:     "deleteCard is terminate",
			body:     `{"action":{"type":"deleteCard","data":{"card":{"id":"c9"}}}}`,
			want:     RouteTerminate,
			wantCard: "c9",
			wantReason: "card_deleted",
		},
		{
			name:     "deleteComment is dropped",
			body:     `{"action":{"type":"deleteComment","data":{"card":{"id":"c10"}}}}`,
			want:     RouteDrop,
			wantCard: "c10",
			wantReason: "deleteComment_not_handled",
		},
		{
			name:     "unknown action type dropped",
			body:     `{"action":{"type":"addLabelToCard","data":{"card":{"id":"c11"}}}}`,
			want:     RouteDrop,
			wantCard: "c11",
			wantReason: "unsupported_action_type",
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
