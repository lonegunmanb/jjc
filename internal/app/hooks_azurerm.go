package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

// azurermProviderModuleLine is the exact (post-trim) module declaration
// the detector requires on the first non-empty line of go.mod.
const azurermProviderModuleLine = "module github.com/hashicorp/terraform-provider-azurerm"

// AzureRMRefreshHookOptions tunes NewAzureRMRefreshHook. All fields are
// optional except either Spawner or RunSession (one of them is required)
// and ScriptPath; defaults are documented per field.
type AzureRMRefreshHookOptions struct {
	// Spawner is used to create the ephemeral Copilot session that runs
	// the refresh script. Required unless RunSession is supplied. In
	// production this is CopilotRunner.SessionSpawner().
	Spawner SessionSpawner

	// RunSession optionally overrides the default "spawn -> send -> wait
	// for idle -> disconnect" subroutine. When non-nil it is invoked
	// instead of going through Spawner. Tests use this to substitute a
	// stub without standing up a real *copilot.Session.
	RunSession SessionRunFunc

	// ScriptPath is the absolute path of refresh-copilot-setup.ps1.
	// Required.
	ScriptPath string

	// CardInfoFetcher resolves the Trello issue number when the gateway
	// has not already classified the card (i.e. info.Classification.Number
	// is empty). Optional: when nil, an unclassified card causes the hook
	// to skip with a logged warning rather than spawn a session that
	// cannot proceed. Production wiring passes a SDK-backed fetcher so the
	// hook never has to shell out to PowerShell to talk to Trello.
	CardInfoFetcher CardInfoFetcher

	// Model selects the Copilot model for the refresh session. Defaults
	// to DefaultCopilotModel.
	Model string

	// Timeout bounds the entire spawn-send-wait-disconnect cycle. Defaults
	// to 5 minutes; pass a negative value to disable (parent context
	// still applies).
	Timeout time.Duration

	// Logger receives lifecycle log lines. Defaults to log.Default().
	Logger *log.Logger
}

// SessionRunFunc executes one Copilot session end-to-end: it owns spawn,
// send a single user prompt, block until idle, and disconnect. Hooks
// accept this so tests can inject a stub without going through the real
// SDK.
type SessionRunFunc func(ctx context.Context, cfg *copilot.SessionConfig, prompt string) error

