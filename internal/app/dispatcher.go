package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/lonegunmanb/jjc/internal/app/kanban"
	"github.com/lonegunmanb/jjc/internal/app/router"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

// WorkerSession is the subset of *copilot.Session that the dispatcher
// uses. It exists so tests can substitute a fake session without bringing
// up the real Copilot CLI process.
type WorkerSession interface {
	// SendAndWait sends a single user message and blocks until the session
	// goes idle (i.e. the worker finished the turn, including any tool
	// calls the model made along the way).
	SendAndWait(ctx context.Context, prompt string) error
	Disconnect() error
}

// SessionFactory creates a fresh worker session for one card. Implementations
// must return a session whose system prompt has already been seeded with
// WORKER.md plus per-card metadata (card_id, work_dir, work_type, ...).
// The provided tracker MUST be fed every SDK SessionEvent the session
// emits so the dispatcher can serve REPL queries about the worker.
type SessionFactory interface {
	NewWorkerSession(ctx context.Context, cardID string, tracker *ActivityTracker) (WorkerSession, error)
}

// SessionFactoryFunc adapts a plain function to the SessionFactory
// interface, mirroring the http.HandlerFunc pattern.
type SessionFactoryFunc func(ctx context.Context, cardID string, tracker *ActivityTracker) (WorkerSession, error)

// NewWorkerSession implements SessionFactory.
func (f SessionFactoryFunc) NewWorkerSession(ctx context.Context, cardID string, tracker *ActivityTracker) (WorkerSession, error) {
	return f(ctx, cardID, tracker)
}

// dispatchKind is the internal queue payload tag.
type dispatchKind int

const (
	kindEvent dispatchKind = iota
	kindTerminate
)

// dispatchMessage is a single item enqueued to a worker's inbox.
type dispatchMessage struct {
	kind    dispatchKind
	eventID string
	prompt  string
}

// workerHandle is the per-card record kept by the Dispatcher.
type workerHandle struct {
	cardID  string
	inbox   chan dispatchMessage
	tracker *ActivityTracker
	// done is closed after the worker goroutine exits and the session has
	// been disconnected. Used by Stop to wait for orderly shutdown.
	done chan struct{}
	// ctx is cancelled by DeleteWorker so an in-flight SendAndWait (which
	// can otherwise block for the model's full think+tool cycle) returns
	// promptly with ctx.Err().
	ctx    context.Context
	cancel context.CancelFunc
	// kill is closed by DeleteWorker to ask the worker goroutine to exit
	// immediately without draining any remaining inbox messages.
	kill chan struct{}
}

// DefaultIdleTimeout is how long a worker session may sit idle before the
// dispatcher disconnects it to free resources. The worker goroutine stays
// alive; a subsequent event lazily creates a fresh session.
const DefaultIdleTimeout = 24 * time.Hour

// Dispatcher routes Trello webhook events to per-card worker sessions.
// Each card gets its own *copilot.Session and a single goroutine that
// drains an inbox channel: this guarantees same-card events are delivered
// strictly in order while different cards are processed in parallel.
type Dispatcher struct {
	logger  sysevent.Sink
	factory SessionFactory
	// inboxBuf is the per-card inbox channel buffer size. Bounded so a
	// runaway worker cannot accumulate unbounded backlog; events past the
	// buffer block the dispatch goroutine until the worker catches up.
	inboxBuf int
	// idleTimeout is the duration after which an idle worker's session is
	// disconnected. Zero disables the timeout.
	idleTimeout time.Duration

	globalLog *GlobalEventLog

	// kanbanView is the resolved view produced at startup by
	// internal/app/kanban.LoadAndResolve. It carries the per-role
	// list IDs, per-category list-id sets, the agent-comment prefix
	// list, and any unclaimed-list metadata. The runner reads it via
	// KanbanView() to build the per-card CARD CONTEXT block. The
	// dispatcher itself no longer consults the view directly — every
	// routing decision now flows through the HCL route engine
	// (routeEngine, below). Nil is tolerated so unit tests that don't
	// talk to Trello keep working: the default engine the dispatcher
	// lazy-builds carries router.DefaultLegacyKanbanView, which uses
	// the hard-coded "Analyze" / "In action" / "Done" / "[agent]:"
	// names the legacy switch encoded.
	kanbanView *kanban.Resolved

	// routeEngine is the HCL `route {}` evaluator. SetRouteEngine
	// installs the operator-loaded engine built from
	// <router-dir>/router.hcl + the resolved kanban view; tests that
	// skip wiring observe a lazy default built from
	// router.MustNewDefaultEngine() instead.
	routeEngine     *router.Engine
	routeEngineOnce sync.Once

	mu      sync.Mutex
	workers map[string]*workerHandle
	stopped bool
}

