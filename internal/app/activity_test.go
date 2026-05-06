package app

import (
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

func TestActivityTracker_StateMachine(t *testing.T) {
	tr := NewActivityTracker("card-1", 8)
	t.Cleanup(func() { _ = tr.Close() })

	// session start → thinking
	model := "claude"
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.SessionStartData{SessionID: "s1", SelectedModel: &model}})
	st, _ := tr.Snapshot()
	if st.State != StateThinking {
		t.Fatalf("after session_start expected thinking, got %s", st.State)
	}
	if st.SessionID != "s1" || st.Model != "claude" {
		t.Fatalf("unexpected status: %+v", st)
	}

	// tool start → running_tool
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolName: "read_powershell", ToolCallID: "c1", Arguments: map[string]any{"shellId": 173}}})
	st, _ = tr.Snapshot()
	if st.State != StateRunningTool {
		t.Fatalf("expected running_tool, got %s", st.State)
	}
	if st.CurrentTool == "" || st.CurrentToolStartedAt.IsZero() {
		t.Fatalf("CurrentTool / start time not set: %+v", st)
	}

	// tool complete → thinking, currentTool cleared
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "c1", Success: true}})
	st, _ = tr.Snapshot()
	if st.State != StateThinking {
		t.Fatalf("expected thinking, got %s", st.State)
	}
	if st.CurrentTool != "" {
		t.Fatalf("CurrentTool should be cleared, got %q", st.CurrentTool)
	}

	// session idle → idle
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.SessionIdleData{}})
	st, _ = tr.Snapshot()
	if st.State != StateIdle {
		t.Fatalf("expected idle, got %s", st.State)
	}

	// error
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.SessionErrorData{ErrorType: "boom", Message: "exploded"}})
	st, _ = tr.Snapshot()
	if st.State != StateError || st.LastError != "exploded" {
		t.Fatalf("unexpected error state: %+v", st)
	}
}

func TestActivityTracker_AssistantMessageAndUsage(t *testing.T) {
	tr := NewActivityTracker("card-1", 8)
	t.Cleanup(func() { _ = tr.Close() })

	in, out := 100.0, 5.0
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "claude", InputTokens: &in, OutputTokens: &out}})
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.AssistantMessageData{MessageID: "m", Content: "hello world"}})

	st, entries := tr.Snapshot()
	if st.TurnTokensIn != 100 || st.TurnTokensOut != 5 {
		t.Fatalf("token counts wrong: %+v", st)
	}
	if st.LastAssistantPreview != "hello world" {
		t.Fatalf("preview wrong: %q", st.LastAssistantPreview)
	}
	// Empty assistant_message should not be appended; non-empty should.
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.AssistantMessageData{MessageID: "m2", Content: ""}})
	_, entries2 := tr.Snapshot()
	if len(entries2) != len(entries) {
		t.Fatalf("empty assistant_message should not append: before=%d after=%d", len(entries), len(entries2))
	}
}

func TestActivityRing_BoundedAndChronological(t *testing.T) {
	tr := NewActivityTracker("card-1", 3)
	t.Cleanup(func() { _ = tr.Close() })

	for i := 0; i < 10; i++ {
		tr.RecordEvent(copilot.SessionEvent{Data: &copilot.SessionIdleData{}})
	}
	_, entries := tr.Snapshot()
	if len(entries) != 3 {
		t.Fatalf("ring should cap at 3, got %d", len(entries))
	}
	// Entries must be in chronological order: each entry's At >= previous.
	for i := 1; i < len(entries); i++ {
		if entries[i].At.Before(entries[i-1].At) {
			t.Fatalf("entries out of order at %d", i)
		}
	}
}

func TestActivityTracker_MarkDisconnected(t *testing.T) {
	tr := NewActivityTracker("card-1", 8)
	t.Cleanup(func() { _ = tr.Close() })
	tr.MarkDisconnected()
	st, entries := tr.Snapshot()
	if st.State != StateDisconnected {
		t.Fatalf("expected disconnected, got %s", st.State)
	}
	if len(entries) != 1 || entries[0].Kind != "session_shutdown" {
		t.Fatalf("expected one shutdown entry, got %+v", entries)
	}
}

