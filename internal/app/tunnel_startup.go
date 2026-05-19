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
	if cfg == nil {
		return "", errors.New("config is nil")
	}
	if cfg.Tunnel == tunnel.None {
		return "", nil
	}
	if provider == nil {
		return "", errors.New("tunnel provider is nil")
	}
	if trelloClient == nil {
		return "", errors.New("trello client is nil")
	}
	publicURL, err := provider.Start(ctx, localAddr)
	if err != nil {
		return "", err
	}
	webhookID, err := trelloclient.ReconcileBoardWebhook(ctx, trelloClient, cfg.TrelloAPIToken, cfg.KanbanBoardID, publicURL)
	if err != nil {
		_ = provider.Stop()
		return "", err
	}
	cfg.CallbackURL = publicURL
	if logger != nil {
		sysevent.Emitf(logger, "trello_webhook_reconciled", "provider=%s board_id=%s webhook_id=%s callback_url=%s", provider.Name(), cfg.KanbanBoardID, webhookID, publicURL)
	}
	return webhookID, nil
}
