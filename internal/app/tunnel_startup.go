package app

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/lonegunmanb/jjc/internal/app/sysevent"
	"github.com/lonegunmanb/jjc/internal/app/trelloclient"
	"github.com/lonegunmanb/jjc/internal/app/tunnel"
)

type switchableHandler struct {
	mu      sync.RWMutex
	handler http.Handler
}

func NewSwitchableHandler(initial http.Handler) *switchableHandler {
	return &switchableHandler{handler: initial}
}

func (h *switchableHandler) Set(next http.Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handler = next
}

func (h *switchableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	next := h.handler
	h.mu.RUnlock()
	if next == nil {
		http.Error(w, "gateway starting", http.StatusServiceUnavailable)
		return
	}
	next.ServeHTTP(w, r)
}

func NewValidationHandler(logger sysevent.Sink) http.Handler {
	if logger == nil {
		logger = sysevent.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodHead)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		sysevent.Emitf(logger, "trello_validation", "method=HEAD remote=%s ua=%q", r.RemoteAddr, r.UserAgent())
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func StartTunnelAndReconcile(ctx context.Context, cfg *Config, provider tunnel.Provider, trelloClient trelloclient.Client, localAddr string, logger sysevent.Sink) (string, error) {
	id, _, err := StartTunnelAndReconcileWithOwnership(ctx, cfg, provider, trelloClient, localAddr, logger)
	return id, err
}

// StartTunnelAndReconcileWithOwnership is the full-fidelity variant of
// StartTunnelAndReconcile. It additionally reports whether this call
// CREATED the webhook (vs. updating one that already existed). main.go
// uses the flag to decide whether to clean the webhook up on shutdown:
// gateway-owned webhooks get deleted so a TryCloudflare URL that just
// died doesn't keep a dangling webhook on Trello; pre-existing
// out-of-band webhooks (e.g. operator-managed for a stable DNS-backed
// callback) are left alone.
func StartTunnelAndReconcileWithOwnership(ctx context.Context, cfg *Config, provider tunnel.Provider, trelloClient trelloclient.Client, localAddr string, logger sysevent.Sink) (string, bool, error) {
	if cfg == nil {
		return "", false, errors.New("config is nil")
	}
	if cfg.Tunnel == tunnel.None {
		return "", false, nil
	}
	if provider == nil {
		return "", false, errors.New("tunnel provider is nil")
	}
	if trelloClient == nil {
		return "", false, errors.New("trello client is nil")
	}
	publicURL, err := provider.Start(ctx, localAddr)
	if err != nil {
		return "", false, err
	}
	webhookID, createdNow, err := trelloclient.ReconcileBoardWebhook(ctx, trelloClient, cfg.TrelloAPIToken, cfg.KanbanBoardID, publicURL)
	if err != nil {
		_ = provider.Stop()
		return "", false, err
	}
	cfg.CallbackURL = publicURL
	if logger != nil {
		sysevent.Emitf(logger, "trello_webhook_reconciled", "provider=%s board_id=%s webhook_id=%s callback_url=%s created_now=%t", provider.Name(), cfg.KanbanBoardID, webhookID, publicURL, createdNow)
	}
	return webhookID, createdNow, nil
}

// DeleteGatewayCreatedWebhook removes a webhook this process previously
// created via StartTunnelAndReconcileWithOwnership. Safe to call from
// main's defer chain: a nil/empty webhookID, or createdNow==false, is
// a no-op so the caller does not need a branch.
//
// A 404 from Trello (webhook already gone) is treated as success by
// trelloclient.Client.DeleteWebhook, so this helper is idempotent.
func DeleteGatewayCreatedWebhook(ctx context.Context, cfg *Config, trelloClient trelloclient.Client, webhookID string, createdNow bool, logger sysevent.Sink) error {
	if !createdNow || webhookID == "" || trelloClient == nil || cfg == nil {
		return nil
	}
	if err := trelloClient.DeleteWebhook(ctx, cfg.TrelloAPIToken, webhookID); err != nil {
		if logger != nil {
			sysevent.Emitf(logger, "trello_webhook_delete_failed", "board_id=%s webhook_id=%s err=%v", cfg.KanbanBoardID, webhookID, err)
		}
		return err
	}
	if logger != nil {
		sysevent.Emitf(logger, "trello_webhook_deleted", "board_id=%s webhook_id=%s", cfg.KanbanBoardID, webhookID)
	}
	return nil
}