// NewAzureRMRefreshHook returns a WorkDirHook that runs only when the
// per-card work_dir contains a clone of hashicorp/terraform-provider-azurerm
// (detected by go.mod's first non-empty line). When that is the case it
// resolves the GitHub issue number Go-side (preferring the gateway's own
// classification, falling back to a SDK-backed CardInfoFetcher when one
// is configured) and spawns an independent, single-purpose Copilot
// session whose only job is to invoke
// refresh-copilot-setup.ps1 <work_dir> -Issue <issue_number>.
//
// The session is intentionally minimal: no WORKER.md, no entry playbook,
// no orchestration role, and crucially no Trello traffic — every Trello
// call has already been made by the Go-side wiring before the prompt is
// built. It runs once and disconnects.
//
// Detection failure (missing or unreadable go.mod) is reported as an
// error so the gateway can log it; a clean "this is not the azurerm
// provider" verdict returns nil.
func NewAzureRMRefreshHook(opts AzureRMRefreshHookOptions) (WorkDirHook, error) {
	if opts.Spawner == nil && opts.RunSession == nil {
		return nil, errors.New("azurerm refresh hook: Spawner or RunSession is required")
	}
	if opts.ScriptPath == "" {
		return nil, errors.New("azurerm refresh hook: ScriptPath is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	model := opts.Model
	if model == "" {
		model = DefaultCopilotModel
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	runSession := opts.RunSession
	if runSession == nil {
		runSession = newSpawnerRunSession(opts.Spawner, logger)
	}

	return func(ctx context.Context, info WorkDirInfo) error {
		match, err := isAzureRMProviderModule(info.WorkDir)
		if err != nil {
			logger.Printf("event=azurerm_refresh_hook_inspect_failed card_id=%s work_dir=%s err=%v",
				info.CardID, info.WorkDir, err)
			return fmt.Errorf("inspect go.mod under %s: %w", info.WorkDir, err)
		}
		if !match {
			logger.Printf("event=azurerm_refresh_hook_skip card_id=%s work_dir=%s reason=not_azurerm_provider",
				info.CardID, info.WorkDir)
			return nil
		}

		sessCtx := ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			sessCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		issueNumber, resolveErr := resolveAzureRMIssueNumber(sessCtx, info, opts.CardInfoFetcher, logger)
		if resolveErr != nil {
			logger.Printf("event=azurerm_refresh_hook_skip card_id=%s work_dir=%s reason=issue_number_unresolved err=%v",
				info.CardID, info.WorkDir, resolveErr)
			return nil
		}

		systemPrompt := buildAzureRMRefreshSystemPrompt(info.CardID, info.WorkDir, opts.ScriptPath)
		userPrompt := buildAzureRMRefreshUserPrompt(info.CardID, info.WorkDir, opts.ScriptPath, issueNumber)

		cfg := &copilot.SessionConfig{
			Model:               model,
			WorkingDirectory:    info.WorkDir,
			OnPermissionRequest: approveAllUserIntent,
			Hooks: &copilot.SessionHooks{
				OnPreToolUse: func(_ copilot.PreToolUseHookInput, _ copilot.HookInvocation) (*copilot.PreToolUseHookOutput, error) {
					return &copilot.PreToolUseHookOutput{PermissionDecision: "allow"}, nil
				},
			},
			SystemMessage: &copilot.SystemMessageConfig{
				Mode:    "append",
				Content: systemPrompt,
			},
		}

		logger.Printf("event=azurerm_refresh_hook_spawning card_id=%s work_dir=%s script=%s",
			info.CardID, info.WorkDir, opts.ScriptPath)
		start := time.Now()
		if err := runSession(sessCtx, cfg, userPrompt); err != nil {
			return fmt.Errorf("refresh session: %w", err)
		}
		logger.Printf("event=azurerm_refresh_hook_done card_id=%s duration=%s",
			info.CardID, time.Since(start))
		return nil
	}, nil
}

// newSpawnerRunSession returns a SessionRunFunc backed by a SessionSpawner:
// spawn -> send one prompt -> wait for idle -> disconnect.
func newSpawnerRunSession(spawner SessionSpawner, logger *log.Logger) SessionRunFunc {
	return func(ctx context.Context, cfg *copilot.SessionConfig, prompt string) error {
		sess, err := spawner.Spawn(ctx, cfg)
		if err != nil {
			return fmt.Errorf("spawn refresh session: %w", err)
		}
		defer func() {
			if dErr := sess.Disconnect(); dErr != nil {
				logger.Printf("event=azurerm_refresh_hook_disconnect_error err=%v", dErr)
			}
		}()
		return refreshSessionSendAndWait(ctx, sess, prompt, logger, cfg.WorkingDirectory)
	}
}

// isAzureRMProviderModule returns true when <workDir>/go.mod exists and
// its first non-empty trimmed line is exactly the azurerm provider module
// declaration. A missing go.mod is a clean "no, not the provider" — not
// an error — to keep generic / non-Go work_dirs from spamming the log.
func isAzureRMProviderModule(workDir string) (bool, error) {
	f, err := os.Open(filepath.Join(workDir, "go.mod"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Cap scanner buffer so a malformed huge first line can't OOM us.
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		return line == azurermProviderModuleLine, scanner.Err()
	}
	return false, scanner.Err()
}

func buildAzureRMRefreshSystemPrompt(cardID, workDir, scriptPath string) string {
	var b strings.Builder
	b.WriteString("# Single-purpose refresh session\n\n")
	b.WriteString("You are an ephemeral Copilot session spawned by the Trello gateway's work_dir ")
	b.WriteString("hook to refresh the upstream `.github/instructions/` etc. files inside a clone ")
	b.WriteString("of `hashicorp/terraform-provider-azurerm`. You are NOT the per-card worker, you ")
	b.WriteString("have NO orchestration role, you DO NOT post Trello comments, you DO NOT move ")
	b.WriteString("the card. Execute the task in the user message exactly once and then end your ")
	b.WriteString("turn.\n\n")
	fmt.Fprintf(&b, "- card_id: %s\n", cardID)
	fmt.Fprintf(&b, "- work_dir: %s\n", workDir)
	fmt.Fprintf(&b, "- refresh_script: %s\n", scriptPath)
	return b.String()
}

func buildAzureRMRefreshUserPrompt(cardID, workDir, scriptPath, issueNumber string) string {
	var b strings.Builder
	b.WriteString("# TASK\n\n")
	fmt.Fprintf(&b, "Run the following PowerShell invocation exactly once and then end your turn. ")
	fmt.Fprintf(&b, "The Trello card lookup and issue-number extraction have already been done ")
	fmt.Fprintf(&b, "Go-side: card_id=%s issue_number=%s.\n\n", cardID, issueNumber)

	b.WriteString("> **Use `pwsh` (PowerShell 7+), NOT `powershell` (Windows PowerShell 5.1).** ")
	b.WriteString("`refresh-copilot-setup.ps1` relies on automatic variables like `$IsWindows` that ")
	b.WriteString("only exist in PowerShell 7+; invoking via `powershell -NoProfile -File ...` will ")
	b.WriteString("fail with `The variable '$IsWindows' cannot be retrieved because it has not been set`. ")
	b.WriteString("Stick to `pwsh -NoProfile -File ...`.\n\n")

	b.WriteString("## Run refresh-copilot-setup.ps1\n\n")
	b.WriteString("```powershell\n")
	fmt.Fprintf(&b, "pwsh -NoProfile -File %s %s -Issue %s\n", scriptPath, workDir, issueNumber)
	b.WriteString("```\n\n")

	b.WriteString("## Stop conditions\n\n")
	b.WriteString("- If the refresh script fails, summarize the failure in your final assistant ")
	b.WriteString("message and stop. Do NOT retry indefinitely.\n")
	b.WriteString("- Do NOT call the Trello REST API or any Trello-helper PowerShell script; the gateway already ")
	b.WriteString("resolved the issue number and embedded it above.\n")
	b.WriteString("- Do NOT post Trello comments, do NOT move the card, do NOT touch git. The ")
	b.WriteString("per-card worker handles all of that.\n")
	return b.String()
}

// resolveAzureRMIssueNumber finds the integer GitHub issue/PR number
// associated with info.CardID. It prefers the gateway's own
// classification (already populated by classifyForWorker before the
// hook fires) and only falls back to a Trello fetch when the
// classification is missing — keeping the hook synchronous and
// PowerShell-free in the common case.
func resolveAzureRMIssueNumber(ctx context.Context, info WorkDirInfo, fetcher CardInfoFetcher, logger *log.Logger) (string, error) {
	if n := strings.TrimSpace(info.Classification.Number); n != "" {
		return n, nil
	}
	if fetcher == nil {
		return "", errors.New("no classification.Number and no CardInfoFetcher configured")
	}
	text, err := fetcher(ctx, info.CardID)
	if err != nil {
		return "", fmt.Errorf("fetch card info: %w", err)
	}
	cls := ClassifyCard(text)
	if n := strings.TrimSpace(cls.Number); n != "" {
		return n, nil
	}
	logger.Printf("event=azurerm_refresh_hook_classify_no_number card_id=%s text_bytes=%d",
		info.CardID, len(text))
	return "", errors.New("could not extract issue number from card description")
}

// refreshSessionSendAndWait mirrors copilotWorkerSession.SendAndWait but
// for the hook's ephemeral session: it sends one prompt, blocks on the
// next SessionIdleData, and surfaces SessionErrorData as an error.
func refreshSessionSendAndWait(ctx context.Context, sess *copilot.Session, prompt string, logger *log.Logger, cardID string) error {
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	var (
		errMu   sync.Mutex
		sawErr  bool
		lastErr string
	)

	unsubscribe := sess.On(func(event copilot.SessionEvent) {
		// Tag the log line so refresh-session traffic is grep-able.
		logSessionEvent(logger, "refresh:"+cardID, event)
		switch d := event.Data.(type) {
		case *copilot.SessionIdleData:
			closeDone()
		case *copilot.SessionErrorData:
			errMu.Lock()
			sawErr = true
			lastErr = d.Message
			errMu.Unlock()
		}
	})
	defer unsubscribe()

	if _, err := sess.Send(ctx, copilot.MessageOptions{Prompt: prompt}); err != nil {
		return err
	}

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	errMu.Lock()
	defer errMu.Unlock()
	if sawErr {
		return fmt.Errorf("session error: %s", lastErr)
	}
	return nil
}