// NewDispatcher returns a Dispatcher ready to accept events. factory is
// called the first time an event for a card needs to be dispatched.
func NewDispatcher(logger sysevent.Sink, factory SessionFactory) *Dispatcher {
	if logger == nil {
		logger = sysevent.Default()
	}
	return &Dispatcher{
		logger:      logger,
		factory:     factory,
		inboxBuf:    64,
		idleTimeout: DefaultIdleTimeout,
		workers:     make(map[string]*workerHandle),
	}
}

// ErrDispatcherStopped is returned by Dispatch after Stop has been called.
var ErrDispatcherStopped = errors.New("dispatcher stopped")

// ErrWorkerNotFound is returned by DeleteWorker when no worker is
// currently registered for the given card.
var ErrWorkerNotFound = errors.New("no active worker for card")

// ErrWorkerDeleted is returned by Dispatch when an enqueue races with a
// DeleteWorker call that tears the worker down before the message lands
// in the inbox.
var ErrWorkerDeleted = errors.New("worker deleted")

// SetGlobalLog attaches a GlobalEventLog for TUI display. If set, key
// lifecycle events (routing decisions, worker registration/deregistration,
// idle reap) are recorded. Safe to call before any Dispatch.
func (d *Dispatcher) SetGlobalLog(g *GlobalEventLog) {
	d.globalLog = g
}

// SetKanbanView installs the resolved kanban view that the runner
// consults when building each per-card CARD CONTEXT block. Call once
// at startup, before the first Dispatch. Passing nil leaves the runner
// in legacy mode (no per-role IDs injected into CARD CONTEXT) — only
// useful for tests that don't stand up the Trello-side resolution.
func (d *Dispatcher) SetKanbanView(view *kanban.Resolved) {
	d.kanbanView = view
}

// SetRouteEngine installs the HCL-driven route engine. Call once at
// startup, before the first Dispatch. When unset, Dispatch lazily
// builds router.MustNewDefaultEngine() so tests that never wire an
// engine keep observing the legacy hard-coded routing semantics.
func (d *Dispatcher) SetRouteEngine(e *router.Engine) {
	d.routeEngine = e
}

// KanbanView returns the resolved view installed via SetKanbanView,
// or nil if none has been installed. Exposed for the runner so the
// per-card CARD CONTEXT renderer can inline the resolved list IDs
// without duplicating the wiring.
func (d *Dispatcher) KanbanView() *kanban.Resolved {
	return d.kanbanView
}

func (d *Dispatcher) recordGlobal(cardID, kind, summary string) {
	if d.globalLog != nil {
		d.globalLog.Record(cardID, kind, summary)
	}
}

