package app

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

// WorkerState is a coarse "what is the worker doing right now?" enum
// derived from the SDK event stream.
type WorkerState string

const (
	StateStarting          WorkerState = "starting"
	StateThinking          WorkerState = "thinking"
	StateRunningTool       WorkerState = "running_tool"
	StateWaitingPermission WorkerState = "waiting_permission"
	StateIdle              WorkerState = "idle"
	StateError             WorkerState = "error"
	StateDisconnected      WorkerState = "disconnected"
)

// ActivityEntry is one normalised event in the per-card ring buffer.
type ActivityEntry struct {
	At      time.Time
	Kind    string // "tool_start" | "tool_complete" | "subagent_start" | "subagent_complete" | "assistant_message" | "user_message" | "idle" | "error" | "permission" | "session_start" | "session_shutdown"
	Tool    string // populated for tool_* kinds
	Summary string // single-line, <=200 chars
}

// WorkerStatus is the high-level snapshot the REPL shows for one card.
type WorkerStatus struct {
	CardID               string
	State                WorkerState
	CurrentTool          string
	CurrentToolStartedAt time.Time
	LastAssistantPreview string
	LastEventAt          time.Time
	LastError            string
	TurnTokensIn         int
	TurnTokensOut        int
	Model                string
	SessionID            string
	InboxDepth           int // populated by Dispatcher.ListCards / Snapshot

	// Classification fields populated once at session creation time via
	// SetClassification. Empty when no GitHub URL was matched on the
	// card's first description line.
	Owner  string
	Repo   string
	Kind   GitHubItemKind
	Number string

	// WorkDir is the local filesystem directory the worker session is
	// anchored to (SDK WorkingDirectory). Populated via SetWorkDir at
	// session creation time.
	WorkDir string

	// LogPath is the absolute path of the per-worker activity log file
	// allocated when the tracker was created. The file is appended to on
	// every recorded event (untruncated) and removed when the worker is
	// deregistered. Empty if the file could not be created.
	LogPath string
}

// ActivityTracker is a thread-safe per-card aggregator. It is fed every
// SessionEvent the worker emits and maintains both a current status, a
// fixed-size ring of recent (truncated) entries for the TUI/REPL display,
// and a per-worker append-only log file capturing the FULL untruncated
// activity history. The log file is allocated in NewActivityTracker and
// removed by Close (called when the worker is deregistered).
type ActivityTracker struct {
	cardID   string
	ringSize int

	mu      sync.RWMutex
	status  WorkerStatus
	entries []ActivityEntry // ring buffer; len <= ringSize
	head    int             // next write index when len(entries) == ringSize
	logFile *os.File        // append-only full activity log; nil if create failed
	logPath string          // absolute path of logFile

	// toolCallNames maps tool_call_id -> tool name, populated on
	// ToolExecutionStartData and consumed (then deleted) on
	// ToolExecutionCompleteData. Lets us label completions even when
	// subagent + parent tool calls interleave.
	toolCallNames map[string]string

	// subagentNames maps the parent task tool_call_id -> subagent
	// AgentName, populated on SubagentStartedData. Used to tag
	// tool_start / tool_complete entries whose ParentToolCallID is
	// non-nil.
	subagentNames map[string]string
}

// NewActivityTracker creates a tracker with a ring buffer of ringSize
// entries (default 64 if <=0). It also allocates a temp file under
// os.TempDir() to which every recorded event is appended in full. The
// caller is responsible for invoking Close on the tracker when the worker
// is deregistered so the file is deleted.
func NewActivityTracker(cardID string, ringSize int) *ActivityTracker {
	if ringSize <= 0 {
		ringSize = 64
	}
	t := &ActivityTracker{
		cardID:   cardID,
		ringSize: ringSize,
		status: WorkerStatus{
			CardID: cardID,
			State:  StateStarting,
		},
		toolCallNames: make(map[string]string),
		subagentNames: make(map[string]string),
	}
	safe := sanitizeForFilename(cardID)
	prefix := fmt.Sprintf("trello-worker-%s-%s-", safe, time.Now().Format("20060102-150405"))
	if f, err := os.CreateTemp("", prefix+"*.log"); err == nil {
		t.logFile = f
		t.logPath = f.Name()
		t.status.LogPath = f.Name()
		fmt.Fprintf(f, "# trello-worker activity log\n# card_id: %s\n# created_at: %s\n",
			cardID, time.Now().Format(time.RFC3339Nano))
	} else {
		log.Printf("event=activity_log_create_error card_id=%s err=%v", cardID, err)
	}
	return t
}

