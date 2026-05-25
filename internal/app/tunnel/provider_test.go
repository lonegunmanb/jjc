package tunnel

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestParseQuickTunnelURL(t *testing.T) {
	line := `2026-05-18T03:14:15Z INF | https://formal-sent-saw-gpl.trycloudflare.com  |`
	got, ok := ParseQuickTunnelURL(line)
	if !ok {
		t.Fatal("expected URL match")
	}
	if got != "https://formal-sent-saw-gpl.trycloudflare.com/" {
		t.Fatalf("got %q", got)
	}
}

func TestParseQuickTunnelURLNoMatch(t *testing.T) {
	if got, ok := ParseQuickTunnelURL("no tunnel here"); ok || got != "" {
		t.Fatalf("got (%q, %v), want no match", got, ok)
	}
}

func TestNormalizePublicURLAppendsExactlyOneSlash(t *testing.T) {
	for _, raw := range []string{
		"https://formal-sent-saw-gpl.trycloudflare.com",
		" https://formal-sent-saw-gpl.trycloudflare.com/ ",
		"https://formal-sent-saw-gpl.trycloudflare.com////",
	} {
		if got := NormalizePublicURL(raw); got != "https://formal-sent-saw-gpl.trycloudflare.com/" {
			t.Fatalf("NormalizePublicURL(%q) = %q", raw, got)
		}
	}
}

func TestCloudflaredProviderStartDoesNotLeakLogConsumerWhenReadersDrain(t *testing.T) {
	t.Parallel()

	before := runtime.NumGoroutine()
	fakeCloudflared := buildExitingFakeCloudflared(t)
	headClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodHead || r.URL.String() != "https://formal-sent-saw-gpl.trycloudflare.com/" {
			t.Fatalf("unexpected HEAD self-test request: %s %s", r.Method, r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})}
	provider, err := NewCloudflaredProvider(
		WithBinary(fakeCloudflared),
		WithHTTPClient(headClient),
		WithLogger(sysevent.FromLogger(log.New(io.Discard, "", 0))),
	)
	if err != nil {
		t.Fatalf("NewCloudflaredProvider: %v", err)
	}
	defer func() { _ = provider.Stop() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := provider.Start(ctx, "127.0.0.1:18790")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got != "https://formal-sent-saw-gpl.trycloudflare.com/" {
		t.Fatalf("Start URL = %q", got)
	}

	assertNoGoroutineDelta(t, before, 3*time.Second)
}

func assertNoGoroutineDelta(t *testing.T, before int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		runtime.Gosched()
		if delta := runtime.NumGoroutine() - before; delta <= 0 {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("goroutine delta = %d, want 0; log consumer goroutine did not exit after cloudflared readers drained", delta)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func buildExitingFakeCloudflared(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(`package main
import (
"fmt"
"os"
"time"
)
func main() {
if len(os.Args) != 4 || os.Args[1] != "tunnel" || os.Args[2] != "--url" || os.Args[3] != "http://localhost:18790" {
fmt.Fprintf(os.Stderr, "unexpected args: %v\n", os.Args[1:])
os.Exit(2)
}
fmt.Fprintln(os.Stderr, "2026-05-18T03:14:15Z INF | https://formal-sent-saw-gpl.trycloudflare.com  |")
_ = os.Stdout.Close()
_ = os.Stderr.Close()
time.Sleep(100 * time.Millisecond)
}
`), 0o600); err != nil {
		t.Fatalf("write fake cloudflared source: %v", err)
	}
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake cloudflared: %v\n%s", err, out)
	}
	return bin
}
