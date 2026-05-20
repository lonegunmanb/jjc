package trelloclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

// newTestServer wires a test client around an httptest.Server. Returns
// the client, the server (so the test can assert on requests it
// received), and a teardown func.
func newTestServer(t *testing.T, handler http.HandlerFunc) (Client, *httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c, err := New(
		WithCredentials("k", "tok"),
		WithServer(srv.URL),
		WithLogger(sysevent.FromLogger(log.New(io.Discard, "", 0))),
	)
	if err != nil {
		srv.Close()
		t.Fatalf("New: %v", err)
	}
	return c, srv, srv.Close
}

func TestNewRejectsMissingCredentials(t *testing.T) {
	if _, err := New(); err == nil {
		t.Fatal("production-shaped New (no credentials, no server) must be rejected")
	}
	if _, err := New(WithCredentials("", "tok")); err == nil {
		t.Fatal("empty key must be rejected")
	}
	if _, err := New(WithCredentials("k", "")); err == nil {
		t.Fatal("empty token must be rejected")
	}
	if _, err := New(WithCredentials("k", "tok"), WithServer("")); err == nil {
		t.Fatal("empty server must be rejected")
	}
	// Test-shaped: server is set, no credentials — must succeed so
	// httptest-driven tests can keep running without producing a real key.
	if _, err := New(WithServer("http://example.invalid")); err != nil {
		t.Fatalf("test-shaped New (server only) must succeed: %v", err)
	}
}

func TestGetCardSendsCredentialsAndDecodes(t *testing.T) {
	var got *http.Request
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","name":"hi","desc":"\nhttps://github.com/x/y/issues/3\nbody","idList":"L1","idBoard":"B1"}`))
	})
	defer done()

	card, err := c.GetCard(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if got == nil {
		t.Fatal("server did not receive request")
	}
	if got.Method != http.MethodGet || !strings.HasPrefix(got.URL.Path, "/cards/c1") {
		t.Errorf("unexpected request: %s %s", got.Method, got.URL.Path)
	}
	q := got.URL.Query()
	if q.Get("key") != "k" || q.Get("token") != "tok" {
		t.Errorf("credentials not appended: %s", got.URL.RawQuery)
	}
	if q.Get("fields") == "" {
		t.Errorf("fields query parameter must be set")
	}
	if card.ID != "c1" || card.Name != "hi" || card.IDList != "L1" || card.IDBoard != "B1" {
		t.Errorf("decoded card mismatch: %+v", card)
	}
	if card.FirstLine != "https://github.com/x/y/issues/3" {
		t.Errorf("FirstLine: got %q want %q", card.FirstLine, "https://github.com/x/y/issues/3")
	}
}

func TestGetCardRejectsEmptyID(t *testing.T) {
	c, _, done := newTestServer(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("server should not be hit when card id is empty")
	})
	defer done()
	if _, err := c.GetCard(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty card id")
	}
}