// LogPath returns the absolute path of the per-worker activity log file,
// or "" if the file could not be created. Safe for concurrent use.
func (t *ActivityTracker) LogPath() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.logPath
}

// Close flushes and removes the per-worker activity log file. Idempotent.
// Intended to be called by the dispatcher once the worker is fully
// deregistered. Returns the underlying os.Remove error if any.
func (t *ActivityTracker) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.logFile == nil {
		return nil
	}
	f := t.logFile
	p := t.logPath
	t.logFile = nil
	t.logPath = ""
	t.status.LogPath = ""
	_ = f.Close()
	return os.Remove(p)
}

// RecordEvent updates the status state machine and appends an entry to the
// ring (if the event is interesting enough to keep). Safe to call from the
// SDK callback goroutine.
func (t *ActivityTracker) RecordEvent(e copilot.SessionEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.status.LastEventAt = now

	switch d := e.Data.(type) {

	case *copilot.SessionStartData:
		t.status.SessionID = d.SessionID
		if d.SelectedModel != nil {
			t.status.Model = *d.SelectedModel
		}
		t.status.State = StateThinking
		t.record(now, "session_start", "", "session="+d.SessionID, "session="+d.SessionID)

	case *copilot.SessionShutdownData:
		t.status.State = StateDisconnected
		t.record(now, "session_shutdown", "", string(d.ShutdownType), string(d.ShutdownType))

	case *copilot.SessionIdleData:
		t.status.State = StateIdle
		t.status.CurrentTool = ""
		t.status.CurrentToolStartedAt = time.Time{}
		t.record(now, "idle", "", "", "")

	case *copilot.SessionErrorData:
		t.status.State = StateError
		t.status.LastError = d.Message
		t.record(now, "error", "", d.Message, d.Message)

	case *copilot.UserMessageData:
		// New turn started: reset per-turn counters.
		t.status.TurnTokensIn = 0
		t.status.TurnTokensOut = 0
		t.status.State = StateThinking
		summary := fmt.Sprintf("chars=%d", len(d.Content))
		full := summary
		if len(d.Content) > 0 {
			full = summary + " content=" + d.Content
		}
		t.record(now, "user_message", "", summary, full)

	case *copilot.AssistantMessageData:
		if len(d.Content) > 0 {
			t.status.LastAssistantPreview = preview(d.Content, 200)
			t.record(now, "assistant_message", "", preview(d.Content, 200), d.Content)
		}
		// Don't change State here: a tool_start usually follows immediately
		// when the assistant message is empty (tool-call only).

	case *copilot.AssistantUsageData:
		if d.InputTokens != nil {
			t.status.TurnTokensIn = int(*d.InputTokens)
		}
		if d.OutputTokens != nil {
			t.status.TurnTokensOut = int(*d.OutputTokens)
		}
		if d.Model != "" {
			t.status.Model = d.Model
		}

	case *copilot.ToolExecutionStartData:
		fullArgs := fmt.Sprintf("%v", d.Arguments)
		argPreview := preview(fullArgs, 80)
		t.status.State = StateRunningTool
		// Tag the tool name with the originating subagent (if any) so
		// the TUI / log shows e.g. "view[sub:fs-probe]" instead of
		// just "view". Subagent calls carry ParentToolCallID = the
		// task tool_call_id we recorded in SubagentStartedData.
		toolLabel := d.ToolName
		if d.ParentToolCallID != nil {
			if name, ok := t.subagentNames[*d.ParentToolCallID]; ok {
				toolLabel = d.ToolName + "[sub:" + name + "]"
			} else {
				toolLabel = d.ToolName + "[sub:?]"
			}
		}
		t.toolCallNames[d.ToolCallID] = toolLabel
		t.status.CurrentTool = fmt.Sprintf("%s %s", toolLabel, argPreview)
		t.status.CurrentToolStartedAt = now
		t.record(now, "tool_start", toolLabel, argPreview, fullArgs)

	case *copilot.ToolExecutionCompleteData:
		// Resolve the tool label via the start-time map so we are
		// robust to interleaving subagent + parent tool calls.
		toolLabel, ok := t.toolCallNames[d.ToolCallID]
		if !ok {
			// Fallback: derive from currently-running tool field.
			toolLabel = strings.SplitN(t.status.CurrentTool, " ", 2)[0]
		} else {
			delete(t.toolCallNames, d.ToolCallID)
		}
		// Decorate with subagent tag if missing (start event may have
		// arrived before the SubagentStartedData rendezvous).
		if d.ParentToolCallID != nil && !strings.Contains(toolLabel, "[sub:") {
			if name, ok := t.subagentNames[*d.ParentToolCallID]; ok {
				toolLabel = toolLabel + "[sub:" + name + "]"
			} else {
				toolLabel = toolLabel + "[sub:?]"
			}
		}
		t.status.State = StateThinking
		t.status.CurrentTool = ""
		t.status.CurrentToolStartedAt = time.Time{}
		summary := fmt.Sprintf("success=%t", d.Success)
		if !d.Success && d.Error != nil && d.Error.Message != "" {
			msg := preview(d.Error.Message, 200)
			summary = fmt.Sprintf("success=false err=%s", msg)
			if d.Error.Code != nil && *d.Error.Code != "" {
				summary += " code=" + *d.Error.Code
			}
		}
		t.record(now, "tool_complete", toolLabel, summary, summary)

	case *copilot.SubagentStartedData:
		t.subagentNames[d.ToolCallID] = d.AgentName
		summary := fmt.Sprintf("name=%s parent_tool_call_id=%s", d.AgentName, d.ToolCallID)
		t.record(now, "subagent_start", d.AgentName, summary, summary)

	case *copilot.SubagentCompletedData:
		summary := fmt.Sprintf("name=%s success=true", d.AgentName)
		if d.TotalToolCalls != nil {
			summary += fmt.Sprintf(" tool_calls=%d", int(*d.TotalToolCalls))
		}
		if d.DurationMs != nil {
			summary += fmt.Sprintf(" duration_ms=%d", int(*d.DurationMs))
		}
		t.record(now, "subagent_complete", d.AgentName, summary, summary)
		delete(t.subagentNames, d.ToolCallID)

	case *copilot.SubagentFailedData:
		summary := fmt.Sprintf("name=%s success=false err=%s", d.AgentName, preview(d.Error, 200))
		t.record(now, "subagent_complete", d.AgentName, summary, summary)
		delete(t.subagentNames, d.ToolCallID)

	case *copilot.PermissionRequestedData:
		t.status.State = StateWaitingPermission
		toolName := strDeref(d.PermissionRequest.ToolName)
		summary := "requested kind=" + string(d.PermissionRequest.Kind)
		t.record(now, "permission", toolName, summary, summary)

	case *copilot.PermissionCompletedData:
		t.status.State = StateThinking
		summary := "completed kind=" + string(d.Result.Kind)
		t.record(now, "permission", "", summary, summary)
	}
}