// evaluateRoute runs the configured (or lazy-built default) route
// engine against the raw Trello payload and translates the engine's
// string `Do` token into the dispatcher's RouteAction enum.
//
// The dispatcher extracts the action.list_after / action.list_after_id
// and action.list_name / action.list_id values from the raw payload,
// plus the gateway's defence-in-depth IsValidCardID check, and feeds
// them to the engine as the router.Event fields.
//
// `ListAfter` is preserved on the returned RouteDecision so existing
// logs and prompt templates (assembleDepartureNotice, ...) keep
// surfacing the human-readable list name.
func (d *Dispatcher) evaluateRoute(rawBody []byte) RouteDecision {
	root := parseRawBody(rawBody)
	action := asMap(root["action"])
	actionType := asString(action["type"])
	data := asMap(action["data"])
	card := asMap(data["card"])
	cardID := asString(card["id"])
	listAfterMap := asMap(data["listAfter"])
	listAfter := asString(listAfterMap["name"])
	listAfterID := asString(listAfterMap["id"])
	listMap := asMap(data["list"])
	listName := asString(listMap["name"])
	listID := asString(listMap["id"])
	text := asString(data["text"])

	ev := router.Event{
		Type:        actionType,
		CardID:      cardID,
		CardIDValid: cardID != "" && IsValidCardID(cardID),
		ListAfter:   listAfter,
		ListAfterID: listAfterID,
		ListName:    listName,
		ListID:      listID,
		Comment:     text,
	}

	dec := d.engine().Evaluate(ev)

	out := RouteDecision{
		Action:    routeActionFromDo(dec.Do),
		CardID:    cardID,
		ListAfter: listAfter,
		Reason:    dec.Reason,
	}
	// The legacy switch suppressed the CardID on no-card-id and
	// invalid-card-id drops so the log line stays clean. The engine
	// returns the raw card id; mirror the legacy elision here.
	if dec.Reason == "no_card_id" || dec.Reason == "invalid_card_id" {
		out.CardID = ""
	}
	return out
}

// engine returns the route engine the dispatcher will evaluate
// against, lazy-building router.MustNewDefaultEngine() on first use
// when no operator-provided engine has been installed via
// SetRouteEngine. This keeps unit tests that never go through main.go
// working without forcing them to wire an engine of their own.
func (d *Dispatcher) engine() *router.Engine {
	d.routeEngineOnce.Do(func() {
		if d.routeEngine == nil {
			d.routeEngine = router.MustNewDefaultEngine()
		}
	})
	return d.routeEngine
}

// routeActionFromDo maps a router.Engine `Do` token to the
// dispatcher's RouteAction enum. Unknown tokens collapse to RouteDrop
// so a misconfigured engine never crashes the dispatch path.
func routeActionFromDo(do string) RouteAction {
	switch do {
	case router.ActionDispatch:
		return RouteDispatch
	case router.ActionTerminate:
		return RouteTerminate
	case router.ActionNotifyDeparture:
		return RouteNotifyDeparture
	default:
		return RouteDrop
	}
}

// Dispatch decides what to do with a Trello event and either pushes it to
// the right per-card worker, terminates a worker, or drops the event.
//
// It is safe to call Dispatch from many goroutines concurrently. Dispatch
// returns once the event has been ENQUEUED (or a routing decision has been
// made not to enqueue) — it does NOT wait for the worker to finish
// processing it. This is what lets us reply 202 to Trello immediately.
func (d *Dispatcher) Dispatch(ctx context.Context, eventID string, rawBody []byte) error {
	decision := d.evaluateRoute(rawBody)
	sysevent.Emitf(d.logger, "route_decided", "event_id=%s action=%s card_id=%s list_after=%q reason=%s",
		eventID, decision.Action, decision.CardID, decision.ListAfter, decision.Reason)
	d.recordGlobal(decision.CardID, decision.Action.String(), decision.ListAfter+" "+decision.Reason)

	switch decision.Action {
	case RouteDrop:
		return nil

	case RouteDispatch:
		prompt := assembleEventPrompt(rawBody, mustSlim(rawBody))
		return d.enqueue(ctx, decision.CardID, dispatchMessage{
			kind:    kindEvent,
			eventID: eventID,
			prompt:  prompt,
		})

	case RouteNotifyDeparture:
		// Only notify if a worker exists; do NOT spawn one for departures.
		d.mu.Lock()
		_, ok := d.workers[decision.CardID]
		d.mu.Unlock()
		if !ok {
			sysevent.Emitf(d.logger, "departure_no_worker", "event_id=%s card_id=%s list_after=%q",
				eventID, decision.CardID, decision.ListAfter)
			return nil
		}
		prompt := assembleDepartureNotice(rawBody, mustSlim(rawBody), decision.ListAfter)
		return d.enqueue(ctx, decision.CardID, dispatchMessage{
			kind:    kindEvent,
			eventID: eventID,
			prompt:  prompt,
		})

	case RouteTerminate:
		d.mu.Lock()
		_, ok := d.workers[decision.CardID]
		d.mu.Unlock()
		if !ok {
			sysevent.Emitf(d.logger, "terminate_no_worker", "event_id=%s card_id=%s reason=%s",
				eventID, decision.CardID, decision.Reason)
			return nil
		}
		prompt := assembleTerminateNotice(rawBody, mustSlim(rawBody), decision.Reason)
		return d.enqueue(ctx, decision.CardID, dispatchMessage{
			kind:    kindTerminate,
			eventID: eventID,
			prompt:  prompt,
		})
	}

	return nil
}

