package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/term"

	"github.com/lonegunmanb/jjc/internal/app"
	"github.com/lonegunmanb/jjc/internal/app/aiassistedrefresh"
	"github.com/lonegunmanb/jjc/internal/app/kanban"
	"github.com/lonegunmanb/jjc/internal/app/prompts"
	"github.com/lonegunmanb/jjc/internal/app/prompttmpl"
	"github.com/lonegunmanb/jjc/internal/app/router"
	"github.com/lonegunmanb/jjc/internal/app/trelloclient"
	"github.com/lonegunmanb/jjc/internal/app/tunnel"
)

// kanbanFetcherAdapter bridges trelloclient.Client (which returns
// []trelloclient.List) to the kanban.BoardListsFetcher interface
// (which expects []kanban.BoardList). The kanban package intentionally
// does not depend on trelloclient so tests can stub the fetch without
// dragging in the full HTTP-backed surface.
type kanbanFetcherAdapter struct{ c trelloclient.Client }

func newKanbanFetcher(c trelloclient.Client) kanban.BoardListsFetcher {
	return &kanbanFetcherAdapter{c: c}
}

func (a *kanbanFetcherAdapter) ListBoardLists(ctx context.Context, boardID string) ([]kanban.BoardList, error) {
	raw, err := a.c.ListBoardLists(ctx, boardID)
	if err != nil {
		return nil, err
	}
	out := make([]kanban.BoardList, len(raw))
	for i, l := range raw {
		out[i] = kanban.BoardList{ID: l.ID, Name: l.Name}
	}
	return out, nil
}