// SetClassification stores the per-card classification on the worker
// status. Safe to call from any goroutine; intended to be invoked once at
// session creation time after rule-input parsing has run.
func (t *ActivityTracker) SetClassification(c CardClassification) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.Owner = c.GitHub.Owner
	t.status.Repo = c.GitHub.Repo
	t.status.Kind = c.GitHub.ItemKind
	t.status.Number = c.GitHub.Number
}

// SetWorkDir records the local working directory the SDK session was
// configured with. Safe to call from any goroutine.
func (t *ActivityTracker) SetWorkDir(dir string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.WorkDir = dir
}

// MarkDisconnected forces State=Disconnected without needing an SDK event.
// Called by the dispatcher after the worker goroutine exits.
//
// Resets the per-call lookup maps (toolCallNames / subagentNames). In
// normal operation each START entry is paired with a COMPLETE that
// removes the corresponding map key, but a worker killed mid-tool (or
// a session reaped while a tool call is in flight) never receives the
// COMPLETE event — without this reset those entries would remain in
// memory for the lifetime of the tracker, which the GlobalEventLog
// keeps reachable for the lifetime of the gateway.
func (t *ActivityTracker) MarkDisconnected() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.State = StateDisconnected
	t.toolCallNames = make(map[string]string)
	t.subagentNames = make(map[string]string)
	t.record(time.Now(), "session_shutdown", "", "worker exited", "worker exited")
}