// enqueue obtains (or creates) the per-card worker handle and pushes one
// message to its inbox.
func (d *Dispatcher) enqueue(ctx context.Context, cardID string, msg dispatchMessage) error {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return ErrDispatcherStopped
	}
	handle, ok := d.workers[cardID]
	if !ok {
		wctx, wcancel := context.WithCancel(context.Background())
		handle = &workerHandle{
			cardID:  cardID,
			inbox:   make(chan dispatchMessage, d.inboxBuf),
			tracker: NewActivityTracker(cardID, 64),
			done:    make(chan struct{}),
			ctx:     wctx,
			cancel:  wcancel,
			kill:    make(chan struct{}),
		}
		d.workers[cardID] = handle
		go d.runWorker(handle)
		sysevent.Emitf(d.logger, "worker_registered", "card_id=%s", cardID)
		d.recordGlobal(cardID, "worker_registered", "")
	}
	d.mu.Unlock()

	select {
	case handle.inbox <- msg:
		sysevent.Emitf(d.logger, "worker_enqueued", "card_id=%s event_id=%s kind=%d", cardID, msg.eventID, msg.kind)
		return nil
	case <-handle.kill:
		return ErrWorkerDeleted
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runWorker is the per-card serialiser. It lazily creates the session on
// the first message, processes messages strictly in arrival order, and
// disconnects when the inbox is closed (terminate) or drained at shutdown.
func (d *Dispatcher) runWorker(handle *workerHandle) {
	defer close(handle.done)
	defer d.deregister(handle.cardID)
	// Remove the per-worker activity log file once everything else has
	// shut down; runs after MarkDisconnected has appended its final entry.
	defer func() {
		if err := handle.tracker.Close(); err != nil {
			sysevent.Emitf(d.logger, "activity_log_remove_error", "card_id=%s err=%v", handle.cardID, err)
		}
	}()

	var session WorkerSession
	defer func() {
		if session != nil {
			if err := session.Disconnect(); err != nil {
				sysevent.Emitf(d.logger, "worker_disconnect_error", "card_id=%s err=%v", handle.cardID, err)
			} else {
				sysevent.Emitf(d.logger, "worker_disconnected", "card_id=%s", handle.cardID)
			}
		}
		handle.tracker.MarkDisconnected()
	}()

	for {
		// Wait for the next message, with an idle timeout that disconnects
		// the session (but keeps the goroutine alive) to free resources.
		var msg dispatchMessage
		var ok bool

		if session != nil && d.idleTimeout > 0 {
			timer := time.NewTimer(d.idleTimeout)
			select {
			case msg, ok = <-handle.inbox:
				timer.Stop()
			case <-handle.kill:
				timer.Stop()
				return
			case <-timer.C:
				sysevent.Emitf(d.logger, "worker_session_idle_reaped", "card_id=%s timeout=%s",
					handle.cardID, d.idleTimeout)
				d.recordGlobal(handle.cardID, "session_reaped", "idle timeout")
				if err := session.Disconnect(); err != nil {
					sysevent.Emitf(d.logger, "worker_idle_disconnect_error", "card_id=%s err=%v",
						handle.cardID, err)
				}
				session = nil
				handle.tracker.MarkSessionIdleReaped()
				continue
			}
		} else {
			select {
			case msg, ok = <-handle.inbox:
			case <-handle.kill:
				return
			}
		}

		if !ok {
			return // inbox closed (shutdown)
		}

		if session == nil {
			sysevent.Emitf(d.logger, "worker_session_creating", "card_id=%s event_id=%s", handle.cardID, msg.eventID)
			created, err := d.factory.NewWorkerSession(context.Background(), handle.cardID, handle.tracker)
			if err != nil {
				sysevent.Emitf(d.logger, "worker_session_create_error", "card_id=%s event_id=%s err=%v",
					handle.cardID, msg.eventID, err)
				// Surface the failure to the TUI/REPL global log so an
				// operator notices something went wrong (the worker
				// goroutine deregisters via the deferred deregister
				// below, so a future event for the same card will spawn
				// a fresh attempt — but without this notification a
				// silently-failed worker is invisible until the next
				// event arrives).
				d.recordGlobal(handle.cardID, "worker_session_create_failed",
					fmt.Sprintf("event_id=%s err=%v", msg.eventID, err))
				// Drop this message and any backlog so we don't pile up
				// failures. Future events for this card will trigger a
				// fresh worker (and a fresh attempt) once the old handle
				// deregisters.
				return
			}
			session = created
			sysevent.Emitf(d.logger, "worker_session_created", "card_id=%s event_id=%s", handle.cardID, msg.eventID)
		}

		started := time.Now()
		sysevent.Emitf(d.logger, "worker_send_start", "card_id=%s event_id=%s kind=%d prompt_bytes=%d",
			handle.cardID, msg.eventID, msg.kind, len(msg.prompt))
		if err := session.SendAndWait(handle.ctx, msg.prompt); err != nil {
			sysevent.Emitf(d.logger, "worker_send_error", "card_id=%s event_id=%s err=%v",
				handle.cardID, msg.eventID, err)
			// If the worker was killed mid-send, exit instead of looping
			// fast on ctx errors.
			if handle.ctx.Err() != nil {
				return
			}
			// Don't tear the worker down on a single send error; the next
			// event may succeed. A persistent failure will surface as
			// repeated send errors in the logs.
			continue
		}
		sysevent.Emitf(d.logger, "worker_send_done", "card_id=%s event_id=%s duration=%s",
			handle.cardID, msg.eventID, time.Since(started))

		if msg.kind == kindTerminate {
			sysevent.Emitf(d.logger, "worker_terminating", "card_id=%s event_id=%s", handle.cardID, msg.eventID)
			d.recordGlobal(handle.cardID, "worker_terminating", msg.eventID)
			return
		}
	}
}

// deregister removes a worker handle from the registry so a subsequent
// event for the same card spawns a fresh worker.
func (d *Dispatcher) deregister(cardID string) {
	d.mu.Lock()
	delete(d.workers, cardID)
	d.mu.Unlock()
	sysevent.Emitf(d.logger, "worker_deregistered", "card_id=%s", cardID)
	d.recordGlobal(cardID, "worker_deregistered", "")
}

// Stop signals every per-card worker to exit and waits for each
// goroutine to disconnect cleanly. New Dispatch calls after Stop return
// ErrDispatcherStopped.
//
// Lifecycle invariant: only the per-card runWorker goroutine is allowed
// to touch its own inbox channel beyond enqueueing. Stop closes each
// worker's `kill` channel, which makes:
//
//   - any in-flight enqueue racing with Stop fall into the `<-kill` branch
//     of its `select` and return ErrWorkerDeleted instead of panicking on
//     a send to a closed channel; and
//   - the worker goroutine wake from its inbox/idle-timer select, return,
//     and disconnect the session.
//
// We deliberately do NOT close the inbox channel from here — doing so
// would race with concurrent enqueues and panic.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	handles := make([]*workerHandle, 0, len(d.workers))
	for _, h := range d.workers {
		handles = append(handles, h)
	}
	d.mu.Unlock()

	for _, h := range handles {
		close(h.kill)
	}
	for _, h := range handles {
		<-h.done
	}
	sysevent.Emitf(d.logger, "dispatcher_stopped", "workers=%d", len(handles))
}

