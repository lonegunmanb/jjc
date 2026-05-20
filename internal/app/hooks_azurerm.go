package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lonegunmanb/jjc/internal/app/aiassistedrefresh"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

// azurermProviderModuleLine is the exact (post-trim) module declaration
// the detector requires on the first non-empty line of go.mod.
const azurermProviderModuleLine = "module github.com/hashicorp/terraform-provider-azurerm"

// AzureRMRefreshHookOptions tunes NewAzureRMRefreshHook. Refresher is the
// only required field: every other knob carries a sane default.
//
// Compared to the legacy script-based wiring the hook used to expose
// (Spawner, RunSession, Model, ScriptPath, Timeout for the spawned
// session, etc.), this surface is intentionally minimal — refreshing the
// AI-assisted infrastructure is now a synchronous, in-process Go call
// (see internal/app/aiassistedrefresh) rather than an LLM turn.
type AzureRMRefreshHookOptions struct {
	// Refresher executes the actual refresh (clone upstream installer +
	// bootstrap + clean + install). Required. In production this is an
	// *aiassistedrefresh.Service constructed in main.go; tests inject an
	// aiassistedrefresh.RefresherFunc recording stub.
	Refresher aiassistedrefresh.Refresher

	// CardInfoFetcher resolves the GitHub issue number when the gateway
	// has not already classified the card (i.e. info.Classification.GitHub.Number
	// is empty). Optional: when nil, an unclassified card causes the hook
	// to skip with a logged warning rather than fall over.
	CardInfoFetcher CardInfoFetcher

	// Timeout bounds the synchronous refresh call. Defaults to 5
	// minutes; pass a negative value to disable (parent context still
	// applies).
	Timeout time.Duration

	// Logger receives lifecycle log lines. Defaults to sysevent.Default().
	Logger sysevent.Sink
}

// NewAzureRMRefreshHook returns a WorkDirHook that runs only when the
// per-card work_dir contains a clone of hashicorp/terraform-provider-azurerm
// (detected by go.mod's first non-empty line). When that is the case it
// resolves the GitHub issue number Go-side (preferring the gateway's own
// classification, falling back to a SDK-backed CardInfoFetcher when one
// is configured) and synchronously invokes the configured Refresher to
// clone the upstream AI-assisted installer + run bootstrap / clean /
// install against work_dir.
//
// The hook no longer spawns an ephemeral Copilot session, no longer
// builds prompts, and no longer shells out to PowerShell or Bash itself
// — the refresh path is fully Go-side and works on any platform supported
// by aiassistedrefresh (Windows uses pwsh + .ps1, macOS / Linux use bash
// + .sh).
//
// Detection failure (missing or unreadable go.mod) is reported as an
// error so the gateway can log it; a clean "this is not the azurerm
// provider" verdict returns nil.
func NewAzureRMRefreshHook(opts AzureRMRefreshHookOptions) (WorkDirHook, error) {
	if opts.Refresher == nil {
		return nil, errors.New("azurerm refresh hook: Refresher is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = sysevent.Default()
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	return func(ctx context.Context, info WorkDirInfo) error {
		match, err := isAzureRMProviderModule(info.WorkDir)
		if err != nil {
			sysevent.Emitf(logger, "azurerm_refresh_hook_inspect_failed", "card_id=%s work_dir=%s err=%v",
				info.CardID, info.WorkDir, err)
			return fmt.Errorf("inspect go.mod under %s: %w", info.WorkDir, err)
		}
		if !match {
			sysevent.Emitf(logger, "azurerm_refresh_hook_skip", "card_id=%s work_dir=%s reason=not_azurerm_provider",
				info.CardID, info.WorkDir)
			return nil
		}

		callCtx := ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		issueNumber, resolveErr := resolveAzureRMIssueNumber(callCtx, info, opts.CardInfoFetcher, logger)
		if resolveErr != nil {
			sysevent.Emitf(logger, "azurerm_refresh_hook_skip", "card_id=%s work_dir=%s reason=issue_number_unresolved err=%v",
				info.CardID, info.WorkDir, resolveErr)
			return nil
		}

		sysevent.Emitf(logger, "azurerm_refresh_hook_invoking", "card_id=%s work_dir=%s issue=%s",
			info.CardID, info.WorkDir, issueNumber)
		start := time.Now()
		err = opts.Refresher.Refresh(callCtx, aiassistedrefresh.Options{
			RepoDirectory: info.WorkDir,
			Issue:         issueNumber,
		})
		if err != nil {
			sysevent.Emitf(logger, "azurerm_refresh_hook_failed", "card_id=%s work_dir=%s err=%v",
				info.CardID, info.WorkDir, err)
			return fmt.Errorf("refresh azurerm work_dir %s: %w", info.WorkDir, err)
		}
		sysevent.Emitf(logger, "azurerm_refresh_hook_done", "card_id=%s duration=%s",
			info.CardID, time.Since(start))
		return nil
	}, nil
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

// resolveAzureRMIssueNumber finds the integer GitHub issue/PR number
// associated with info.CardID. It prefers the gateway's own
// classification (already populated by classifyForWorker before the
// hook fires) and only falls back to a Trello fetch when the
// classification is missing.
func resolveAzureRMIssueNumber(ctx context.Context, info WorkDirInfo, fetcher CardInfoFetcher, logger sysevent.Sink) (string, error) {
	if n := strings.TrimSpace(info.Classification.GitHub.Number); n != "" {
		return n, nil
	}
	if fetcher == nil {
		return "", errors.New("no classification.GitHub.Number and no CardInfoFetcher configured")
	}
	text, err := fetcher(ctx, info.CardID)
	if err != nil {
		return "", fmt.Errorf("fetch card info: %w", err)
	}
	cls := classifyGitHubRef(text)
	if n := strings.TrimSpace(cls.GitHub.Number); n != "" {
		return n, nil
	}
	sysevent.Emitf(logger, "azurerm_refresh_hook_classify_no_number", "card_id=%s text_bytes=%d",
		info.CardID, len(text))
	return "", errors.New("could not extract issue number from card description")
}