func main() {
	cfg, err := app.LoadConfig(os.Args)
	if err != nil {
		log.New(os.Stderr, "", log.LstdFlags).Fatalf("invalid config: %v", err)
	}

	// Always redirect logs to a file so stdio is free for the REPL.
	const logFileName = "trellooperator.log"
	var logOut = os.Stderr
	// Mode 0o600: the log captures card ids, model output, prompt
	// previews and timing data. Restrict to the gateway user so a
	// shared host (or a stray operator) cannot read it without
	// explicit privilege escalation.
	if f, ferr := os.OpenFile(logFileName, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr != nil {
		log.New(os.Stderr, "", log.LstdFlags).Printf("warning: cannot open log file %q (%v); falling back to stderr", logFileName, ferr)
	} else {
		logOut = f
		defer f.Close()
	}
	logger := log.New(logOut, "", log.LstdFlags)
	gin.DefaultWriter = logOut
	gin.DefaultErrorWriter = logOut

	logger.Printf("event=gateway_starting %s log_file=%q", cfg.Redacted(), logFileName)
	fmt.Fprintf(os.Stdout, "trello-gateway: logging to %s\n", logFileName)

	runner := app.NewCopilotRunner(cfg.CopilotModel, logger)
	runner.SetRouterDir(cfg.RouterDir)

	// Build the SDK-backed Trello client once at startup. Both the
	// CardInfoFetcher (used to derive work_type from a card description)
	// and the per-session `trello_*` tools share this client — no more
	// pwsh.exe shell-out for any Trello traffic.
	trelloClient, terr := trelloclient.New(
		trelloclient.WithCredentials(cfg.TrelloAPIKey, cfg.TrelloAPIToken),
		trelloclient.WithLogger(logger),
	)
	if terr != nil {
		logger.Fatalf("event=trelloclient_init_failed err=%v", terr)
	}
	runner.SetTrelloClient(trelloClient)
	runner.SetCardInfoFetcher(app.NewSDKCardInfoFetcher(trelloClient))
	runner.SetCardSignalsFetcher(app.NewSDKCardSignalsFetcher(trelloClient))

	// Resolve the kanban {} block in router.hcl against the configured
	// Trello board. cfg.KanbanBoardID was already validated to be
	// non-empty by LoadConfig; the bootstrap is unconditional here.
	//
	// Resolution has to land before prompttmpl.New so the renderer can
	// substitute `{{kanban.*}}` template variables (see
	// docs/playbook-template-variables.md) into every playbook .md at
	// startup; otherwise unknown_kanban_key errors would fire for the
	// very first reference.
	kanbanCtx, kanbanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	hclPath := filepath.Join(cfg.RouterDir, "router.hcl")
	resolved, kerr := kanban.LoadAndResolve(kanbanCtx, hclPath, cfg.KanbanBoardID,
		newKanbanFetcher(trelloClient), logger)
	kanbanCancel()
	if kerr != nil {
		// Surface the distinct failure modes the issue calls out so
		// operators can grep the structured event token.
		var rerr *kanban.ResolveError
		if errors.As(kerr, &rerr) {
			logger.Fatalf("event=kanban_resolve_failed board_id=%s missing_roles=%v ambiguous_roles=%v err=%v",
				rerr.BoardID, rerr.MissingRoles, rerr.AmbiguousRoles, rerr)
		}
		// Distinguish "could not fetch lists" from "could not read HCL"
		// by inspecting the wrapped error message — both are fatal but
		// the log token differs so dashboards can alert on them
		// separately.
		if strings.Contains(kerr.Error(), "fetch board lists") {
			logger.Fatalf("event=trello_board_lists_fetch_failed board_id=%s err=%v",
				cfg.KanbanBoardID, kerr)
		}
		logger.Fatalf("event=kanban_load_failed hcl_path=%s board_id=%s err=%v",
			hclPath, cfg.KanbanBoardID, kerr)
	}
	logger.Printf("event=kanban_resolved board_id=%s plan_id=%s action_id=%s done_id=%s wait_list_count=%d unclaimed_count=%d",
		resolved.BoardID, resolved.Plan.ID, resolved.Action.ID, resolved.Done.ID,
		len(resolved.WaitListIDs), len(resolved.UnclaimedListNames))
	runner.SetKanbanView(resolved)

	// Pre-render every playbook .md file in cfg.PlaybooksDir into a
	// per-process temp directory; substitute every `{{<basename>}}`
	// reference inside those files with the absolute path of that
	// playbook in the same temp directory, and every `{{kanban.*}}`
	// reference with the corresponding value drawn from the resolved
	// kanban view. Skeleton prompts shipped with the binary (BOOTSTRAP
	// / IDENTITY / WORKER / TOOLS / USER) are written first, so any
	// user file with the same basename overrides the embedded copy.
	renderer, err := prompttmpl.New(prompttmpl.Options{
		PlaybooksDir:     cfg.PlaybooksDir,
		EmbeddedDefaults: prompts.Defaults(),
		KanbanVars:       resolved.PromptVars(),
		Logger:           logger,
	})
	if err != nil {
		logger.Fatalf("event=playbooks_dir_invalid err=%v", err)
	}
	defer func() {
		if err := renderer.Cleanup(); err != nil {
			logger.Printf("event=playbooks_tempdir_cleanup_failed err=%v", err)
		}
	}()
	runner.SetPlaybooks(renderer)

	ruleCfg, ruleErr := router.LoadRuleConfig(hclPath, cfg.PlaybooksDir)
	if ruleErr != nil {
		logger.Fatalf("event=rule_load_failed hcl_path=%s playbooks_dir=%s err=%v",
			hclPath, cfg.PlaybooksDir, ruleErr)
	}
	runner.SetRuleEngine(router.NewRuleEngine(ruleCfg, cfg.RouterDir, resolved, logger))

	// Load the HCL `route {}` blocks the same way and hand the engine
	// to the dispatcher so every Trello event is classified by the
	// operator-managed router.hcl rather than the legacy Go switch
	// that lived in internal/app/routing.go before #6 landed.
	routeCfg, routeErr := router.LoadConfig(hclPath)
	if routeErr != nil {
		logger.Fatalf("event=route_load_failed hcl_path=%s err=%v", hclPath, routeErr)
	}
	runner.Dispatcher().SetRouteEngine(router.NewEngine(routeCfg, resolved, logger))

	// Register the AzureRM provider refresh hook: when the per-card
	// work_dir turns out to be a clone of hashicorp/terraform-provider-azurerm
	// (detected by go.mod's first line), synchronously refresh the
	// upstream `.github/instructions/` etc. by cloning
	// WodansSon/terraform-azurerm-ai-assisted-development into a temp dir
	// and running its installer (pwsh + .ps1 on Windows, bash + .sh on
	// macOS / Linux — chosen at runtime by aiassistedrefresh based on
	// GOOS). The hook silently no-ops for any other repo.
	refresher := aiassistedrefresh.New(aiassistedrefresh.WithLogger(logger))
	azurermHook, hookErr := app.NewAzureRMRefreshHook(app.AzureRMRefreshHookOptions{
		Refresher:       refresher,
		CardInfoFetcher: app.NewSDKCardInfoFetcher(trelloClient),
		Logger:          logger,
	})
	if hookErr != nil {
		logger.Printf("event=azurerm_refresh_hook_register_failed err=%v", hookErr)
	} else {
		runner.RegisterWorkDirHook(azurermHook)
		logger.Printf("event=azurerm_refresh_hook_registered impl=aiassistedrefresh")
	}

	globalLog := app.NewGlobalEventLog(128)
	runner.Dispatcher().SetGlobalLog(globalLog)

	// Establish the shutdown context before the HTTP server so validation
	// requests, tunnel startup and background dispatch all share the same
	// SIGINT/SIGTERM cancellation signal.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var tunnelProvider tunnel.Provider
	if cfg.Tunnel == tunnel.Cloudflared {
		p, perr := tunnel.NewCloudflaredProvider(tunnel.WithLogger(logger))
		if perr != nil {
			logger.Fatalf("event=tunnel_provider_init_failed provider=%s err=%v", cfg.Tunnel, perr)
		}
		tunnelProvider = p
		defer func() {
			if err := tunnelProvider.Stop(); err != nil {
				logger.Printf("event=tunnel_stop_failed provider=%s err=%v", tunnelProvider.Name(), err)
			}
		}()
	}

	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := runner.Start(startCtx); err != nil {
		startCancel()
		logger.Fatalf("start copilot runner: %v", err)
	}
	startCancel()
	defer func() {
		if err := runner.Stop(); err != nil {
			logger.Printf("stop copilot runner: %v", err)
		}
	}()

	handler := app.NewSwitchableHandler(app.NewValidationHandler(logger))
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Slow-loris and slow-write defence: cap the time we'll spend
		// reading a body or writing a response for any one request.
		// Trello payloads are tiny (<50 KiB) so 15s is generous; keeping
		// idle keep-alive at 60s caps the count of orphan connections
		// the runtime will hold on to between events.
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ln, lerr := net.Listen("tcp", cfg.ListenAddr)
	if lerr != nil {
		logger.Fatalf("event=http_listen_failed addr=%s err=%v", cfg.ListenAddr, lerr)
	}
	cfg.ListenAddr = ln.Addr().String()
	go func() {
		logger.Printf("event=http_listening addr=%s", cfg.ListenAddr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("event=http_server_error err=%v", err)
		}
	}()

	if cfg.Tunnel != tunnel.None {
		tunnelCtx, tunnelCancel := context.WithTimeout(ctx, 60*time.Second)
		webhookID, createdNow, err := app.StartTunnelAndReconcileWithOwnership(tunnelCtx, &cfg, tunnelProvider, trelloClient, cfg.ListenAddr, logger)
		tunnelCancel()
		if err != nil {
			logger.Fatalf("event=tunnel_start_failed provider=%s err=%v", cfg.Tunnel, err)
		}
		// If THIS process created the webhook (vs. updated an existing
		// one operators provisioned out-of-band), delete it on shutdown
		// so a defunct trycloudflare URL doesn't keep a dangling
		// webhook on Trello. A pre-existing webhook is left alone.
		if createdNow {
			defer func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cleanupCancel()
				if err := app.DeleteGatewayCreatedWebhook(cleanupCtx, &cfg, trelloClient, webhookID, true, logger); err != nil {
					logger.Printf("event=trello_webhook_shutdown_cleanup_error webhook_id=%s err=%v", webhookID, err)
				}
			}()
		}
	}

	router := app.NewRouter(ctx, cfg, runner, logger)
	handler.Set(router)

	if term.IsTerminal(int(os.Stdin.Fd())) {
		// TUI mode: full-screen bubbletea interface.
		p := app.NewTUIProgram(runner.Dispatcher(), runner.Dispatcher(), globalLog, cfg.ListenAddr, cfg.CopilotModel)
		go func() {
			<-ctx.Done()
			p.Quit()
		}()
		logger.Printf("event=tui_starting")
		if _, err := p.Run(); err != nil {
			logger.Printf("event=tui_error err=%v", err)
		}
		stop() // cancel context to trigger shutdown
	} else {
		// Headless mode: line-oriented REPL.
		fmt.Fprintln(os.Stdout, "trello-gateway: no TTY detected, using REPL mode")
		go func() {
			repl := app.NewREPL(runner.Dispatcher(), os.Stdin, os.Stdout)
			_ = repl.Run(ctx)
		}()
		<-ctx.Done()
	}

	logger.Printf("event=shutdown_signal")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("event=http_shutdown_error err=%v", err)
	}
	logger.Printf("event=http_stopped")
}