// DeleteWorker forcibly tears down the worker for cardID and removes the
// per-card local work_dir from disk. It is the operator-initiated kill
// switch invoked from the TUI:
//
//  1. The handle is removed from the registry under the dispatcher mutex,
//     so any subsequent Dispatch for the same card spawns a fresh worker.
//  2. The kill channel is closed so the worker goroutine exits without
//     draining any remaining queued messages.
//  3. The handle's context is cancelled so an in-flight SendAndWait
//     returns promptly instead of blocking on the model's full
//     think+tool cycle.
//  4. After the goroutine has finished disconnecting the session and
//     closing its activity log, os.RemoveAll wipes the work_dir.
//
// Returns the work_dir path that was removed (empty if the worker had not
// recorded one yet) and any error encountered during the rmdir.
// Returns ErrWorkerNotFound when no worker is registered for cardID.
func (d *Dispatcher) DeleteWorker(cardID string) (string, error) {
	d.mu.Lock()
	h, ok := d.workers[cardID]
	if !ok {
		d.mu.Unlock()
		return "", ErrWorkerNotFound
	}
	delete(d.workers, cardID)
	d.mu.Unlock()

	// Snapshot work_dir before tearing the worker down — the tracker is
	// still readable here, but the runWorker defer chain may close the
	// log file by the time we get a chance to read it again.
	st, _ := h.tracker.Snapshot()
	workDir := st.WorkDir

	// Close kill first so the goroutine wakes from any blocking inbox
	// read; cancel ctx so SendAndWait (if mid-flight) returns promptly.
	close(h.kill)
	h.cancel()
	<-h.done

	sysevent.Emitf(d.logger, "worker_deleted", "card_id=%s work_dir=%q", cardID, workDir)
	d.recordGlobal(cardID, "worker_deleted", workDir)

	if workDir == "" {
		return "", nil
	}
	if err := os.RemoveAll(workDir); err != nil {
		sysevent.Emitf(d.logger, "worker_workdir_remove_error", "card_id=%s work_dir=%q err=%v",
			cardID, workDir, err)
		return workDir, fmt.Errorf("remove work_dir %s: %w", workDir, err)
	}
	sysevent.Emitf(d.logger, "worker_workdir_removed", "card_id=%s work_dir=%q", cardID, workDir)
	return workDir, nil
}