// MarkSessionIdleReaped records that the session was disconnected due to
// idle timeout. The worker goroutine remains alive; a new session will be
// created lazily when the next event arrives.
//
// The tool-name lookup maps are reset for the same reason as in
// MarkDisconnected: the SDK session is gone, so any START entries that
// had not yet seen their COMPLETE will never get one. A fresh session
// for this card starts with a clean tool-id namespace anyway.
func (t *ActivityTracker) MarkSessionIdleReaped() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.State = StateDisconnected
	t.status.CurrentTool = ""
	t.status.CurrentToolStartedAt = time.Time{}
	t.toolCallNames = make(map[string]string)
	t.subagentNames = make(map[string]string)
	t.record(time.Now(), "session_idle_reaped", "", "idle timeout", "idle timeout")
}

// Snapshot returns a copy of the current status plus the ring entries in
// chronological order (oldest first).
func (t *ActivityTracker) Snapshot() (WorkerStatus, []ActivityEntry) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	status := t.status
	entries := t.entriesChronological()
	return status, entries
}

// record appends a truncated entry to the in-memory ring (for TUI/REPL
// display) and writes the FULL untruncated payload to the per-worker log
// file. summary is the short, single-line, possibly preview()-truncated
// rendering for display; full is the untruncated payload to persist (may
// contain newlines). Caller must hold t.mu.
func (t *ActivityTracker) record(at time.Time, kind, tool, summary, full string) {
	t.appendEntry(ActivityEntry{At: at, Kind: kind, Tool: tool, Summary: summary})
	t.writeLogLine(at, kind, tool, full)
}

// writeLogLine appends one line to the per-worker activity log file.
// Newlines and carriage returns within full are escaped to keep one entry
// per line (grep-friendly). No-op if the file could not be created.
// Caller must hold t.mu.
func (t *ActivityTracker) writeLogLine(at time.Time, kind, tool, full string) {
	if t.logFile == nil {
		return
	}
	escaped := strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(full)
	ts := at.Format("2006-01-02T15:04:05.000000000")
	var line string
	if tool != "" {
		line = fmt.Sprintf("%s\t%s\t%s\t%s\n", ts, kind, tool, escaped)
	} else {
		line = fmt.Sprintf("%s\t%s\t\t%s\n", ts, kind, escaped)
	}
	if _, err := t.logFile.WriteString(line); err != nil {
		log.Printf("event=activity_log_write_error card_id=%s err=%v", t.cardID, err)
	}
}

// appendEntry writes one entry into the ring; caller must hold t.mu.
func (t *ActivityTracker) appendEntry(e ActivityEntry) {
	if len(t.entries) < t.ringSize {
		t.entries = append(t.entries, e)
		return
	}
	t.entries[t.head] = e
	t.head = (t.head + 1) % t.ringSize
}

// entriesChronological returns entries oldest first; caller must hold t.mu.
func (t *ActivityTracker) entriesChronological() []ActivityEntry {
	n := len(t.entries)
	out := make([]ActivityEntry, n)
	if n < t.ringSize {
		copy(out, t.entries)
		return out
	}
	copy(out, t.entries[t.head:])
	copy(out[n-t.head:], t.entries[:t.head])
	return out
}

// sanitizeForFilename returns a version of s safe to embed in a filename
// on Windows / POSIX. Non-alphanumeric characters become '_'; the result
// is truncated to 64 chars so paths stay short.
func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "unknown"
	}
	return out
}
