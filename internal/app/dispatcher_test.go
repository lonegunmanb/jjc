package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lonegunmanb/jjc/internal/app/kanban"
	"github.com/lonegunmanb/jjc/internal/app/router"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

// fakeSession captures every prompt sent to it. Each fakeSession is bound
// to one cardID so tests can assert per-card serialisation.
type fakeSession struct {
	cardID          string
	delay           time.Duration
	mu              sync.Mutex
	prompts         []string
	concurrent      int32
	maxConcurrent   int32
	disconnectCount int32
}

func (f *fakeSession) SendAndWait(ctx context.Context, prompt string) error {
	c := atomic.AddInt32(&f.concurrent, 1)
	for {
		old := atomic.LoadInt32(&f.maxConcurrent)
		if c <= old {
			break
		}
		if atomic.CompareAndSwapInt32(&f.maxConcurrent, old, c) {
			break
		}
	}
	defer atomic.AddInt32(&f.concurrent, -1)

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) Disconnect() error {
	atomic.AddInt32(&f.disconnectCount, 1)
	return nil
}

// fakeFactory hands out a unique fakeSession per cardID and tracks
// creation order.
type fakeFactory struct {
	delay       time.Duration
	mu          sync.Mutex
	sessions    map[string]*fakeSession
	createOrder []string
	createErr   error
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{sessions: make(map[string]*fakeSession)}
}

func (f *fakeFactory) NewWorkerSession(_ context.Context, cardID string, _ *ActivityTracker) (WorkerSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	s := &fakeSession{cardID: cardID, delay: f.delay}
	f.sessions[cardID] = s
	f.createOrder = append(f.createOrder, cardID)
	return s, nil
}

func (f *fakeFactory) get(cardID string) *fakeSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[cardID]
}

type cancelBlockingSession struct {
	started chan struct{}
	ctxDone chan struct{}
}

func newCancelBlockingSession() *cancelBlockingSession {
	return &cancelBlockingSession{
		started: make(chan struct{}),
		ctxDone: make(chan struct{}),
	}
}

func (s *cancelBlockingSession) SendAndWait(ctx context.Context, prompt string) error {
	close(s.started)
	<-ctx.Done()
	close(s.ctxDone)
	return ctx.Err()
}

func (s *cancelBlockingSession) Disconnect() error {
	return nil
}

func dispatchEvent(t *testing.T, d *Dispatcher, eventID, body string) {
	t.Helper()
	if err := d.Dispatch(context.Background(), eventID, []byte(body)); err != nil {
		t.Fatalf("dispatch %s: %v", eventID, err)
	}
}

func waitFor(t *testing.T, deadline time.Duration, cond func() bool, msg string) {
	t.Helper()
	limit := time.Now().Add(deadline)
	for time.Now().Before(limit) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", msg)
}

func TestAssembleDepartureNoticeUsesKanbanNames(t *testing.T) {
	rawBody := []byte(`{"action":{"type":"updateCard","data":{"card":{"id":"card1"},"listAfter":{"name":"Done"}}}}`)
	slimBody := []byte(`{"action":{"type":"updateCard"}}`)
	view := &kanban.Resolved{
		Plan:   kanban.Role{Name: "调研"},
		Action: kanban.Role{Name: "执行"},
	}

	got := assembleDepartureNotice(rawBody, slimBody, "Done", view)
	for _, want := range []string{"调研", "执行"} {
		if !strings.Contains(got, want) {
			t.Errorf("departure notice missing kanban name %q: %s", want, got)
		}
	}
	for _, legacy := range []string{"Analyze", "In action"} {
		if strings.Contains(got, legacy) {
			t.Errorf("departure notice contains legacy name %q: %s", legacy, got)
		}
	}
}

func TestAssembleDepartureNoticeNilViewFallsBackToLegacyNames(t *testing.T) {
	rawBody := []byte(`{"action":{"type":"updateCard","data":{"card":{"id":"card1"},"listAfter":{"name":"Done"}}}}`)
	slimBody := []byte(`{"action":{"type":"updateCard"}}`)

	got := assembleDepartureNotice(rawBody, slimBody, "Done", nil)
	for _, want := range []string{"Analyze", "In action"} {
		if !strings.Contains(got, want) {
			t.Errorf("departure notice missing legacy name %q: %s", want, got)
		}
	}
}

