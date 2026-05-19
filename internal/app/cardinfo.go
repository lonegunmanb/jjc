package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lonegunmanb/jjc/internal/app/router"
	"github.com/lonegunmanb/jjc/internal/app/trelloclient"
)

// CardInfoFetcher returns text from a Trello card that fallback GitHub parsing
// can scan for a URL (the line/body the gateway uses to derive
// owner/repo, issue/PR number). Implementations typically
// return the card's first description line concatenated with the full
// description so the regex finds the URL even when it is
// not on the first line. Returning an empty string with a nil error is
// acceptable when the card has no description text.
// Implementations must be safe to call concurrently from multiple
// goroutines.
type CardInfoFetcher func(ctx context.Context, cardID string) (cardText string, err error)

// CardSignalsFetcher returns the structured per-card fields visible to HCL
// `rule {}` expressions.
type CardSignalsFetcher func(ctx context.Context, cardID string) (router.CardSignals, error)

// NewSDKCardInfoFetcher returns a CardInfoFetcher backed by the
// project-local Trello SDK wrapper. It replaces the previous
// PowerShell/script implementation: every call hits api.trello.com
// directly via the in-process HTTP client, so the gateway no longer
// has to fork pwsh.exe (and no longer requires PowerShell on the host).
//
// The returned text concatenates name + firstLine + desc — same shape
// the script-based fetcher used to produce — so GitHub URL parsing
// finds the GitHub URL whether it sits in the title, on the first
// description line, or further down the body.
func NewSDKCardInfoFetcher(c trelloclient.Client) CardInfoFetcher {
	if c == nil {
		// Refuse to construct a fetcher that can never succeed: callers
		// would be silently fed empty strings, defeating GitHub URL parsing.
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

// NewSDKCardSignalsFetcher returns a structured rule-input fetcher backed by
// the project-local Trello SDK wrapper.
func NewSDKCardSignalsFetcher(c trelloclient.Client) CardSignalsFetcher {
	if c == nil {
		return func(context.Context, string) (router.CardSignals, error) {
			return router.CardSignals{}, errors.New("trelloclient is nil")
		}
	}
	return func(ctx context.Context, cardID string) (router.CardSignals, error) {
		if cardID == "" {
			return router.CardSignals{}, errors.New("cardID is empty")
		}
		card, err := c.GetCard(ctx, cardID)
		if err != nil {
			return router.CardSignals{}, fmt.Errorf("fetch trello card %s: %w", cardID, err)
		}
		var listName string
		if list, lerr := c.GetCardList(ctx, cardID); lerr == nil {
			listName = strings.TrimSpace(list.Name)
		}
		return router.CardSignals{
			ID:        card.ID,
			Name:      strings.TrimSpace(card.Name),
			ListName:  listName,
			FirstLine: strings.TrimSpace(card.FirstLine),
			Labels:    []string{},
		}, nil
	}
}