func TestReconcileBoardWebhookUpdatesExistingBoardWebhook(t *testing.T) {
	var requests []string
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/tokens/tok/webhooks":
			_, _ = w.Write([]byte(`[
				{"id":"other","idModel":"other-board","callbackURL":"https://old.example/"},
				{"id":"hook-1","idModel":"board-1","callbackURL":"https://old.example/"}
			]`))
		case r.Method == http.MethodPut && r.URL.Path == "/webhooks/hook-1":
			if got := r.URL.Query().Get("callbackURL"); got != "https://formal-sent-saw-gpl.trycloudflare.com/" {
				t.Fatalf("callbackURL query: got %q", got)
			}
			_, _ = w.Write([]byte(`{"id":"hook-1"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
	defer done()

	id, createdNow, err := ReconcileBoardWebhook(context.Background(), c, "tok", "board-1", "https://formal-sent-saw-gpl.trycloudflare.com/")
	if err != nil {
		t.Fatalf("ReconcileBoardWebhook: %v", err)
	}
	if id != "hook-1" {
		t.Fatalf("webhook id: got %q", id)
	}
	if createdNow {
		t.Fatalf("updating existing webhook must not set createdNow=true")
	}
	if len(requests) != 2 || !strings.HasPrefix(requests[1], "PUT /webhooks/hook-1?") {
		t.Fatalf("expected GET then PUT, got %#v", requests)
	}
}

func TestReconcileBoardWebhookCreatesWhenMissing(t *testing.T) {
	var sawPost bool
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/tokens/tok/webhooks":
			_, _ = w.Write([]byte(`[{"id":"other","idModel":"other-board"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/tokens/tok/webhooks":
			sawPost = true
			q := r.URL.Query()
			if q.Get("callbackURL") != "https://formal-sent-saw-gpl.trycloudflare.com/" {
				t.Fatalf("callbackURL query: got %q", q.Get("callbackURL"))
			}
			if q.Get("idModel") != "board-1" {
				t.Fatalf("idModel query: got %q", q.Get("idModel"))
			}
			if q.Get("description") != "jjc-gateway" {
				t.Fatalf("description query: got %q", q.Get("description"))
			}
			_, _ = w.Write([]byte(`{"id":"new-hook","idModel":"board-1"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
	defer done()

	id, createdNow, err := ReconcileBoardWebhook(context.Background(), c, "tok", "board-1", "https://formal-sent-saw-gpl.trycloudflare.com/")
	if err != nil {
		t.Fatalf("ReconcileBoardWebhook: %v", err)
	}
	if id != "new-hook" || !sawPost {
		t.Fatalf("expected created webhook id, got id=%q sawPost=%v", id, sawPost)
	}
	if !createdNow {
		t.Fatalf("newly created webhook must set createdNow=true")
	}
}

func TestDeleteWebhookSendsCredentialedRequest(t *testing.T) {
	var got *http.Request
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		if r.Method != http.MethodDelete || r.URL.Path != "/tokens/tok/webhooks/hook-1" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`"hook-1"`))
	})
	defer done()

	if err := c.DeleteWebhook(context.Background(), "tok", "hook-1"); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if got == nil {
		t.Fatal("server did not receive request")
	}
	if q := got.URL.Query(); q.Get("key") != "k" || q.Get("token") != "tok" {
		t.Errorf("credentials not appended: %s", got.URL.RawQuery)
	}
}

// TestDeleteWebhookTreats404AsSuccess locks in the idempotency
// guarantee the gateway's shutdown cleanup path relies on: if the
// webhook is already gone (operator removed it by hand, or a previous
// shutdown ran twice), DeleteWebhook must NOT surface an error.
func TestDeleteWebhookTreats404AsSuccess(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`not found`))
	})
	defer done()

	if err := c.DeleteWebhook(context.Background(), "tok", "hook-1"); err != nil {
		t.Fatalf("expected nil error on 404, got: %v", err)
	}
}

func TestDeleteWebhookSurfacesOther4xx(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`bad token`))
	})
	defer done()

	err := c.DeleteWebhook(context.Background(), "tok", "hook-1")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should reference status code, got: %v", err)
	}
}

func TestDeleteWebhookRejectsEmptyArgs(t *testing.T) {
	c, _, done := newTestServer(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("server should not be hit when args are empty")
	})
	defer done()
	if err := c.DeleteWebhook(context.Background(), "", "hook-1"); err == nil {
		t.Error("empty token must be rejected")
	}
	if err := c.DeleteWebhook(context.Background(), "tok", ""); err == nil {
		t.Error("empty webhook id must be rejected")
	}
}

func TestGetCardSurfacesNon2xx(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`invalid token`))
	})
	defer done()
	_, err := c.GetCard(context.Background(), "c1")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("error should include status and body preview, got %v", err)
	}
}

func TestGetCardListDecodes(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/cards/c1/list") {
			t.Errorf("path: got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"L1","name":"In Progress"}`))
	})
	defer done()
	l, err := c.GetCardList(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetCardList: %v", err)
	}
	if l.ID != "L1" || l.Name != "In Progress" {
		t.Errorf("decoded list mismatch: %+v", l)
	}
}

func TestListBoardListsDecodes(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/boards/B1/lists") {
			t.Errorf("path: got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"L1","name":"Todo"},{"id":"L2","name":"Done"}]`))
	})
	defer done()
	lists, err := c.ListBoardLists(context.Background(), "B1")
	if err != nil {
		t.Fatalf("ListBoardLists: %v", err)
	}
	if len(lists) != 2 || lists[0].ID != "L1" || lists[1].Name != "Done" {
		t.Errorf("decoded lists mismatch: %+v", lists)
	}
}

func TestMoveCardSendsIdListQuery(t *testing.T) {
	var got *http.Request
	var bodyBytes []byte
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		bodyBytes, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"c1"}`))
	})
	defer done()
	if err := c.MoveCard(context.Background(), "c1", "L2"); err != nil {
		t.Fatalf("MoveCard: %v", err)
	}
	if got.Method != http.MethodPut {
		t.Errorf("method: got %s want PUT", got.Method)
	}
	if !strings.HasSuffix(got.URL.Path, "/cards/c1") {
		t.Errorf("path: got %s", got.URL.Path)
	}
	// SDK encodes form params on PUT; check that idList ends up either in
	// the query or in the body.
	if v := got.URL.Query().Get("idList"); v != "L2" {
		// fall back to form-encoded body
		form, err := url.ParseQuery(string(bodyBytes))
		if err != nil || form.Get("idList") != "L2" {
			t.Errorf("idList not transmitted: query=%q body=%q", got.URL.RawQuery, string(bodyBytes))
		}
	}
}

func TestMoveCardRejectsEmptyArgs(t *testing.T) {
	c, _, done := newTestServer(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("server should not be hit when args missing")
	})
	defer done()
	if err := c.MoveCard(context.Background(), "", "L1"); err == nil {
		t.Fatal("expected error for empty card id")
	}
	if err := c.MoveCard(context.Background(), "c1", ""); err == nil {
		t.Fatal("expected error for empty list id")
	}
}

func TestAddCommentPostsAndDecodesAction(t *testing.T) {
	var got *http.Request
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"id":"a1","date":"2026-05-01T10:00:00.000Z","data":{"text":"hello"},"memberCreator":{"id":"m1","fullName":"Alice","username":"alice"}}`))
	})
	defer done()
	cmt, err := c.AddComment(context.Background(), "c1", "hello")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if got.Method != http.MethodPost {
		t.Errorf("method: got %s", got.Method)
	}
	if !strings.HasSuffix(got.URL.Path, "/cards/c1/actions/comments") {
		t.Errorf("path: got %s", got.URL.Path)
	}
	if got.URL.Query().Get("text") != "hello" {
		// fall back to body
		body, _ := io.ReadAll(got.Body)
		form, err := url.ParseQuery(string(body))
		if err != nil || form.Get("text") != "hello" {
			t.Errorf("text not transmitted: query=%q body=%q", got.URL.RawQuery, string(body))
		}
	}
	if cmt.ID != "a1" || cmt.Text != "hello" || cmt.By != "Alice" || cmt.ByID != "m1" {
		t.Errorf("decoded comment mismatch: %+v", cmt)
	}
	if cmt.At.IsZero() {
		t.Errorf("expected non-zero At timestamp")
	}
}