func TestAssembleTerminateNoticeDoesNotHardcodeWorkDirPath(t *testing.T) {
	notice := assembleTerminateNotice(
		[]byte(`{"action":{"type":"deleteCard"}}`),
		[]byte(`{"action":{"type":"deleteCard"}}`),
		"done",
	)

	for _, forbidden := range []string{`C:\project\`, "<card_id>"} {
		if strings.Contains(notice, forbidden) {
			t.Fatalf("assembleTerminateNotice() contains forbidden work_dir placeholder %q\n%s", forbidden, notice)
		}
	}

	lower := strings.ToLower(notice)
	for _, want := range []string{"work_dir", "card context"} {
		if !strings.Contains(lower, want) {
			t.Fatalf("assembleTerminateNotice() missing %q\n%s", want, notice)
		}
	}
}

func TestEvaluateRoutePopulatesListIDs(t *testing.T) {
	cfg, err := router.DecodeConfig([]byte(`
route "moved_by_id" {
  when   = action.type == "updateCard" && action.list_after_id == "L_PLAN"
  do     = "dispatch"
  reason = "moved_by_id"
}

route "created_by_id" {
  when   = action.type == "createCard" && action.list_id == "L_ACTION"
  do     = "dispatch"
  reason = "created_by_id"
}

route "catch_all" {
  when   = true
  do     = "drop"
  reason = "catch_all"
}
`), "ids.hcl")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}

	cases := []struct {
		name       string
		body       string
		wantReason string
	}{
		{
			name:       "updateCard listAfter id",
			body:       `{"action":{"type":"updateCard","data":{"card":{"id":"card1"},"listAfter":{"id":"L_PLAN","name":"Renamed"}}}}`,
			wantReason: "moved_by_id",
		},
		{
			name:       "createCard list id",
			body:       `{"action":{"type":"createCard","data":{"card":{"id":"card1"},"list":{"id":"L_ACTION","name":"Renamed"}}}}`,
			wantReason: "created_by_id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(sysevent.Default(), newFakeFactory())
			d.SetRouteEngine(router.NewEngine(cfg, nil, sysevent.Default()))
			got := d.evaluateRoute([]byte(tc.body))
			if got.Action != RouteDispatch || got.Reason != tc.wantReason {
				t.Errorf("evaluateRoute() = %+v; want action=%s reason=%s", got, RouteDispatch, tc.wantReason)
			}
		})
	}
}

func TestDispatcherDifferentCardsRunInParallel(t *testing.T) {
	factory := newFakeFactory()
	factory.delay = 80 * time.Millisecond
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	cards := []string{"aaa", "bbb", "ccc", "ddd"}
	for _, c := range cards {
		body := `{"action":{"type":"updateCard","data":{"card":{"id":"` + c + `"},"listAfter":{"name":"Analyze"}}}}`
		dispatchEvent(t, d, "evt-"+c, body)
	}

	// Wait until all four sessions exist (they're created lazily).
	waitFor(t, 2*time.Second, func() bool {
		factory.mu.Lock()
		defer factory.mu.Unlock()
		return len(factory.sessions) == 4
	}, "four worker sessions to be created")

	// Wait until all four have processed their single event.
	waitFor(t, 2*time.Second, func() bool {
		for _, c := range cards {
			s := factory.get(c)
			if s == nil {
				return false
			}
			s.mu.Lock()
			n := len(s.prompts)
			s.mu.Unlock()
			if n == 0 {
				return false
			}
		}
		return true
	}, "all four workers to finish")
}

func TestDispatcherSameCardEventsSerialised(t *testing.T) {
	factory := newFakeFactory()
	factory.delay = 30 * time.Millisecond
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	const cardID = "card-x"
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"` + cardID + `"},"listAfter":{"name":"Analyze"}}}}`
	for i := 0; i < 5; i++ {
		dispatchEvent(t, d, "evt", body)
	}

	waitFor(t, 2*time.Second, func() bool {
		s := factory.get(cardID)
		if s == nil {
			return false
		}
		s.mu.Lock()
		n := len(s.prompts)
		s.mu.Unlock()
		return n == 5
	}, "all 5 prompts delivered")

	s := factory.get(cardID)
	if max := atomic.LoadInt32(&s.maxConcurrent); max != 1 {
		t.Fatalf("expected strict serialisation, max concurrent = %d", max)
	}

	// Only one session should have been created for this card.
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(factory.sessions))
	}
}

func TestDispatcherDropsUnsupportedEvents(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	// updateCard without a list move = drop.
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"card1"}}}}`
	dispatchEvent(t, d, "evt-1", body)

	// Give the dispatcher a moment to (incorrectly) create a session if it
	// were going to.
	time.Sleep(50 * time.Millisecond)

	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.sessions) != 0 {
		t.Fatalf("expected no sessions created for dropped event, got %d", len(factory.sessions))
	}
}

func TestDispatcherDepartureWithoutWorkerIsNoop(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	// First event for the card is a departure (move to a non-active list).
	// MANAGER.md rule 1: drop, do NOT spawn a worker.
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"card1"},"listAfter":{"name":"Ready for review"}}}}`
	dispatchEvent(t, d, "evt-1", body)

	time.Sleep(50 * time.Millisecond)

	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.sessions) != 0 {
		t.Fatalf("expected no session for first-seen departure, got %d", len(factory.sessions))
	}
}