func TestActivityTracker_ToolCompleteIncludesError(t *testing.T) {
	tr := NewActivityTracker("card-1", 8)
	t.Cleanup(func() { _ = tr.Close() })

	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolCallID: "c1", ToolName: "view", Arguments: map[string]any{"path": "/x"}}})
	code := "EACCES"
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{
		ToolCallID: "c1",
		Success:    false,
		Error:      &copilot.ToolExecutionCompleteError{Message: "permission denied", Code: &code},
	}})

	_, entries := tr.Snapshot()
	var done *ActivityEntry
	for i := range entries {
		if entries[i].Kind == "tool_complete" {
			done = &entries[i]
		}
	}
	if done == nil {
		t.Fatalf("no tool_complete entry: %+v", entries)
	}
	if done.Tool != "view" {
		t.Fatalf("expected tool=view, got %q", done.Tool)
	}
	if !strings.Contains(done.Summary, "success=false") ||
		!strings.Contains(done.Summary, "permission denied") ||
		!strings.Contains(done.Summary, "EACCES") {
		t.Fatalf("error not surfaced in summary: %q", done.Summary)
	}
}

func TestActivityTracker_SubagentTagsToolEvents(t *testing.T) {
	tr := NewActivityTracker("card-1", 16)
	t.Cleanup(func() { _ = tr.Close() })

	// Parent dispatches a `task` tool call.
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolCallID: "task-1", ToolName: "task", Arguments: map[string]any{"name": "fs-probe"}}})
	// SDK signals subagent started, parent task call_id = task-1.
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.SubagentStartedData{
		ToolCallID: "task-1", AgentName: "fs-probe", AgentDisplayName: "Filesystem probe",
	}})
	// Subagent issues its own view; ParentToolCallID points back to task-1.
	parent := "task-1"
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{
		ToolCallID: "v1", ToolName: "view", ParentToolCallID: &parent,
		Arguments: map[string]any{"path": "/x"},
	}})
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{
		ToolCallID: "v1", Success: true, ParentToolCallID: &parent,
	}})
	// Subagent finishes.
	dur := 1234.0
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.SubagentCompletedData{
		ToolCallID: "task-1", AgentName: "fs-probe", AgentDisplayName: "Filesystem probe",
		DurationMs: &dur,
	}})

	_, entries := tr.Snapshot()
	var subStart, subDone *ActivityEntry
	var taggedStart, taggedDone *ActivityEntry
	for i := range entries {
		switch entries[i].Kind {
		case "subagent_start":
			subStart = &entries[i]
		case "subagent_complete":
			subDone = &entries[i]
		case "tool_start":
			if entries[i].Tool == "view[sub:fs-probe]" {
				taggedStart = &entries[i]
			}
		case "tool_complete":
			if entries[i].Tool == "view[sub:fs-probe]" {
				taggedDone = &entries[i]
			}
		}
	}
	if subStart == nil || subStart.Tool != "fs-probe" {
		t.Fatalf("missing or mislabelled subagent_start: %+v", entries)
	}
	if subDone == nil || !strings.Contains(subDone.Summary, "duration_ms=1234") {
		t.Fatalf("missing or mislabelled subagent_complete: %+v", subDone)
	}
	if taggedStart == nil {
		t.Fatalf("subagent tool_start was not tagged with [sub:fs-probe]: %+v", entries)
	}
	if taggedDone == nil {
		t.Fatalf("subagent tool_complete was not tagged with [sub:fs-probe]: %+v", entries)
	}

	// Parent-issued tool call (no ParentToolCallID) should NOT carry the tag.
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolCallID: "p1", ToolName: "view", Arguments: map[string]any{"path": "/y"}}})
	tr.RecordEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "p1", Success: true}})
	_, entries = tr.Snapshot()
	for _, e := range entries {
		if (e.Kind == "tool_start" || e.Kind == "tool_complete") && strings.Contains(e.Tool, "[sub:") && e.Tool != "view[sub:fs-probe]" {
			t.Fatalf("parent tool call wrongly tagged: %+v", e)
		}
	}
}