func TestAddCommentValidatesArgs(t *testing.T) {
	c, _, done := newTestServer(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("server should not be hit when args invalid")
	})
	defer done()
	if _, err := c.AddComment(context.Background(), "", "hi"); err == nil {
		t.Fatal("expected error for empty card id")
	}
	if _, err := c.AddComment(context.Background(), "c1", ""); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestGetLatestCommentReturnsErrNoComments(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	defer done()
	_, err := c.GetLatestComment(context.Background(), "c1")
	if err == nil || !errors.Is(err, ErrNoComments) {
		t.Fatalf("expected ErrNoComments, got %v", err)
	}
}

func TestGetLatestCommentPicksMostRecent(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Trello returns newest-first by default; confirm we still pick
		// max(.At) even if the order is shuffled.
		_, _ = w.Write([]byte(`[
			{"id":"a-mid","date":"2026-05-02T10:00:00.000Z","data":{"text":"middle"}},
			{"id":"a-old","date":"2026-05-01T10:00:00.000Z","data":{"text":"old"}},
			{"id":"a-new","date":"2026-05-03T10:00:00.000Z","data":{"text":"new"}}
		]`))
	})
	defer done()
	cmt, err := c.GetLatestComment(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetLatestComment: %v", err)
	}
	if cmt.ID != "a-new" {
		t.Errorf("latest: got %q want %q", cmt.ID, "a-new")
	}
}

