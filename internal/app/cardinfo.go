package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lonegunmanb/trello-copilot/internal/app/trelloclient"
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

// NewSDKCardInfoFetcher returns a CardInfoFetcher backed by the
// project-local Trello SDK wrapper. It replaces the previous
// PowerShell/script implementation: every call hits api.trello.com
// directly via the in-process HTTP client, so the gateway no longer
// has to fork pwsh.exe (and no longer requires PowerShell on the host).
//
// The returned text concatenates name + firstLine + desc — same shape
// the script-based fetcher used to produce — so ClassifyCard's regex
// finds the GitHub URL whether it sits in the title, on the first
// description line, or further down the body.
func NewSDKCardInfoFetcher(c trelloclient.Client) CardInfoFetcher {
	if c == nil {
		// Refuse to construct a fetcher that can never succeed: callers
		// would be silently fed empty strings, defeating ClassifyCard.
		return func(context.Context, string) (string, error) {
			return "", errors.New("trelloclient is nil")
		}
	}
	return func(ctx context.Context, cardID string) (string, error) {
		if cardID == "" {
			return "", errors.New("cardID is empty")
		}
		card, err := c.GetCard(ctx, cardID)
		if err != nil {
			return "", fmt.Errorf("fetch trello card %s: %w", cardID, err)
		}
		parts := []string{
			strings.TrimSpace(card.Name),
			strings.TrimSpace(card.FirstLine),
			strings.TrimSpace(card.Desc),
		}
		return strings.TrimSpace(strings.Join(parts, "\n")), nil
	}
}
