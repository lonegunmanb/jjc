package app

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	delay      time.Duration
	mu         sync.Mutex
	sessions   map[string]*fakeSession
	createOrder []string
	createErr  error
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

func TestDispatcherDifferentCardsRunInParallel(t *testing.T) {
	factory := newFakeFactory()
	factory.delay = 80 * time.Millisecond
	d := NewDispatcher(log.Default(), factory)
	defer d.Stop()

	cards := []string{"a", "b", "c", "d"}
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
	d := NewDispatcher(log.Default(), factory)
	defer d.Stop()

	const cardID = "x"
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
	d := NewDispatcher(log.Default(), factory)
	defer d.Stop()

	// updateCard without a list move = drop.
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"c1"}}}}`
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
	d := NewDispatcher(log.Default(), factory)
	defer d.Stop()

	// First event for the card is a departure (move to a non-active list).
	// MANAGER.md rule 1: drop, do NOT spawn a worker.
	body := `{"action":{"type":"updateCard","data":{"card":{"id":"c1"},"listAfter":{"name":"Ready for review"}}}}`
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
	d := NewDispatcher(log.Default(), factory)
	defer d.Stop()

	const cardID = "y"
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
	d := NewDispatcher(log.Default(), factory)
	defer d.Stop()

	body := `{"action":{"type":"deleteCard","data":{"card":{"id":"never-seen"}}}}`
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
	d := NewDispatcher(log.Default(), factory)
	d.Stop()

	body := `{"action":{"type":"updateCard","data":{"card":{"id":"c"},"listAfter":{"name":"Analyze"}}}}`
	err := d.Dispatch(context.Background(), "evt", []byte(body))
	if !errors.Is(err, ErrDispatcherStopped) {
		t.Fatalf("expected ErrDispatcherStopped, got %v", err)
	}
}

func TestDispatcherSessionCreationFailureDoesNotPanic(t *testing.T) {
	factory := newFakeFactory()
	factory.createErr = errors.New("create boom")
	d := NewDispatcher(log.Default(), factory)
	defer d.Stop()

	body := `{"action":{"type":"updateCard","data":{"card":{"id":"c"},"listAfter":{"name":"Analyze"}}}}`
	dispatchEvent(t, d, "evt", body)

	// Worker goroutine should give up on the failed creation and
	// deregister so the next event can try again.
	waitFor(t, 2*time.Second, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.workers["c"]
		return !ok
	}, "failed worker to deregister")
}

func TestDispatcherIdleTimeoutDisconnectsSession(t *testing.T) {
	factory := newFakeFactory()
	d := NewDispatcher(log.Default(), factory)
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
	d := NewDispatcher(log.Default(), factory)
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
