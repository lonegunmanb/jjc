package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

type fakeStatusProvider struct {
	list []WorkerStatus
	snap map[string]struct {
		st      WorkerStatus
		entries []ActivityEntry
	}
}

func (f *fakeStatusProvider) ListCards() []WorkerStatus { return f.list }
func (f *fakeStatusProvider) Snapshot(cardID string) (WorkerStatus, []ActivityEntry, bool) {
	v, ok := f.snap[cardID]
	if !ok {
		return WorkerStatus{}, nil, false
	}
	return v.st, v.entries, true
}

func runREPL(t *testing.T, provider StatusProvider, input string) string {
	t.Helper()
	var out bytes.Buffer
	repl := NewREPL(provider, strings.NewReader(input), &out)
	if err := repl.Run(context.Background()); err != nil {
		t.Fatalf("repl.Run: %v", err)
	}
	return out.String()
}

func TestREPL_QuitImmediately(t *testing.T) {
	out := runREPL(t, &fakeStatusProvider{}, "quit\n")
	if !strings.Contains(out, "bye.") {
		t.Fatalf("expected bye, got: %s", out)
	}
}

func TestREPL_LsEmpty(t *testing.T) {
	out := runREPL(t, &fakeStatusProvider{}, "ls\nquit\n")
	if !strings.Contains(out, "(no active workers)") {
		t.Fatalf("expected empty notice, got: %s", out)
	}
}

func TestREPL_LsAndShow(t *testing.T) {
	now := time.Now()
	provider := &fakeStatusProvider{
		list: []WorkerStatus{
			{CardID: "card-A", State: StateRunningTool, InboxDepth: 0, LastEventAt: now},
			{CardID: "card-B", State: StateIdle, InboxDepth: 2, LastEventAt: now},
		},
		snap: map[string]struct {
			st      WorkerStatus
			entries []ActivityEntry
		}{
			"card-A": {
				st: WorkerStatus{CardID: "card-A", State: StateRunningTool, CurrentTool: "read_powershell shellId=173", CurrentToolStartedAt: now, Model: "claude", SessionID: "s1", TurnTokensIn: 100, TurnTokensOut: 5, LastEventAt: now, LastAssistantPreview: "thinking..."},
				entries: []ActivityEntry{
					{At: now, Kind: "tool_start", Tool: "read_powershell", Summary: "shellId=173"},
				},
			},
		},
	}
	out := runREPL(t, provider, "ls\nshow card-A\nshow missing\nquit\n")
	if !strings.Contains(out, "card-A") || !strings.Contains(out, "card-B") {
		t.Fatalf("ls missing cards: %s", out)
	}
	if !strings.Contains(out, "current_tool:   read_powershell shellId=173") {
		t.Fatalf("show missing current_tool: %s", out)
	}
	if !strings.Contains(out, "tool_start") {
		t.Fatalf("show missing recent activity: %s", out)
	}
	if !strings.Contains(out, `no active worker for card "missing"`) {
		t.Fatalf("expected missing-card notice: %s", out)
	}
}

func TestREPL_UnknownCommand(t *testing.T) {
	out := runREPL(t, &fakeStatusProvider{}, "wat\nquit\n")
	if !strings.Contains(out, "unknown command: wat") {
		t.Fatalf("expected unknown command notice: %s", out)
	}
}

func TestREPL_ContextCancel(t *testing.T) {
	provider := &fakeStatusProvider{}
	var out bytes.Buffer
	// blocking reader
	pr, _ := newBlockingReader()
	repl := NewREPL(provider, pr, &out)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = repl.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("REPL did not exit after ctx cancel")
	}
	if !strings.Contains(out.String(), "context cancelled") {
		t.Fatalf("expected ctx cancel notice: %s", out.String())
	}
}

// newBlockingReader returns a Reader whose Read blocks until the writer is
// closed. Used to simulate stdin with no input arriving.
func newBlockingReader() (*blockingReader, func()) {
	r := &blockingReader{ch: make(chan struct{})}
	return r, func() { close(r.ch) }
}

type blockingReader struct{ ch chan struct{} }

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.ch
	return 0, nil
}
