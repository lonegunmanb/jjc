package app

import (
	"bytes"
	"log"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("missing %q in:\n%s", needle, haystack)
	}
}

func TestLogSessionEventCoversCommonTypes(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	model := "claude-opus-4.6-1m"
	cases := []struct {
		name string
		ev   copilot.SessionEvent
		want string
	}{
		{"start", copilot.SessionEvent{Data: &copilot.SessionStartData{SessionID: "sid", SelectedModel: &model, Producer: "p", CopilotVersion: "1.0"}}, "sub=session_start"},
		{"turn_start", copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{TurnID: "t1"}}, "sub=assistant_turn_start"},
		{"reasoning", copilot.SessionEvent{Data: &copilot.AssistantReasoningData{ReasoningID: "r", Content: "thinking..."}}, "sub=assistant_reasoning"},
		{"assistant", copilot.SessionEvent{Data: &copilot.AssistantMessageData{MessageID: "m", Content: "hello"}}, "sub=assistant_message"},
		{"tool_start", copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolCallID: "tc", ToolName: "view"}}, "sub=tool_start"},
		{"tool_done", copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "tc", Success: true}}, "sub=tool_complete"},
		{"perm_req", copilot.SessionEvent{Data: &copilot.PermissionRequestedData{RequestID: "pr"}}, "sub=permission_requested"},
		{"idle", copilot.SessionEvent{Data: &copilot.SessionIdleData{}}, "sub=session_idle"},
		{"error", copilot.SessionEvent{Data: &copilot.SessionErrorData{ErrorType: "rate_limit", Message: "boom"}}, "sub=session_error"},
	}

	for _, c := range cases {
		buf.Reset()
		logSessionEvent(sysevent.FromLogger(logger), "card-abc", c.ev)
		got := buf.String()
		mustContain(t, got, c.want)
		mustContain(t, got, "card=card-abc")
	}
}

func TestLogSessionEventDropsNoiseTypes(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	for _, ty := range []copilot.SessionEventType{"tool.execution_partial_result", "session.background_tasks_changed"} {
		buf.Reset()
		logSessionEvent(sysevent.FromLogger(logger), "card-abc", copilot.SessionEvent{Type: ty})
		if buf.Len() != 0 {
			t.Fatalf("event type %q should be dropped, got: %s", ty, buf.String())
		}
	}
}

func TestLogSessionEventSkipsEmptyAssistantMessage(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	logSessionEvent(sysevent.FromLogger(logger), "card-abc", copilot.SessionEvent{
		Data: &copilot.AssistantMessageData{MessageID: "m", Content: ""},
	})
	if buf.Len() != 0 {
		t.Fatalf("empty assistant message should be skipped, got: %s", buf.String())
	}
}

func TestPreviewTruncates(t *testing.T) {
	in := strings.Repeat("a", 500) + "\nnewline"
	got := preview(in, 10)
	if got != "aaaaaaaaaa..." {
		t.Fatalf("unexpected preview: %q", got)
	}
	if preview("", 10) != "" {
		t.Fatal("empty input should give empty output")
	}
	if preview("short", 10) != "short" {
		t.Fatal("short input should pass through")
	}
}
