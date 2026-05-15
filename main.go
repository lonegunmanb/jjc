package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/term"

	"github.com/lonegunmanb/trello-copilot/internal/app"
	"github.com/lonegunmanb/trello-copilot/internal/app/prompts"
	"github.com/lonegunmanb/trello-copilot/internal/app/prompttmpl"
)

func main() {
	cfg, err := app.LoadConfig(os.Args)
	if err != nil {
		log.New(os.Stderr, "", log.LstdFlags).Fatalf("invalid config: %v", err)
	}

	// Always redirect logs to a file so stdio is free for the REPL.
	const logFileName = "trellooperator.log"
	var logOut = os.Stderr
	if f, ferr := os.OpenFile(logFileName, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr != nil {
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

	// Pre-render every playbook .md file in cfg.PlaybooksDir into a
	// per-process temp directory; substitute every `{{<basename>}}`
	// reference inside those files with the absolute path of that
	// playbook in the same temp directory. Skeleton prompts shipped with
	// the binary (BOOTSTRAP / IDENTITY / WORKER / TOOLS / USER) are
	// written first, so any user file with the same basename overrides
	// the embedded copy.
	renderer, err := prompttmpl.New(prompttmpl.Options{
		PlaybooksDir:     cfg.PlaybooksDir,
		EmbeddedDefaults: prompts.Defaults(),
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

	runner := app.NewCopilotRunner(cfg.CopilotModel, logger)
	runner.SetRouterDir(cfg.RouterDir)
	runner.SetCardInfoFetcher(app.NewScriptCardInfoFetcher(cfg.RouterDir))
	runner.SetPlaybooks(renderer)

	// Register the AzureRM provider refresh hook: when the per-card
	// work_dir turns out to be a clone of hashicorp/terraform-provider-azurerm
	// (detected by go.mod's first line), spawn an independent Copilot
	// session that reads the Trello card to find the issue number and runs
	// refresh-copilot-setup.ps1 against the work_dir. The hook silently
	// no-ops for any other repo.
	if cfg.RouterDir != "" {
		azurermHook, hookErr := app.NewAzureRMRefreshHook(app.AzureRMRefreshHookOptions{
			Spawner:            runner.SessionSpawner(),
			ScriptPath:         filepath.Join(cfg.RouterDir, "scripts", "refresh-copilot-setup.ps1"),
			CardInfoScriptPath: filepath.Join(cfg.RouterDir, "scripts", "trello-get-card-info.ps1"),
			Model:              cfg.CopilotModel,
			Logger:             logger,
		})
		if hookErr != nil {
			logger.Printf("event=azurerm_refresh_hook_register_failed err=%v", hookErr)
		} else {
			runner.RegisterWorkDirHook(azurermHook)
			logger.Printf("event=azurerm_refresh_hook_registered script=%s",
				filepath.Join(cfg.RouterDir, "scripts", "refresh-copilot-setup.ps1"))
		}
	}

	globalLog := app.NewGlobalEventLog(128)
	runner.Dispatcher().SetGlobalLog(globalLog)
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

	router := app.NewRouter(cfg, runner, logger)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Printf("event=http_listening addr=%s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("event=http_server_error err=%v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
