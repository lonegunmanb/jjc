// Package trelloclient is the project-local wrapper around the
// upstream go-trello-sdk. It exposes only the operations the
// trello-copilot gateway needs (card metadata, comments, list
// management, list lookups) behind a small interface so the rest of the
// codebase does not depend on the generated SDK directly and so tests
// can substitute a fake implementation without standing up an HTTP
// server.
//
// The wrapper deliberately decodes responses into the small structs in
// this package rather than the SDK's generated models. The upstream
// OpenAPI spec marks several response fields (e.g. dateLastActivity) as
// `format: date` while the live API returns full RFC3339 date-times,
// which breaks the SDK's typed `…WithResponse` helpers. Decoding only
// the fields we care about keeps the wrapper resilient to spec drift
// (this is the same trade-off the SDK's own acceptance tests make).
//
// All methods on Client are safe to call concurrently from multiple
// goroutines — the underlying *http.Client is goroutine-safe and we
// hold no per-call mutable state.
package trelloclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	trellosdk "github.com/lonegunmanb/go-trello-sdk/trello"
)

// Card is the trimmed-down view of a Trello card surface the gateway
// uses. FirstLine is the first non-empty line of Desc, computed by the
// wrapper so callers don't have to.
type Card struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Desc      string `json:"desc"`
	FirstLine string `json:"firstLine"`
	IDList    string `json:"idList"`
	IDBoard   string `json:"idBoard"`
}

// List is the trimmed-down view of a Trello list (column on a board).
type List struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Comment is a single Trello commentCard action with the fields useful
// for routing and audit logs.
type Comment struct {
	ID       string    `json:"id"`
	Text     string    `json:"text"`
	By       string    `json:"by"`     // member full name (best effort)
	ByID     string    `json:"by_id"`  // member id (best effort)
	At       time.Time `json:"at"`     // action timestamp
	Username string    `json:"username,omitempty"`
}

// Client is the operations surface that consumers see. It is small on
// purpose: every method maps directly to one or two REST calls and
// returns a small wrapper struct rather than the raw SDK type.
type Client interface {
	// GetCard fetches the card and returns name/desc/idList/idBoard plus
	// the first non-empty line of the description.
	GetCard(ctx context.Context, cardID string) (Card, error)

	// GetCardList fetches the list a card currently sits in.
	GetCardList(ctx context.Context, cardID string) (List, error)

	// ListBoardLists returns every (open) list on the board.
	ListBoardLists(ctx context.Context, boardID string) ([]List, error)

	// MoveCard updates a card's idList. The caller is responsible for
	// resolving a target list name to an id (see ListBoardLists or the
	// trello_card_move tool wrapper).
	MoveCard(ctx context.Context, cardID, targetListID string) error

	// AddComment posts a new commentCard action and returns the created
	// comment id. The returned Comment has ID/Text/At/By populated.
	AddComment(ctx context.Context, cardID, body string) (Comment, error)

	// GetLatestComment returns the most recent commentCard action on the
	// card, or an error wrapping ErrNoComments when there are none.
	GetLatestComment(ctx context.Context, cardID string) (Comment, error)

	// ListCommentsSince returns every commentCard action posted strictly
	// after `since`, oldest-first. since.IsZero() returns up to one page
	// of recent comments.
	ListCommentsSince(ctx context.Context, cardID string, since time.Time) ([]Comment, error)
}

// ErrNoComments is the sentinel returned (wrapped) by GetLatestComment
// when the card has no commentCard actions.
var ErrNoComments = errors.New("trelloclient: card has no comments")

// Option tunes New.
type Option func(*config) error

type config struct {
	apiKey  string
	apiToken string
	server  string
	httpDoer trellosdk.HttpRequestDoer
	logger  *log.Logger
}

// WithCredentials supplies the API key/token Trello requires. Required.
func WithCredentials(apiKey, apiToken string) Option {
	return func(c *config) error {
		if apiKey == "" || apiToken == "" {
			return errors.New("trelloclient: api key and token must both be non-empty")
		}
		c.apiKey, c.apiToken = apiKey, apiToken
		return nil
	}
}

// WithServer overrides the API base URL (defaults to the SDK default).
// Primarily useful in tests so the client points at httptest.Server.
func WithServer(server string) Option {
	return func(c *config) error {
		if server == "" {
			return errors.New("trelloclient: server must not be empty")
		}
		c.server = server
		return nil
	}
}

// WithHTTPClient overrides the http client. Optional.
func WithHTTPClient(doer trellosdk.HttpRequestDoer) Option {
	return func(c *config) error {
		if doer == nil {
			return errors.New("trelloclient: http doer must not be nil")
		}
		c.httpDoer = doer
		return nil
	}
}

// WithLogger installs the logger used for non-fatal warnings.
// Defaults to log.Default().
func WithLogger(logger *log.Logger) Option {
	return func(c *config) error {
		if logger != nil {
			c.logger = logger
		}
		return nil
	}
}

