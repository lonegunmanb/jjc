package sysevent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSinkOpenSuccessWritesFileMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.log")
	s := NewFileSink(WithLogFile(path))
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close log file: %v", err)
		}
	})

	Emitf(s, "gateway_starting", "trello_api_secret=%s log_file=%q", "<redacted len=11>", path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("log file mode = %v, want 0600", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	line := string(b)
	if !strings.Contains(line, `event=gateway_starting trello_api_secret=<redacted len=11> log_file="`+path+`"`) {
		t.Fatalf("unexpected log line: %q", line)
	}
	if strings.Contains(line, "supersecret") {
		t.Fatalf("log line leaked secret: %q", line)
	}
}

func TestFileSinkOpenFailureFallsBackToStderr(t *testing.T) {
	var stderr bytes.Buffer
	s := NewFileSink(WithLogFile(t.TempDir()), WithStderr(&stderr))

	Emitf(s, "fallback_test", "answer=%d", 42)

	out := stderr.String()
	if !strings.Contains(out, "warning: cannot open log file") || !strings.Contains(out, "falling back to stderr") {
		t.Fatalf("missing fallback warning: %q", out)
	}
	if !strings.Contains(out, "event=fallback_test answer=42") {
		t.Fatalf("missing fallback event: %q", out)
	}
}

type captor struct {
	events []Event
}

func (c *captor) Emit(e Event) {
	c.events = append(c.events, e)
}

func TestSetDefaultCaptorSink(t *testing.T) {
	old := Default()
	defer Set(old)

	c := &captor{}
	Set(c)
	Emitf(Default(), "captured", "card_id=%s", "abc123")

	if len(c.events) != 1 {
		t.Fatalf("captured %d events, want 1", len(c.events))
	}
	if got := c.events[0]; got.Token != "captured" || got.Message != "card_id=abc123" {
		t.Fatalf("unexpected event: %+v", got)
	}
}
