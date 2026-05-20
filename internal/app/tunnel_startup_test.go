package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/jjc/internal/app/trelloclient"
	"github.com/lonegunmanb/jjc/internal/app/tunnel"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStartTunnelAndReconcileDefaultPathUpdatesWebhook(t *testing.T) {
	fakeCloudflared := buildFakeCloudflared(t)
	var sawPut bool
	trelloServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/tokens/tok/webhooks":
			_, _ = w.Write([]byte(`[
{"id":"other","idModel":"other-board"},
{"id":"hook-1","idModel":"board-1","callbackURL":"https://old.example/"}
]`))
		case r.Method == http.MethodPut && r.URL.Path == "/webhooks/hook-1":
			sawPut = true
			if got := r.URL.Query().Get("callbackURL"); got != "https://formal-sent-saw-gpl.trycloudflare.com/" {
				t.Fatalf("callbackURL query: got %q", got)
			}
			_, _ = w.Write([]byte(`{"id":"hook-1"}`))
		default:
			t.Fatalf("unexpected Trello request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer trelloServer.Close()

	trelloClient, err := trelloclient.New(
		trelloclient.WithCredentials("key", "tok"),
		trelloclient.WithServer(trelloServer.URL),
		trelloclient.WithLogger(log.New(io.Discard, "", 0)),
	)
	if err != nil {
		t.Fatalf("trelloclient.New: %v", err)
	}

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
	provider, err := tunnel.NewCloudflaredProvider(
		tunnel.WithBinary(fakeCloudflared),
		tunnel.WithHTTPClient(headClient),
		tunnel.WithLogger(log.New(io.Discard, "", 0)),
	)
	if err != nil {
		t.Fatalf("NewCloudflaredProvider: %v", err)
	}
	defer func() { _ = provider.Stop() }()

	cfg := Config{Tunnel: tunnel.Cloudflared, TrelloAPIToken: "tok", KanbanBoardID: "board-1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := StartTunnelAndReconcile(ctx, &cfg, provider, trelloClient, "127.0.0.1:18790", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("StartTunnelAndReconcile: %v", err)
	}
	if id != "hook-1" || !sawPut {
		t.Fatalf("expected PUT to hook-1, got id=%q sawPut=%v", id, sawPut)
	}
	if cfg.CallbackURL != "https://formal-sent-saw-gpl.trycloudflare.com/" {
		t.Fatalf("callback URL not set in memory: %q", cfg.CallbackURL)
	}
}

// TestStartTunnelAndReconcileWithOwnershipReportsCreatedFlag verifies the
// new createdNow return value: true when this call POSTed a brand-new
// webhook (i.e. the gateway owns it and shutdown should delete it),
// false when it only PUT-updated an existing one (operator owns it;
// leave alone).
func TestStartTunnelAndReconcileWithOwnershipReportsCreatedFlag(t *testing.T) {
	cases := []struct {
		name           string
		listResponse   string
		wantCreatedNow bool
		wantWebhookID  string
	}{
		{
			name:           "existing-webhook-is-updated-not-owned",
			listResponse:   `[{"id":"hook-existing","idModel":"board-1","callbackURL":"https://old/"}]`,
			wantCreatedNow: false,
			wantWebhookID:  "hook-existing",
		},
		{
			name:           "missing-webhook-is-created-and-owned",
			listResponse:   `[]`,
			wantCreatedNow: true,
			wantWebhookID:  "hook-new",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCloudflared := buildFakeCloudflared(t)
			trelloServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/tokens/tok/webhooks":
					_, _ = w.Write([]byte(tc.listResponse))
				case r.Method == http.MethodPut && r.URL.Path == "/webhooks/hook-existing":
					_, _ = w.Write([]byte(`{"id":"hook-existing"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/tokens/tok/webhooks":
					_, _ = w.Write([]byte(`{"id":"hook-new","idModel":"board-1"}`))
				default:
					t.Fatalf("unexpected Trello request: %s %s", r.Method, r.URL.String())
				}
			}))
			defer trelloServer.Close()

			trelloClient, err := trelloclient.New(
				trelloclient.WithCredentials("key", "tok"),
				trelloclient.WithServer(trelloServer.URL),
				trelloclient.WithLogger(log.New(io.Discard, "", 0)),
			)
			if err != nil {
				t.Fatalf("trelloclient.New: %v", err)
			}

			headClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    r,
				}, nil
			})}
			provider, err := tunnel.NewCloudflaredProvider(
				tunnel.WithBinary(fakeCloudflared),
				tunnel.WithHTTPClient(headClient),
				tunnel.WithLogger(log.New(io.Discard, "", 0)),
			)
			if err != nil {
				t.Fatalf("NewCloudflaredProvider: %v", err)
			}
			defer func() { _ = provider.Stop() }()

			cfg := Config{Tunnel: tunnel.Cloudflared, TrelloAPIToken: "tok", KanbanBoardID: "board-1"}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			id, createdNow, err := StartTunnelAndReconcileWithOwnership(ctx, &cfg, provider, trelloClient, "127.0.0.1:18790", log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatalf("StartTunnelAndReconcileWithOwnership: %v", err)
			}
			if id != tc.wantWebhookID {
				t.Fatalf("webhook id: got %q want %q", id, tc.wantWebhookID)
			}
			if createdNow != tc.wantCreatedNow {
				t.Fatalf("createdNow: got %v want %v", createdNow, tc.wantCreatedNow)
			}
		})
	}
}

// TestDeleteGatewayCreatedWebhookSkipsNonOwnedWebhooks pins one half of
// the cleanup contract main.go relies on: the helper is a no-op for a
// webhook the gateway did NOT create (operator-managed, stable DNS-
// backed callback URL). Crucially: do NOT issue a DELETE.
func TestDeleteGatewayCreatedWebhookSkipsNonOwnedWebhooks(t *testing.T) {
	trelloServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Fatalf("shutdown cleanup must NOT hit Trello when createdNow=false (got %s %s)", r.Method, r.URL.String())
	}))
	defer trelloServer.Close()

	trelloClient, err := trelloclient.New(
		trelloclient.WithCredentials("key", "tok"),
		trelloclient.WithServer(trelloServer.URL),
		trelloclient.WithLogger(log.New(io.Discard, "", 0)),
	)
	if err != nil {
		t.Fatalf("trelloclient.New: %v", err)
	}
	cfg := Config{TrelloAPIToken: "tok", KanbanBoardID: "board-1"}
	if err := DeleteGatewayCreatedWebhook(context.Background(), &cfg, trelloClient, "hook-existing", false, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("DeleteGatewayCreatedWebhook: %v", err)
	}
}

// TestDeleteGatewayCreatedWebhookDeletesOwnedWebhook pins the other
// half: for a webhook this process created, the helper issues the
// DELETE so a defunct trycloudflare URL does not keep a dangling
// webhook on Trello.
func TestDeleteGatewayCreatedWebhookDeletesOwnedWebhook(t *testing.T) {
	var sawDelete bool
	trelloServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/tokens/tok/webhooks/hook-new" {
			sawDelete = true
			_, _ = w.Write([]byte(`"hook-new"`))
			return
		}
		t.Fatalf("unexpected Trello request: %s %s", r.Method, r.URL.String())
	}))
	defer trelloServer.Close()

	trelloClient, err := trelloclient.New(
		trelloclient.WithCredentials("key", "tok"),
		trelloclient.WithServer(trelloServer.URL),
		trelloclient.WithLogger(log.New(io.Discard, "", 0)),
	)
	if err != nil {
		t.Fatalf("trelloclient.New: %v", err)
	}
	cfg := Config{TrelloAPIToken: "tok", KanbanBoardID: "board-1"}
	if err := DeleteGatewayCreatedWebhook(context.Background(), &cfg, trelloClient, "hook-new", true, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("DeleteGatewayCreatedWebhook: %v", err)
	}
	if !sawDelete {
		t.Fatal("expected DELETE request for owned webhook")
	}
}

// TestDeleteGatewayCreatedWebhookIsIdempotent verifies the second-call
// safety guarantee: a 404 from Trello (webhook already gone) is not an
// error. main.go's defer chain may run twice in pathological
// shutdown sequences; the cleanup must not surface a spurious failure.
func TestDeleteGatewayCreatedWebhookIsIdempotent(t *testing.T) {
	trelloServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer trelloServer.Close()

	trelloClient, err := trelloclient.New(
		trelloclient.WithCredentials("key", "tok"),
		trelloclient.WithServer(trelloServer.URL),
		trelloclient.WithLogger(log.New(io.Discard, "", 0)),
	)
	if err != nil {
		t.Fatalf("trelloclient.New: %v", err)
	}
	cfg := Config{TrelloAPIToken: "tok", KanbanBoardID: "board-1"}
	if err := DeleteGatewayCreatedWebhook(context.Background(), &cfg, trelloClient, "hook-gone", true, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("DeleteGatewayCreatedWebhook on a 404 webhook should be a no-op, got: %v", err)
	}
}

func buildFakeCloudflared(t *testing.T) string {
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
time.Sleep(10 * time.Minute)
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