func TestListCommentsSinceFiltersAndSorts(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "commentCard" {
			t.Errorf("filter: got %q", r.URL.Query().Get("filter"))
		}
		_, _ = w.Write([]byte(`[
			{"id":"a-new","date":"2026-05-03T10:00:00.000Z","data":{"text":"new"}},
			{"id":"a-mid","date":"2026-05-02T10:00:00.000Z","data":{"text":"middle"}},
			{"id":"a-old","date":"2026-05-01T10:00:00.000Z","data":{"text":"old"}}
		]`))
	})
	defer done()
	cutoff, _ := time.Parse(time.RFC3339, "2026-05-01T20:00:00Z")
	got, err := c.ListCommentsSince(context.Background(), "c1", cutoff)
	if err != nil {
		t.Fatalf("ListCommentsSince: %v", err)
	}
	// Should drop a-old, return a-mid and a-new in oldest-first order.
	if len(got) != 2 || got[0].ID != "a-mid" || got[1].ID != "a-new" {
		t.Errorf("filtered/sorted comments mismatch: %+v", got)
	}
}

func TestListCommentsSinceZeroReturnsAll(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"a","date":"2026-05-01T10:00:00.000Z","data":{"text":"a"}},
			{"id":"b","date":"2026-05-02T10:00:00.000Z","data":{"text":"b"}}
		]`))
	})
	defer done()
	got, err := c.ListCommentsSince(context.Background(), "c1", time.Time{})
	if err != nil {
		t.Fatalf("ListCommentsSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(got))
	}
}

func TestListCommentsSinceSkipsMalformed(t *testing.T) {
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// First entry is invalid JSON (plain int), second is valid.
		_, _ = w.Write([]byte(`[42, {"id":"a","date":"2026-05-01T10:00:00.000Z","data":{"text":"a"}}]`))
	})
	defer done()
	got, err := c.ListCommentsSince(context.Background(), "c1", time.Time{})
	if err != nil {
		t.Fatalf("ListCommentsSince: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("malformed entry should be skipped, got %+v", got)
	}
}

// TestListCommentsSincePaginatesUntilEmpty verifies the wrapper walks
// back through the action history using `before=<lastID>` until Trello
// returns an empty page, instead of stopping after the first page (the
// pre-fix behaviour silently truncated long-lived cards to ~50
// comments).
func TestListCommentsSincePaginatesUntilEmpty(t *testing.T) {
	// Build a fixture of 1500 comments newest-first across two full
	// pages of `commentsPerPage` (1000) plus a partial third page (500).
	mkPage := func(start, end int) string {
		var sb strings.Builder
		sb.WriteString("[")
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteString(",")
			}
			// Use distinct timestamps so sorting and pagination cursor
			// behaviour are observable.
			fmt.Fprintf(&sb, `{"id":"a%05d","date":"2026-01-01T00:00:%02dZ","data":{"text":"t"}}`, i, i%60)
		}
		sb.WriteString("]")
		return sb.String()
	}
	const total = 1500
	var receivedBefore []string
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Capture the `before` cursor each request brought in. Page 1
		// has no cursor; subsequent pages must carry the last id from
		// the previous page.
		receivedBefore = append(receivedBefore, r.URL.Query().Get("before"))
		// Always assert limit=1000 was injected.
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit query param: got %q want %q", got, "1000")
		}
		page := len(receivedBefore) - 1
		switch page {
		case 0:
			_, _ = w.Write([]byte(mkPage(0, 1000)))
		case 1:
			_, _ = w.Write([]byte(mkPage(1000, total)))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	defer done()
	got, err := c.ListCommentsSince(context.Background(), "c1", time.Time{})
	if err != nil {
		t.Fatalf("ListCommentsSince: %v", err)
	}
	if len(got) != total {
		t.Fatalf("expected %d comments after pagination, got %d", total, len(got))
	}
	if len(receivedBefore) < 2 {
		t.Fatalf("expected at least 2 server hits (paginated), got %d", len(receivedBefore))
	}
	if receivedBefore[0] != "" {
		t.Errorf("first request must omit before; got %q", receivedBefore[0])
	}
	if receivedBefore[1] != "a00999" {
		t.Errorf("second request must carry before=last-id-of-page-1; got %q", receivedBefore[1])
	}
}

// TestGetLatestCommentDoesNotPaginate confirms the latest-only path
// only hits the server once; otherwise we would do unbounded work for
// every webhook.
func TestGetLatestCommentDoesNotPaginate(t *testing.T) {
	hits := 0
	c, _, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`[{"id":"a","date":"2026-05-03T10:00:00.000Z","data":{"text":"newest"}}]`))
	})
	defer done()
	if _, err := c.GetLatestComment(context.Background(), "c1"); err != nil {
		t.Fatalf("GetLatestComment: %v", err)
	}
	if hits != 1 {
		t.Fatalf("GetLatestComment should hit the API exactly once, got %d", hits)
	}
}

func TestFirstNonEmptyLine(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"   \n\t\n":           "",
		"first":               "first",
		"\n\n  hello  \nrest": "hello",
	}
	for in, want := range cases {
		if got := firstNonEmptyLine(in); got != want {
			t.Errorf("firstNonEmptyLine(%q): got %q want %q", in, got, want)
		}
	}
}

// TestDecodeCommentActionParsesRFC3339WithFraction confirms
// time.RFC3339 already accepts the optional millisecond fraction Trello
// emits, so a decoded comment carries a non-zero timestamp.
func TestDecodeCommentActionParsesRFC3339WithFraction(t *testing.T) {
	body := []byte(`{"id":"a","date":"2026-05-04T01:02:03.000Z","data":{"text":"x"},"idMemberCreator":"m1"}`)
	c, err := decodeCommentAction(body)
	if err != nil {
		t.Fatalf("decodeCommentAction: %v", err)
	}
	if c.ByID != "m1" {
		t.Errorf("ByID fallback: got %q", c.ByID)
	}
	if c.At.IsZero() {
		t.Errorf("At should not be zero")
	}
}

// TestDecodeCommentActionRejectsMissingDate ensures we never silently
// emit a zero-time Comment, which would mis-order GetLatestComment and
// mis-include entries in ListCommentsSince filtering.
func TestDecodeCommentActionRejectsMissingDate(t *testing.T) {
	body := []byte(`{"id":"a","data":{"text":"x"}}`)
	if _, err := decodeCommentAction(body); err == nil {
		t.Fatal("missing date must surface as an error")
	}
}

func TestDecodeCommentActionRejectsBadDate(t *testing.T) {
	body := []byte(`{"id":"a","date":"not-a-date","data":{"text":"x"}}`)
	if _, err := decodeCommentAction(body); err == nil {
		t.Fatal("unparseable date must surface as an error")
	}
}

// stubDoer is a HttpRequestDoer that always returns a canned response.
// It exists to verify WithHTTPClient is honored.
type stubDoer struct {
	calls int
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	s.calls++
	body, _ := json.Marshal(map[string]string{"id": "c1", "name": "n", "desc": "", "idList": "L", "idBoard": "B"})
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestWithHTTPClientIsHonored(t *testing.T) {
	d := &stubDoer{}
	c, err := New(WithCredentials("k", "tok"), WithHTTPClient(d))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetCard(context.Background(), "c1"); err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if d.calls != 1 {
		t.Fatalf("expected exactly 1 stubDoer call, got %d", d.calls)
	}
}