func TestDispatcherTerminateNotifiesAndShutsDownWorker(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	const cardID = "card-y"
	dispatch := `{"action":{"type":"updateCard","data":{"card":{"id":"` + cardID + `"},"listAfter":{"name":"Analyze"}}}}`
	terminate := `{"action":{"type":"updateCard","data":{"card":{"id":"` + cardID + `"},"listAfter":{"name":"Done"}}}}`
	dispatchEvent(t, d, "evt-go", dispatch)
	dispatchEvent(t, d, "evt-done", terminate)

	// Worker should process both, then deregister itself.
	waitFor(t, 2*time.Second, func() bool {
		s := factory.get(cardID)
		if s == nil {
			return false
		}
		s.mu.Lock()
		n := len(s.prompts)
		s.mu.Unlock()
		return n == 2
	}, "worker to process both events")

	waitFor(t, 2*time.Second, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.workers[cardID]
		return !ok
	}, "worker to deregister after terminate")

	// A subsequent dispatch for the same card should spawn a brand-new
	// worker (and a brand-new fake session).
	dispatchEvent(t, d, "evt-revived", dispatch)
	waitFor(t, 2*time.Second, func() bool {
		factory.mu.Lock()
		defer factory.mu.Unlock()
		return len(factory.createOrder) == 2 && factory.createOrder[1] == cardID
	}, "second worker session for revived card")
}

func TestDispatcherTerminateWithoutWorkerIsNoop(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	body := `{"action":{"type":"deleteCard","data":{"card":{"id":"never-seen-card"}}}}`
	dispatchEvent(t, d, "evt", body)
	time.Sleep(50 * time.Millisecond)

	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.sessions) != 0 {
		t.Fatalf("expected no session for terminate-without-worker, got %d", len(factory.sessions))
	}
}

func TestDispatcherStopAfterStopReturnsError(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	d.Stop()

	body := `{"action":{"type":"updateCard","data":{"card":{"id":"card-c"},"listAfter":{"name":"Analyze"}}}}`
	err := d.Dispatch(context.Background(), "evt", []byte(body))
	if !errors.Is(err, ErrDispatcherStopped) {
		t.Fatalf("expected ErrDispatcherStopped, got %v", err)
	}
}

func TestDispatcherStopCancelsInflightSend(t *testing.T) {
	session := newCancelBlockingSession()
	d := NewDispatcher(sysevent.Default(), SessionFactoryFunc(func(context.Context, string, *ActivityTracker) (WorkerSession, error) {
		return session, nil
	}))
	defer d.Stop()

	const cardID = "stop-card"
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"` + cardID + `"},"listAfter":{"name":"Analyze"}}}}`
	dispatchEvent(t, d, "evt", body)

	select {
	case <-session.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for send to be in flight")
	}

	stopped := make(chan struct{})
	go func() {
		d.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop timed out waiting for in-flight send to cancel")
	}

	select {
	case <-session.ctxDone:
	default:
		t.Fatal("SendAndWait did not observe ctx.Done")
	}
}

func TestDispatcherSessionCreationFailureDoesNotPanic(t *testing.T) {
	factory := newFakeFactory()
	factory.createErr = errors.New("create boom")
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	body := `{"action":{"type":"updateCard","data":{"card":{"id":"card-c"},"listAfter":{"name":"Analyze"}}}}`
	dispatchEvent(t, d, "evt", body)

	// Worker goroutine should give up on the failed creation and
	// deregister so the next event can try again.
	waitFor(t, 2*time.Second, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.workers["card-c"]
		return !ok
	}, "failed worker to deregister")
}

func TestDispatcherIdleTimeoutDisconnectsSession(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	d.idleTimeout = 100 * time.Millisecond
	defer d.Stop()

	const cardID = "idle-card"
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"` + cardID + `"},"listAfter":{"name":"Analyze"}}}}`
	dispatchEvent(t, d, "evt-1", body)

	// Wait for the first event to be processed.
	waitFor(t, 2*time.Second, func() bool {
		s := factory.get(cardID)
		if s == nil {
			return false
		}
		s.mu.Lock()
		n := len(s.prompts)
		s.mu.Unlock()
		return n == 1
	}, "first event processed")

	firstSession := factory.get(cardID)

	// Wait for idle timeout to fire and disconnect the session.
	waitFor(t, 2*time.Second, func() bool {
		return atomic.LoadInt32(&firstSession.disconnectCount) > 0
	}, "session disconnected by idle timeout")

	// Worker handle should still be registered (goroutine alive).
	d.mu.Lock()
	_, alive := d.workers[cardID]
	d.mu.Unlock()
	if !alive {
		t.Fatal("worker should still be registered after idle timeout")
	}
}