// mustSlim slims rawBody, returning an empty payload on error rather than
// panicking — slim failures are logged elsewhere and should not block
// dispatch.
func mustSlim(rawBody []byte) []byte {
	slim, err := slimRawBody(rawBody)
	if err != nil {
		return []byte("{}")
	}
	return slim
}

// assembleDepartureNotice builds a per-event prompt asking the worker to
// wind down because the card moved away from the active lists. The wording
// mirrors MANAGER.md rule 6.
func assembleDepartureNotice(rawBody, slimBody []byte, listAfter string) string {
	var b strings.Builder
	b.WriteString("# TASK\n\n")
	b.WriteString("The card has moved out of the active lists (Analyze / In action) ")
	fmt.Fprintf(&b, "and into %q. ", listAfter)
	b.WriteString("Per the worker contract: if you were executing a planned implementation, ")
	b.WriteString("stop immediately. If you are mid-experiment, you may complete the current ")
	b.WriteString("experiment to a clean state but do not start any new ones. Make sure all ")
	b.WriteString("provisioned experiment resources are torn down before you idle.\n\n")
	b.WriteString("## Human-readable summary\n\n")
	b.WriteString(BuildPromptSummary(rawBody))
	b.WriteString("\n\n## Slimmed event payload (JSON)\n\n```json\n")
	b.Write(slimBody)
	b.WriteString("\n```\n")
	return b.String()
}

