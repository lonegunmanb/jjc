package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestBuildMessageMoveCard(t *testing.T) {
	raw := mustJSON(t, map[string]any{
		"action": map[string]any{
			"type": "updateCard",
			"data": map[string]any{
				"card":       map[string]any{"name": "Fix bug"},
				"listBefore": map[string]any{"name": "Ready for review"},
				"listAfter":  map[string]any{"name": "Approved for action"},
			},
			"memberCreator": map[string]any{"fullName": "Roger"},
		},
	})

	got := BuildMessage(raw)
	want := `Trello: card "Fix bug" moved from "Ready for review" to "Approved for action" (by Roger)`
	if got != want {
		t.Fatalf("unexpected message:\nwant: %s\ngot:  %s", want, got)
	}
}

func TestBuildMessageCommentCard(t *testing.T) {
	raw := mustJSON(t, map[string]any{
		"action": map[string]any{
			"type": "commentCard",
			"data": map[string]any{
				"text": "Looks good",
				"card": map[string]any{"name": "Implement feature"},
			},
			"memberCreator": map[string]any{"fullName": "Alice"},
		},
	})

	got := BuildMessage(raw)
	want := `Trello: Alice commented on card "Implement feature": Looks good`
	if got != want {
		t.Fatalf("unexpected message:\nwant: %s\ngot:  %s", want, got)
	}
}

func TestBuildLogSummaryUsesEnglishNarrative(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "move card",
			raw: mustJSON(t, map[string]any{
				"action": map[string]any{
					"type": "updateCard",
					"data": map[string]any{
						"card":       map[string]any{"name": "Fix bug"},
						"listBefore": map[string]any{"name": "Ready for review"},
						"listAfter":  map[string]any{"name": "Approved for action"},
					},
					"memberCreator": map[string]any{"fullName": "Roger"},
				},
			}),
			want: `Trello: card "Fix bug" moved from "Ready for review" to "Approved for action" (by Roger)`,
		},
		{
			name: "comment card",
			raw: mustJSON(t, map[string]any{
				"action": map[string]any{
					"type": "commentCard",
					"data": map[string]any{
						"text": "Looks good",
						"card": map[string]any{"name": "Implement feature"},
					},
					"memberCreator": map[string]any{"fullName": "Alice"},
				},
			}),
			want: `Trello: Alice commented on card "Implement feature": Looks good`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildLogSummary(tt.raw)
			if got != tt.want {
				t.Fatalf("unexpected log summary:\nwant: %s\ngot:  %s", tt.want, got)
			}
		})
	}
}

func TestBuildPromptSummaryUsesEnglishNarrative(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			name: "move card",
			raw: mustJSON(t, map[string]any{
				"action": map[string]any{
					"type": "updateCard",
					"data": map[string]any{
						"card":       map[string]any{"name": "Fix bug"},
						"listBefore": map[string]any{"name": "Ready for review"},
						"listAfter":  map[string]any{"name": "Approved for action"},
					},
					"memberCreator": map[string]any{"fullName": "Roger"},
				},
			}),
			want: `Trello: card "Fix bug" moved from "Ready for review" to "Approved for action" (by Roger)`,
		},
		{
			name: "comment card",
			raw: mustJSON(t, map[string]any{
				"action": map[string]any{
					"type": "commentCard",
					"data": map[string]any{
						"text": "Looks good",
						"card": map[string]any{"name": "Implement feature"},
					},
					"memberCreator": map[string]any{"fullName": "Alice"},
				},
			}),
			want: `Trello: Alice commented on card "Implement feature": Looks good`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildPromptSummary(tt.raw)
			if got != tt.want {
				t.Fatalf("unexpected prompt summary:\nwant: %s\ngot:  %s", tt.want, got)
			}
		})
	}
}

func TestBuildMessageFallbackIncludesRawJSON(t *testing.T) {
	raw := mustJSON(t, map[string]any{
		"action": map[string]any{
			"type": "createCard",
			"data": map[string]any{
				"card":  map[string]any{"name": "New card"},
				"board": map[string]any{"name": "Main Board"},
			},
			"memberCreator": map[string]any{"fullName": "Bob"},
		},
	})

	got := BuildMessage(raw)
	if !strings.Contains(got, `Trello: createCard on card "New card" in board "Main Board" by Bob`) {
		t.Fatalf("fallback summary missing: %s", got)
	}
	if !strings.Contains(got, `"type":"createCard"`) {
		t.Fatalf("raw json missing: %s", got)
	}
}

func TestBuildMessageHandlesMissingFields(t *testing.T) {
	raw := []byte(`{"action":{"type":"updateCard"}}`)
	got := BuildMessage(raw)
	if got == "" {
		t.Fatal("message should not be empty")
	}
}

// TestBuildPromptSummaryNeverEmbedsRawJSON guards against re-introducing
// the prompt-injection surface that BuildLogSummary intentionally
// embeds in its raw= fallback. BuildPromptSummary is the only flavour
// safe to splice into a worker prompt; it must never include raw user
// content beyond the named fields (cardName / list names / comment
// text). Adding `raw=...` back to the prompt path defeats slim.go.
func TestBuildPromptSummaryNeverEmbedsRawJSON(t *testing.T) {
	// A payload with a custom action type the named branches do not
	// recognise and a clearly attacker-shaped marker that should not
	// survive into the summary.
	const marker = "PROMPT_INJECTION_MARKER_42"
	raw := mustJSON(t, map[string]any{
		"action": map[string]any{
			"type": "addAttachmentToCard", // not a recognised branch
			"data": map[string]any{
				"card":  map[string]any{"name": "card", "extra": marker},
				"board": map[string]any{"name": "board"},
			},
			"memberCreator": map[string]any{"fullName": "user"},
		},
	})
	got := BuildPromptSummary(raw)
	if strings.Contains(got, marker) {
		t.Fatalf("BuildPromptSummary leaked raw payload (%q in output): %s", marker, got)
	}
	if strings.Contains(got, "raw=") {
		t.Fatalf("BuildPromptSummary still embeds raw= clause: %s", got)
	}
	// And it should still report the named fields (so the test is
	// asserting "minus raw", not "completely empty").
	for _, want := range []string{"addAttachmentToCard", "card", "board", "user"} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildPromptSummary missing field %q: %s", want, got)
		}
	}
}

// TestBuildLogSummaryStillEmbedsRawJSON pins the contract that
// BuildLogSummary (display-only) DOES embed the raw payload, so the
// log path is unchanged by the prompt-side hardening above. Reviewers
// modifying BuildLogSummary should think twice if this test breaks.
func TestBuildLogSummaryStillEmbedsRawJSON(t *testing.T) {
	const marker = "LOG_RAW_MARKER_99"
	raw := mustJSON(t, map[string]any{
		"action": map[string]any{
			"type": "addAttachmentToCard",
			"data": map[string]any{
				"card":  map[string]any{"name": "c", "extra": marker},
				"board": map[string]any{"name": "b"},
			},
			"memberCreator": map[string]any{"fullName": "u"},
		},
	})
	got := BuildLogSummary(raw)
	if !strings.Contains(got, marker) {
		t.Fatalf("BuildLogSummary should still embed raw payload for operator log readability; got: %s", got)
	}
}