func TestDispatcherIdleTimeoutWorkerAcceptsNewEvents(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	d.idleTimeout = 100 * time.Millisecond
	defer d.Stop()

	const cardID = "reuse-card"
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"` + cardID + `"},"listAfter":{"name":"Analyze"}}}}`
	dispatchEvent(t, d, "evt-1", body)

	waitFor(t, 2*time.Second, func() bool {
		s := factory.get(cardID)
		if s == nil {
			return false
		}
		s.mu.Lock()
		n := len(s.prompts)
		s.mu.Unlock()
		return n == 1
	}, "first event processed")

	firstSession := factory.get(cardID)

	// Wait for idle reap.
	waitFor(t, 2*time.Second, func() bool {
		return atomic.LoadInt32(&firstSession.disconnectCount) > 0
	}, "first session reaped")

	// Send another event — should lazily create a second session.
	dispatchEvent(t, d, "evt-2", body)
	waitFor(t, 2*time.Second, func() bool {
		factory.mu.Lock()
		defer factory.mu.Unlock()
		return len(factory.createOrder) == 2
	}, "second session created after idle reap")

	secondSession := factory.get(cardID)
	waitFor(t, 2*time.Second, func() bool {
		secondSession.mu.Lock()
		n := len(secondSession.prompts)
		secondSession.mu.Unlock()
		return n == 1
	}, "second event processed on new session")
}

func TestDispatcherDeleteWorkerNotFound(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	if _, err := d.DeleteWorker("nope"); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("expected ErrWorkerNotFound, got %v", err)
	}
}

func TestDispatcherDeleteWorkerRemovesWorkdirAndDeregisters(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	const cardID = "del-card"
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"` + cardID + `"},"listAfter":{"name":"Analyze"}}}}`
	dispatchEvent(t, d, "evt", body)

	// Wait for the worker to land its first prompt so the goroutine and
	// session are fully spun up before we attempt the delete.
	waitFor(t, 2*time.Second, func() bool {
		s := factory.get(cardID)
		if s == nil {
			return false
		}
		s.mu.Lock()
		n := len(s.prompts)
		s.mu.Unlock()
		return n == 1
	}, "first prompt processed")

	// Plant a fake work_dir so DeleteWorker has something to remove.
	tmp := t.TempDir()
	workDir := tmp + string(os.PathSeparator) + "wd-" + cardID
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	d.mu.Lock()
	d.workers[cardID].tracker.SetWorkDir(workDir)
	d.mu.Unlock()

	removed, err := d.DeleteWorker(cardID)
	if err != nil {
		t.Fatalf("DeleteWorker: %v", err)
	}
	if removed != workDir {
		t.Fatalf("expected removed=%q, got %q", workDir, removed)
	}
	if _, statErr := os.Stat(workDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected work_dir gone, stat err = %v", statErr)
	}

	// Worker must be deregistered.
	d.mu.Lock()
	_, stillRegistered := d.workers[cardID]
	d.mu.Unlock()
	if stillRegistered {
		t.Fatalf("worker still registered after DeleteWorker")
	}

	// Session must have been disconnected.
	s := factory.get(cardID)
	if s == nil {
		t.Fatalf("session missing")
	}
	if got := atomic.LoadInt32(&s.disconnectCount); got == 0 {
		t.Fatalf("expected session disconnected, got count=%d", got)
	}

	// A subsequent dispatch for the same card spawns a fresh worker.
	dispatchEvent(t, d, "evt-2", body)
	waitFor(t, 2*time.Second, func() bool {
		factory.mu.Lock()
		defer factory.mu.Unlock()
		return len(factory.createOrder) == 2
	}, "fresh worker after delete")
}

func TestDispatcherDeleteWorkerCancelsInflightSend(t *testing.T) {
	factory := newFakeFactory()
	// Long delay so SendAndWait blocks; DeleteWorker must cancel the ctx
	// passed into it so the goroutine returns promptly.
	factory.delay = 5 * time.Second
	d := NewDispatcher(sysevent.Default(), factory)
	defer d.Stop()

	const cardID = "kill-card"
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"` + cardID + `"},"listAfter":{"name":"Analyze"}}}}`
	dispatchEvent(t, d, "evt", body)

	// Wait until the worker has actually entered SendAndWait.
	waitFor(t, 2*time.Second, func() bool {
		s := factory.get(cardID)
		if s == nil {
			return false
		}
		return atomic.LoadInt32(&s.concurrent) == 1
	}, "send to be in flight")

	start := time.Now()
	if _, err := d.DeleteWorker(cardID); err != nil {
		t.Fatalf("DeleteWorker: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("DeleteWorker took %s — expected fast cancellation", d)
	}
}