// assembleTerminateNotice builds the final user message sent to a worker
// before its session is disconnected. The worker is asked to clean up
// experiment resources, delete its work directory, and finish.
func assembleTerminateNotice(rawBody, slimBody []byte, reason string) string {
	var b strings.Builder
	b.WriteString("# TASK (FINAL)\n\n")
	b.WriteString("This card has reached a terminal state (")
	b.WriteString(reason)
	b.WriteString("). This is your final user turn. Do the following before responding:\n\n")
	b.WriteString("1. Stop any in-flight work that is not strictly cleanup.\n")
	b.WriteString("2. Tear down every cloud / local experiment resource you provisioned.\n")
	b.WriteString("3. Delete your work directory at `C:\\project\\<card_id>` (use the card_id ")
	b.WriteString("from your system prompt).\n")
	b.WriteString("4. Reply with a short summary of what you cleaned up. After this turn, ")
	b.WriteString("your session will be disconnected.\n\n")
	b.WriteString("## Human-readable summary\n\n")
	b.WriteString(BuildPromptSummary(rawBody))
	b.WriteString("\n\n## Slimmed event payload (JSON)\n\n```json\n")
	b.Write(slimBody)
	b.WriteString("\n```\n")
	return b.String()
}

// copilotWorkerSession adapts a real *copilot.Session to the WorkerSession
// interface used by the dispatcher. It implements SendAndWait by sending
// the prompt and blocking on a SessionIdleData event.
type copilotWorkerSession struct {
	session *copilot.Session
	logger  sysevent.Sink
	cardID  string
	tracker *ActivityTracker
}

func (w *copilotWorkerSession) SendAndWait(ctx context.Context, prompt string) error {
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	var sErrMu sync.Mutex
	var sawErr bool
	var lastErr string

	unsubscribe := w.session.On(func(event copilot.SessionEvent) {
		logSessionEvent(w.logger, w.cardID, event)
		if w.tracker != nil {
			w.tracker.RecordEvent(event)
		}
		switch d := event.Data.(type) {
		case *copilot.SessionIdleData:
			closeDone()
		case *copilot.SessionErrorData:
			sErrMu.Lock()
			sawErr = true
			lastErr = d.Message
			sErrMu.Unlock()
		}
	})
	defer unsubscribe()

	if _, err := w.session.Send(ctx, copilot.MessageOptions{Prompt: prompt}); err != nil {
		return err
	}

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	sErrMu.Lock()
	defer sErrMu.Unlock()
	if sawErr {
		return fmt.Errorf("session reported error: %s", lastErr)
	}
	return nil
}

func (w *copilotWorkerSession) Disconnect() error {
	if w.session == nil {
		return nil
	}
	return w.session.Disconnect()
}

// Snapshot returns the worker status + recent activity for a card. ok=false
// if no worker is registered for that card.
func (d *Dispatcher) Snapshot(cardID string) (WorkerStatus, []ActivityEntry, bool) {
	d.mu.Lock()
	h, ok := d.workers[cardID]
	if !ok {
		d.mu.Unlock()
		return WorkerStatus{}, nil, false
	}
	// Read the inbox depth while still holding d.mu so a concurrent
	// DeleteWorker / Stop cannot close the channel beneath us. The
	// tracker.Snapshot call also runs under the lock for the same
	// reason: it touches activity-log state that DeleteWorker tears
	// down via tracker.Close once the worker goroutine returns.
	depth := len(h.inbox)
	tracker := h.tracker
	d.mu.Unlock()

	st, entries := tracker.Snapshot()
	st.InboxDepth = depth
	return st, entries, true
}

// ListCards returns a snapshot of every currently-registered worker's
// status (without the activity ring). Sorted by card_id for stable output.
func (d *Dispatcher) ListCards() []WorkerStatus {
	type sample struct {
		tracker *ActivityTracker
		depth   int
	}
	d.mu.Lock()
	samples := make([]sample, 0, len(d.workers))
	for _, h := range d.workers {
		samples = append(samples, sample{tracker: h.tracker, depth: len(h.inbox)})
	}
	d.mu.Unlock()

	out := make([]WorkerStatus, 0, len(samples))
	for _, s := range samples {
		st, _ := s.tracker.Snapshot()
		st.InboxDepth = s.depth
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CardID < out[j].CardID })
	return out
}
