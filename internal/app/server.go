package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// MaxWebhookBodyBytes caps the request body the gateway is willing to
// read off the wire. Trello payloads are typically <50 KiB; allowing
// 1 MiB leaves headroom for unusually large card descriptions while
// still bounding memory per request.
const MaxWebhookBodyBytes int64 = 1 << 20

// MaxDispatchDuration bounds how long a single background dispatch
// goroutine is allowed to live. The Copilot session that the dispatch
// hands off to may take much longer than this in steady state — the cap
// exists strictly so a stuck dispatch (unable to spawn a session, blocked
// on I/O) cannot leak forever under sustained webhook traffic.
const MaxDispatchDuration = 30 * time.Minute

// NewRouter wires up the gin router with the default CopilotRunner targeting
// the model configured in cfg.CopilotModel. The returned router does not own
// the runner's lifecycle; callers must Start/Stop it themselves.
//
// dispatchCtx is the parent context for every background dispatch
// goroutine the router spawns. main wires it to the gateway's shutdown
// signal so SIGTERM cancels in-flight dispatches instead of letting them
// run forever on context.Background. A nil dispatchCtx is upgraded to
// context.Background for backward compatibility with tests that don't
// care about cancellation.
func NewRouter(dispatchCtx context.Context, cfg Config, runner *CopilotRunner, logger *log.Logger) *gin.Engine {
	return NewRouterWithRunner(dispatchCtx, cfg, runner, logger)
}

// NewRouterWithRunner exposes the runner dependency for tests so that the
// copilot CLI invocation can be stubbed out.
func NewRouterWithRunner(dispatchCtx context.Context, cfg Config, runner *CopilotRunner, logger *log.Logger) *gin.Engine {
	if dispatchCtx == nil {
		dispatchCtx = context.Background()
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.HandleMethodNotAllowed = true
	r.NoMethod(func(c *gin.Context) {
		logger.Printf("event=method_not_allowed method=%s path=%s remote=%s", c.Request.Method, c.Request.URL.Path, c.ClientIP())
		c.Status(http.StatusMethodNotAllowed)
	})

	r.HEAD("/", func(c *gin.Context) {
		logger.Printf("event=trello_validation method=HEAD remote=%s ua=%q", c.ClientIP(), c.Request.UserAgent())
		c.Status(http.StatusOK)
	})

	r.POST("/", func(c *gin.Context) {
		eventID := newEventID()
		remote := c.ClientIP()
		ua := c.Request.UserAgent()
		logger.Printf("event=trello_event_received event_id=%s remote=%s ua=%q content_length=%d", eventID, remote, ua, c.Request.ContentLength)

		// Cheap pre-check on the advertised Content-Length so we don't
		// even start reading bodies that are obviously too large. -1
		// (unknown) and 0 are both fine here; the streaming guard below
		// catches actually-too-large bodies whose CL was missing or lied.
		if c.Request.ContentLength > MaxWebhookBodyBytes {
			logger.Printf("event=trello_body_too_large event_id=%s remote=%s content_length=%d limit=%d",
				eventID, remote, c.Request.ContentLength, MaxWebhookBodyBytes)
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		// Wrap the body so io.ReadAll cannot allocate beyond the cap;
		// MaxBytesReader returns *http.MaxBytesError once the limit is
		// crossed, which we surface as 413.
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxWebhookBodyBytes)
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			var mbErr *http.MaxBytesError
			if errors.As(err, &mbErr) {
				logger.Printf("event=trello_body_too_large event_id=%s remote=%s limit=%d",
					eventID, remote, MaxWebhookBodyBytes)
				c.Status(http.StatusRequestEntityTooLarge)
				return
			}
			logger.Printf("event=trello_body_read_error event_id=%s err=%v", eventID, err)
			c.Status(http.StatusBadRequest)
			return
		}
		logger.Printf("event=trello_body_read event_id=%s body_bytes=%d", eventID, len(raw))

		headerSig := c.GetHeader("X-Trello-Webhook")
		ok := VerifySignature(cfg.TrelloSecret, raw, cfg.CallbackURL, headerSig)
		if !ok {
			logger.Printf("event=signature_invalid event_id=%s remote=%s", eventID, remote)
			c.Status(http.StatusForbidden)
			return
		}
		logger.Printf("event=signature_valid event_id=%s", eventID)

		summary := BuildMessage(raw)
		logger.Printf("event=trello_summary event_id=%s summary=%q", eventID, summary)

		// Trello requires a 2xx within ~30s or it will retry the webhook,
		// causing duplicate events. Manager dispatch can take much longer
		// (the agent may run multiple tool calls), so we acknowledge
		// immediately and process the event in the background. The
		// dispatch goroutine inherits dispatchCtx (tied to gateway
		// shutdown) and is further bounded by MaxDispatchDuration so a
		// stuck dispatch cannot leak under sustained webhook traffic.
		logger.Printf("event=copilot_dispatch_start event_id=%s model=%s", eventID, runner.Model())
		c.Status(http.StatusAccepted)

		go func(eventID string, raw []byte) {
			ctx, cancel := context.WithTimeout(dispatchCtx, MaxDispatchDuration)
			defer cancel()
			promptPath, err := runner.Handle(ctx, eventID, raw)
			if err != nil {
				logger.Printf("event=copilot_dispatch_error event_id=%s prompt_file=%s err=%v", eventID, promptPath, err)
				return
			}
			logger.Printf("event=copilot_dispatched event_id=%s prompt_file=%s", eventID, promptPath)
		}(eventID, raw)
	})

	return r
}

// newEventID returns a short random hex id used to correlate log lines for a
// single Trello webhook event end-to-end.
func newEventID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a constant marker; correlation will be lost but logging
		// must never panic the request.
		return "norandid"
	}
	return hex.EncodeToString(b[:])
}