// New constructs a Client. WithCredentials is required for production
// use; tests that point WithServer at httptest.NewServer can omit
// credentials (the SDK skips the auth editor when both are empty).
func New(opts ...Option) (Client, error) {
	cfg := &config{logger: log.Default()}
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}
	sdkOpts := []trellosdk.Option{
		trellosdk.WithCredentials(cfg.apiKey, cfg.apiToken),
	}
	if cfg.server != "" {
		sdkOpts = append(sdkOpts, trellosdk.WithServer(cfg.server))
	}
	if cfg.httpDoer != nil {
		sdkOpts = append(sdkOpts, trellosdk.WithHTTPDoer(cfg.httpDoer))
	}
	sdkClient, err := trellosdk.New(sdkOpts...)
	if err != nil {
		return nil, fmt.Errorf("trelloclient: build sdk client: %w", err)
	}
	return &sdkBackedClient{sdk: sdkClient, logger: cfg.logger}, nil
}

// sdkBackedClient is the production implementation. It exists in this
// file (rather than next to a fake in another file) so the wire-level
// behaviour stays in one place.
type sdkBackedClient struct {
	sdk    *trellosdk.ClientWithResponses
	logger *log.Logger
}

func (c *sdkBackedClient) GetCard(ctx context.Context, cardID string) (Card, error) {
	if cardID == "" {
		return Card{}, errors.New("trelloclient: card id is empty")
	}
	fields := "id,name,desc,idList,idBoard"
	resp, err := c.sdk.GetCardsId(ctx, cardID, &trellosdk.GetCardsIdParams{Fields: &fields})
	if err != nil {
		return Card{}, fmt.Errorf("trelloclient: GET /cards/%s: %w", cardID, err)
	}
	body, err := readAndCheck(resp, http.StatusOK, "GET /cards/"+cardID)
	if err != nil {
		return Card{}, err
	}
	var raw struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Desc    string `json:"desc"`
		IDList  string `json:"idList"`
		IDBoard string `json:"idBoard"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Card{}, fmt.Errorf("trelloclient: decode card %s: %w", cardID, err)
	}
	return Card{
		ID:        raw.ID,
		Name:      raw.Name,
		Desc:      raw.Desc,
		FirstLine: firstNonEmptyLine(raw.Desc),
		IDList:    raw.IDList,
		IDBoard:   raw.IDBoard,
	}, nil
}

func (c *sdkBackedClient) GetCardList(ctx context.Context, cardID string) (List, error) {
	if cardID == "" {
		return List{}, errors.New("trelloclient: card id is empty")
	}
	fields := "id,name"
	resp, err := c.sdk.GetCardsIdList(ctx, cardID, &trellosdk.GetCardsIdListParams{Fields: &fields})
	if err != nil {
		return List{}, fmt.Errorf("trelloclient: GET /cards/%s/list: %w", cardID, err)
	}
	body, err := readAndCheck(resp, http.StatusOK, "GET /cards/"+cardID+"/list")
	if err != nil {
		return List{}, err
	}
	var l List
	if err := json.Unmarshal(body, &l); err != nil {
		return List{}, fmt.Errorf("trelloclient: decode list for card %s: %w", cardID, err)
	}
	return l, nil
}

func (c *sdkBackedClient) ListBoardLists(ctx context.Context, boardID string) ([]List, error) {
	if boardID == "" {
		return nil, errors.New("trelloclient: board id is empty")
	}
	fields := "id,name"
	resp, err := c.sdk.GetBoardsIdLists(ctx, boardID, &trellosdk.GetBoardsIdListsParams{Fields: &fields})
	if err != nil {
		return nil, fmt.Errorf("trelloclient: GET /boards/%s/lists: %w", boardID, err)
	}
	body, err := readAndCheck(resp, http.StatusOK, "GET /boards/"+boardID+"/lists")
	if err != nil {
		return nil, err
	}
	var lists []List
	if err := json.Unmarshal(body, &lists); err != nil {
		return nil, fmt.Errorf("trelloclient: decode lists for board %s: %w", boardID, err)
	}
	return lists, nil
}

func (c *sdkBackedClient) MoveCard(ctx context.Context, cardID, targetListID string) error {
	if cardID == "" {
		return errors.New("trelloclient: card id is empty")
	}
	if targetListID == "" {
		return errors.New("trelloclient: target list id is empty")
	}
	idList := trellosdk.TrelloID(targetListID)
	resp, err := c.sdk.PutCardsId(ctx, cardID, &trellosdk.PutCardsIdParams{IdList: &idList})
	if err != nil {
		return fmt.Errorf("trelloclient: PUT /cards/%s: %w", cardID, err)
	}
	if _, err := readAndCheck(resp, http.StatusOK, "PUT /cards/"+cardID); err != nil {
		return err
	}
	return nil
}

func (c *sdkBackedClient) AddComment(ctx context.Context, cardID, body string) (Comment, error) {
	if cardID == "" {
		return Comment{}, errors.New("trelloclient: card id is empty")
	}
	if body == "" {
		return Comment{}, errors.New("trelloclient: comment body is empty")
	}
	resp, err := c.sdk.PostCardsIdActionsComments(ctx, cardID,
		&trellosdk.PostCardsIdActionsCommentsParams{Text: body})
	if err != nil {
		return Comment{}, fmt.Errorf("trelloclient: POST /cards/%s/actions/comments: %w", cardID, err)
	}
	respBody, err := readAndCheck(resp, http.StatusOK, "POST /cards/"+cardID+"/actions/comments")
	if err != nil {
		return Comment{}, err
	}
	return decodeCommentAction(respBody)
}

func (c *sdkBackedClient) GetLatestComment(ctx context.Context, cardID string) (Comment, error) {
	if cardID == "" {
		return Comment{}, errors.New("trelloclient: card id is empty")
	}
	all, err := c.fetchCommentsPage(ctx, cardID)
	if err != nil {
		return Comment{}, err
	}
	if len(all) == 0 {
		return Comment{}, fmt.Errorf("trelloclient: card %s: %w", cardID, ErrNoComments)
	}
	// Trello returns comments newest-first; our slice is unsorted from
	// the API perspective so be explicit: pick the max .At.
	latest := all[0]
	for _, a := range all[1:] {
		if a.At.After(latest.At) {
			latest = a
		}
	}
	return latest, nil
}

func (c *sdkBackedClient) ListCommentsSince(ctx context.Context, cardID string, since time.Time) ([]Comment, error) {
	if cardID == "" {
		return nil, errors.New("trelloclient: card id is empty")
	}
	all, err := c.fetchCommentsPage(ctx, cardID)
	if err != nil {
		return nil, err
	}
	var out []Comment
	for _, a := range all {
		if since.IsZero() || a.At.After(since) {
			out = append(out, a)
		}
	}
	// Caller-friendly oldest-first ordering. Use insertion sort (tiny n).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].At.After(out[j].At); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

// fetchCommentsPage fetches the first page (up to 50 items, the Trello
// default) of commentCard actions. Pagination is intentionally not
// implemented: the gateway only consumes recent comments and a single
// page covers every realistic per-card volume.
func (c *sdkBackedClient) fetchCommentsPage(ctx context.Context, cardID string) ([]Comment, error) {
	filter := "commentCard"
	resp, err := c.sdk.GetCardsIdActions(ctx, cardID, &trellosdk.GetCardsIdActionsParams{Filter: &filter})
	if err != nil {
		return nil, fmt.Errorf("trelloclient: GET /cards/%s/actions: %w", cardID, err)
	}
	body, err := readAndCheck(resp, http.StatusOK, "GET /cards/"+cardID+"/actions")
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("trelloclient: decode actions for card %s: %w", cardID, err)
	}
	out := make([]Comment, 0, len(raw))
	for _, item := range raw {
		comment, err := decodeCommentAction(item)
		if err != nil {
			// Skip malformed actions but keep going so a single bad row
			// doesn't blank the whole page.
			c.logger.Printf("event=trelloclient_action_decode_error card_id=%s err=%v", cardID, err)
			continue
		}
		out = append(out, comment)
	}
	return out, nil
}

// decodeCommentAction handles both the "single action" envelope returned
// by POST /cards/{id}/actions/comments and the per-element shape inside
// the GET /cards/{id}/actions array.
func decodeCommentAction(body []byte) (Comment, error) {
	var raw struct {
		ID   string `json:"id"`
		Date string `json:"date"`
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
		MemberCreator struct {
			ID       string `json:"id"`
			FullName string `json:"fullName"`
			Username string `json:"username"`
		} `json:"memberCreator"`
		IDMemberCreator string `json:"idMemberCreator"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Comment{}, fmt.Errorf("trelloclient: decode action: %w", err)
	}
	c := Comment{
		ID:       raw.ID,
		Text:     raw.Data.Text,
		By:       raw.MemberCreator.FullName,
		ByID:     raw.MemberCreator.ID,
		Username: raw.MemberCreator.Username,
	}
	if c.ByID == "" {
		c.ByID = raw.IDMemberCreator
	}
	if raw.Date != "" {
		// Trello sends RFC3339 (with millisecond fraction). time.Parse
		// handles both with and without the fraction.
		if t, err := time.Parse(time.RFC3339, raw.Date); err == nil {
			c.At = t
		} else if t, err := time.Parse("2006-01-02T15:04:05.000Z", raw.Date); err == nil {
			c.At = t
		}
	}
	return c, nil
}

// readAndCheck closes resp.Body, validates the status code, and returns
// the body bytes. It returns a wrapped error that includes the response
// body (truncated) so callers can include it in audit logs.
func readAndCheck(resp *http.Response, want int, op string) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("trelloclient: %s: read body: %w", op, err)
	}
	if resp.StatusCode != want {
		preview := string(body)
		if len(preview) > 512 {
			preview = preview[:512] + "..."
		}
		return body, fmt.Errorf("trelloclient: %s: unexpected status %d: %s", op, resp.StatusCode, preview)
	}
	return body, nil
}

// firstNonEmptyLine returns the first line of s with leading/trailing
// whitespace trimmed, or "" if every line is blank. The classifier uses
// it as the "card title-ish" string to scan for a GitHub URL.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
