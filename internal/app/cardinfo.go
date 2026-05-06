package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// CardInfoFetcher returns text from a Trello card that ClassifyCard can
// scan for a GitHub URL (the line/body the gateway uses to derive
// work_type, owner/repo, issue/PR number). Implementations typically
// return the card's first description line concatenated with the full
// description so the regex in ClassifyCard finds the URL even when it is
// not on the first line. Returning an empty string with a nil error is
// acceptable when the card has no description text.
// Implementations must be safe to call concurrently from multiple
// goroutines.
type CardInfoFetcher func(ctx context.Context, cardID string) (cardText string, err error)

// trelloCardInfo mirrors the JSON shape produced by
// scripts/trello-get-card-info.ps1 in the workspace-trello-router repo.
type trelloCardInfo struct {
	Name      string `json:"name"`
	Desc      string `json:"desc"`
	FirstLine string `json:"firstLine"`
}

// NewScriptCardInfoFetcher returns a CardInfoFetcher that shells out to
// trello-get-card-info.ps1 under <routerDir>/scripts/. The returned
// fetcher inherits TRELLO_API_KEY / TRELLO_API_TOKEN from the gateway
// process environment (the script needs them to call api.trello.com).
func NewScriptCardInfoFetcher(routerDir string) CardInfoFetcher {
	scriptPath := filepath.Join(routerDir, "scripts", "trello-get-card-info.ps1")
	return func(ctx context.Context, cardID string) (string, error) {
		if cardID == "" {
			return "", errors.New("cardID is empty")
		}
		// Use pwsh.exe explicitly: PowerShell 7+ is the supported runtime for
		// the workspace-trello-router scripts. -NoProfile keeps invocation
		// cheap and predictable across operator machines.
		cmd := exec.CommandContext(ctx, "pwsh.exe",
			"-NoProfile", "-NonInteractive", "-File", scriptPath, "-CardId", cardID)
		out, err := cmd.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return "", fmt.Errorf("trello-get-card-info.ps1 failed (exit %d): %s",
					ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
			}
			return "", fmt.Errorf("invoke trello-get-card-info.ps1: %w", err)
		}
		var info trelloCardInfo
		if err := json.Unmarshal(out, &info); err != nil {
			return "", fmt.Errorf("parse trello-get-card-info.ps1 output: %w (output=%q)", err, string(out))
		}
		// Return name + firstLine + full desc so ClassifyCard's regex finds a
		// GitHub URL regardless of whether it sits on the first line, in the
		// title, or further down the description.
		parts := []string{
			strings.TrimSpace(info.Name),
			strings.TrimSpace(info.FirstLine),
			strings.TrimSpace(info.Desc),
		}
		return strings.TrimSpace(strings.Join(parts, "\n")), nil
	}
}
