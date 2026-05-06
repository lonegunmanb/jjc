package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/term"

	"github.com/lonegunmanb/trello-copilot/internal/app"
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

	runner := app.NewCopilotRunner(cfg.CopilotModel, logger)
	runner.SetRouterDir(cfg.RouterDir)
	runner.SetCardInfoFetcher(app.NewScriptCardInfoFetcher(cfg.RouterDir))
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
		p := app.NewTUIProgram(runner.Dispatcher(), globalLog, cfg.ListenAddr, cfg.CopilotModel)
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
