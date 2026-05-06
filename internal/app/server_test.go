package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func signTrello(secret string, rawBody []byte, callbackURL string) string {
	h := hmac.New(sha1.New, []byte(secret))
	_, _ = h.Write(rawBody)
	_, _ = h.Write([]byte(callbackURL))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func payload(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

// newStubbedRunner returns a CopilotRunner whose dispatcher uses the given
// SessionFactory. It is the standard test rig for end-to-end HTTP tests
// that don't want to spin up a real Copilot CLI process.
func newStubbedRunner(t *testing.T, factory SessionFactory) *CopilotRunner {
	t.Helper()
	r := NewCopilotRunner("stub-model", log.Default())
	r.tmpDir = t.TempDir()
	r.dispatcher = NewDispatcher(log.Default(), factory)
	t.Cleanup(func() { r.dispatcher.Stop() })
	return r
}

func TestHeadReturns200(t *testing.T) {
	cfg := Config{ListenAddr: ":0", TrelloSecret: "secret", CallbackURL: "https://example.com/trello", CopilotModel: "stub"}
	r := NewRouterWithRunner(cfg, newStubbedRunner(t, newFakeFactory()), log.Default())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestPostRejectsBadSignature403(t *testing.T) {
	cfg := Config{ListenAddr: ":0", TrelloSecret: "secret", CallbackURL: "https://example.com/trello", CopilotModel: "stub"}
	factory := newFakeFactory()
	r := NewRouterWithRunner(cfg, newStubbedRunner(t, factory), log.Default())

	body := payload(t, map[string]any{"action": map[string]any{"type": "updateCard"}})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Trello-Webhook", "bad-sign")
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
	time.Sleep(20 * time.Millisecond)
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.sessions) != 0 {
		t.Fatalf("should not create a session for invalid signature, got %d", len(factory.sessions))
	}
}

func TestPostValidSignatureDispatchesEvent(t *testing.T) {
	cfg := Config{ListenAddr: ":0", TrelloSecret: "secret", CallbackURL: "https://example.com/trello", CopilotModel: "stub"}
	factory := newFakeFactory()
	r := NewRouterWithRunner(cfg, newStubbedRunner(t, factory), log.Default())

	body := payload(t, map[string]any{
		"action": map[string]any{
			"type": "updateCard",
			"data": map[string]any{
				"card":       map[string]any{"id": "card-1", "name": "Fix bug"},
				"board":      map[string]any{"name": "Main Board"},
				"listBefore": map[string]any{"id": "lb", "name": "Need Attention"},
				"listAfter":  map[string]any{"id": "la", "name": "Analyze"},
			},
			"memberCreator": map[string]any{"fullName": "Roger"},
		},
	})
	sig := signTrello(cfg.TrelloSecret, body, cfg.CallbackURL)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Trello-Webhook", sig)
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", rr.Code)
	}

	waitFor(t, time.Second, func() bool {
		s := factory.get("card-1")
		if s == nil {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.prompts) == 1
	}, "card-1 worker to receive the event")

	s := factory.get("card-1")
	s.mu.Lock()
	prompt := s.prompts[0]
	s.mu.Unlock()
	for _, want := range []string{"# TASK", "Analyze", "\"action\""} {
		if !bytes.Contains([]byte(prompt), []byte(want)) {
			t.Fatalf("prompt missing %q; got:\n%s", want, prompt)
		}
	}
}

func TestPostReturns202EvenWhenSessionCreationFails(t *testing.T) {
	cfg := Config{ListenAddr: ":0", TrelloSecret: "secret", CallbackURL: "https://example.com/trello", CopilotModel: "stub"}
	factory := newFakeFactory()
	factory.createErr = errExec
	runner := newStubbedRunner(t, factory)
	r := NewRouterWithRunner(cfg, runner, log.Default())

	body := payload(t, map[string]any{
		"action": map[string]any{
			"type": "updateCard",
			"data": map[string]any{
				"card":      map[string]any{"id": "card-fail"},
				"listAfter": map[string]any{"name": "Analyze"},
			},
		},
	})
	sig := signTrello(cfg.TrelloSecret, body, cfg.CallbackURL)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Trello-Webhook", sig)
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202 (errors are async-logged, not HTTP), got %d", rr.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	cfg := Config{ListenAddr: ":0", TrelloSecret: "secret", CallbackURL: "https://example.com/trello", CopilotModel: "stub"}
	r := NewRouterWithRunner(cfg, newStubbedRunner(t, newFakeFactory()), log.Default())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestEventsForDifferentCardsRunInParallel(t *testing.T) {
	// Cross-card concurrency is the whole point of the new architecture.
	// Each fake session blocks for 80ms; with serial processing 4 cards
	// would take >320ms, parallel processing should finish well under that.
	cfg := Config{ListenAddr: ":0", TrelloSecret: "secret", CallbackURL: "https://example.com/trello", CopilotModel: "stub"}
	factory := newFakeFactory()
	factory.delay = 80 * time.Millisecond
	runner := newStubbedRunner(t, factory)
	r := NewRouterWithRunner(cfg, runner, log.Default())

	cards := []string{"c1", "c2", "c3", "c4"}
	var wg sync.WaitGroup
	started := time.Now()
	for _, c := range cards {
		wg.Add(1)
		go func(card string) {
			defer wg.Done()
			body := payload(t, map[string]any{"action": map[string]any{
				"type": "updateCard",
				"data": map[string]any{
					"card":      map[string]any{"id": card},
					"listAfter": map[string]any{"name": "Analyze"},
				},
			}})
			sig := signTrello(cfg.TrelloSecret, body, cfg.CallbackURL)
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			req.Header.Set("X-Trello-Webhook", sig)
			r.ServeHTTP(rr, req)
		}(c)
	}
	wg.Wait()

	waitFor(t, 2*time.Second, func() bool {
		factory.mu.Lock()
		defer factory.mu.Unlock()
		if len(factory.sessions) != len(cards) {
			return false
		}
		for _, s := range factory.sessions {
			s.mu.Lock()
			n := len(s.prompts)
			s.mu.Unlock()
			if n == 0 {
				return false
			}
		}
		return true
	}, "all four cards processed")

	elapsed := time.Since(started)
	// Serial would be ~320ms; parallel should be ~80ms plus overhead.
	if elapsed > 250*time.Millisecond {
		t.Fatalf("expected cross-card parallelism, elapsed=%s", elapsed)
	}
}

func TestEventsForSameCardAreSerialised(t *testing.T) {
	cfg := Config{ListenAddr: ":0", TrelloSecret: "secret", CallbackURL: "https://example.com/trello", CopilotModel: "stub"}
	factory := newFakeFactory()
	factory.delay = 30 * time.Millisecond
	runner := newStubbedRunner(t, factory)
	r := NewRouterWithRunner(cfg, runner, log.Default())

	body := payload(t, map[string]any{"action": map[string]any{
		"type": "updateCard",
		"data": map[string]any{
			"card":      map[string]any{"id": "same-card"},
			"listAfter": map[string]any{"name": "Analyze"},
		},
	}})
	sig := signTrello(cfg.TrelloSecret, body, cfg.CallbackURL)

	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			req.Header.Set("X-Trello-Webhook", sig)
			r.ServeHTTP(rr, req)
		}()
	}
	wg.Wait()

	waitFor(t, 2*time.Second, func() bool {
		s := factory.get("same-card")
		if s == nil {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.prompts) == n
	}, "all events delivered")

	// Only one session per card.
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(factory.sessions))
	}
}

func TestDuplicateActionIDsAreDeduped(t *testing.T) {
	cfg := Config{ListenAddr: ":0", TrelloSecret: "secret", CallbackURL: "https://example.com/trello", CopilotModel: "stub"}
	factory := newFakeFactory()
	runner := newStubbedRunner(t, factory)
	r := NewRouterWithRunner(cfg, runner, log.Default())

	body := payload(t, map[string]any{"action": map[string]any{
		"id":   "act-dup-1",
		"type": "updateCard",
		"data": map[string]any{
			"card":      map[string]any{"id": "dup"},
			"listAfter": map[string]any{"name": "Analyze"},
		},
	}})
	sig := signTrello(cfg.TrelloSecret, body, cfg.CallbackURL)

	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("X-Trello-Webhook", sig)
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusAccepted {
			t.Fatalf("expected 202 for duplicates, got %d", rr.Code)
		}
	}

	waitFor(t, time.Second, func() bool {
		s := factory.get("dup")
		if s == nil {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.prompts) == 1
	}, "first event to land")
	time.Sleep(100 * time.Millisecond)

	s := factory.get("dup")
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.prompts) != 1 {
		t.Fatalf("expected exactly one prompt for duplicate action.id, got %d", len(s.prompts))
	}
}
