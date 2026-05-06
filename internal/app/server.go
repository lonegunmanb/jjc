package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// NewRouter wires up the gin router with the default CopilotRunner targeting
// the model configured in cfg.CopilotModel. The returned router does not own
// the runner's lifecycle; callers must Start/Stop it themselves.
func NewRouter(cfg Config, runner *CopilotRunner, logger *log.Logger) *gin.Engine {
	return NewRouterWithRunner(cfg, runner, logger)
}

// NewRouterWithRunner exposes the runner dependency for tests so that the
// copilot CLI invocation can be stubbed out.
func NewRouterWithRunner(cfg Config, runner *CopilotRunner, logger *log.Logger) *gin.Engine {
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

		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
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
		// immediately and process the event in the background. The dispatch
		// uses context.Background() because the request context is cancelled
		// as soon as we return.
		logger.Printf("event=copilot_dispatch_start event_id=%s model=%s", eventID, runner.Model())
		c.Status(http.StatusAccepted)

		go func(eventID string, raw []byte) {
			promptPath, err := runner.Handle(context.Background(), eventID, raw)
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
