package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/lonegunmanb/jjc/internal/app/sysevent"
	"github.com/lonegunmanb/jjc/internal/app/trelloclient"
)

// trelloToolTimeout caps every individual Trello tool call. The tool
// handler also accepts the parent context delivered by the SDK (via
// withTimeout below), so the effective timeout is min(parent, 30s).
const trelloToolTimeout = 30 * time.Second

// BuildTrelloTools returns the slice of copilot.Tool definitions that
// the gateway registers on every per-card worker session. Each tool is
// a thin wrapper around one trelloclient method, named per the
// JJC tool vocabulary (`trello_*` snake_case verbs).
//
// Read-only tools (GET) set SkipPermission = true so the worker doesn't
// stall on a permission prompt for harmless lookups; write tools
// (comment / move) leave it false and route through the session's
// permission handler — callers stay in control of every Trello mutation.
//
// Threat model for SkipPermission tools (`trello_card_get`,
// `trello_card_list`, `trello_board_lists`, `trello_card_latest_comment`,
// `trello_card_comments_since`):
//
//   - The `card_id` / `board_id` arguments are NOT pinned to the card
//     the worker was spawned for. A prompt-injection that talks the
//     worker into reading another card on the same board succeeds
//     silently.
//   - The only real perimeter is the Trello API key/token's own scope:
//     the gateway only grants access to whatever boards that token can
//     already see. Operators who wire the gateway up against a token
//     with broad workspace access should review whether they're
//     comfortable with that.
//   - We accept this trade-off because the alternative (prompt the
//     operator on every card-read) would block every dispatch on
//     several seconds of human latency for no real safety win — the
//     worker can already exfiltrate via the write tools we DO gate.
//     Write tools (`trello_card_move`, `trello_card_comment`) keep the
//     permission prompt precisely so a hijacked worker can't quietly
//     mutate Trello state.
//
// Returns an empty slice when client is nil so callers (and tests)
// don't have to special-case that.
func BuildTrelloTools(client trelloclient.Client, logger sysevent.Sink) []copilot.Tool {
	if client == nil {
		return nil
	}
	if logger == nil {
		logger = sysevent.Default()
	}

	cardGet := copilot.DefineTool(
		"trello_card_get",
		"Fetch a Trello card's metadata. Returns id, name, desc, firstLine, idList and idBoard. The Go gateway hosts the Trello credentials; do NOT inline `Invoke-RestMethod` calls.",
		func(p trelloCardGetParams, inv copilot.ToolInvocation) (any, error) {
			ctx, cancel := withTimeout(inv)
			defer cancel()
			if err := p.validate(); err != nil {
				logToolEvent(logger, inv, "trello_card_get", p.CardID, err)
				return nil, err
			}
			card, err := client.GetCard(ctx, p.CardID)
			logToolEvent(logger, inv, "trello_card_get", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return card, nil
		},
	)
	cardGet.SkipPermission = true

	cardList := copilot.DefineTool(
		"trello_card_list",
		"Look up the Trello list (column) a card currently sits in. Returns {id, name}.",
		func(p trelloCardGetParams, inv copilot.ToolInvocation) (any, error) {
			ctx, cancel := withTimeout(inv)
			defer cancel()
			if err := p.validate(); err != nil {
				logToolEvent(logger, inv, "trello_card_list", p.CardID, err)
				return nil, err
			}
			lst, err := client.GetCardList(ctx, p.CardID)
			logToolEvent(logger, inv, "trello_card_list", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return lst, nil
		},
	)
	cardList.SkipPermission = true

	boardLists := copilot.DefineTool(
		"trello_board_lists",
		"Return every list (column) on a board, with id and name. Useful for resolving a list name to an id before calling trello_card_move.",
		func(p trelloBoardListsParams, inv copilot.ToolInvocation) (any, error) {
			ctx, cancel := withTimeout(inv)
			defer cancel()
			if err := p.validate(); err != nil {
				logToolEvent(logger, inv, "trello_board_lists", p.BoardID, err)
				return nil, err
			}
			lists, err := client.ListBoardLists(ctx, p.BoardID)
			logToolEvent(logger, inv, "trello_board_lists", p.BoardID, err)
			if err != nil {
				return nil, err
			}
			return lists, nil
		},
	)
	boardLists.SkipPermission = true

	cardMove := copilot.DefineTool(
		"trello_card_move",
		"Move a Trello card to a different list. Pass either target_list_id (preferred) or target_list_name (resolved against the card's board). Returns {from:{id,name}, to:{id,name}}.",
		func(p trelloCardMoveParams, inv copilot.ToolInvocation) (any, error) {
			ctx, cancel := withTimeout(inv)
			defer cancel()
			if err := p.validate(); err != nil {
				logToolEvent(logger, inv, "trello_card_move", p.CardID, err)
				return nil, err
			}
			res, err := moveCard(ctx, client, p)
			logToolEvent(logger, inv, "trello_card_move", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	)

	cardComment := copilot.DefineTool(
		"trello_card_comment",
		"Post a new comment on a Trello card. Returns {id, text, by, at} of the created comment. Use this instead of an `Invoke-RestMethod` block targeting api.trello.com.",
		func(p trelloCardCommentParams, inv copilot.ToolInvocation) (any, error) {
			ctx, cancel := withTimeout(inv)
			defer cancel()
			if err := p.validate(); err != nil {
				logToolEvent(logger, inv, "trello_card_comment", p.CardID, err)
				return nil, err
			}
			cmt, err := client.AddComment(ctx, p.CardID, p.Text)
			logToolEvent(logger, inv, "trello_card_comment", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return cmt, nil
		},
	)

	cardLatestComment := copilot.DefineTool(
		"trello_card_latest_comment",
		"Return the most recent comment on a Trello card. Returns {id, text, by, at}; errors when the card has no comments.",
		func(p trelloCardGetParams, inv copilot.ToolInvocation) (any, error) {
			ctx, cancel := withTimeout(inv)
			defer cancel()
			if err := p.validate(); err != nil {
				logToolEvent(logger, inv, "trello_card_latest_comment", p.CardID, err)
				return nil, err
			}
			cmt, err := client.GetLatestComment(ctx, p.CardID)
			logToolEvent(logger, inv, "trello_card_latest_comment", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return cmt, nil
		},
	)
	cardLatestComment.SkipPermission = true

	cardCommentsSince := copilot.DefineTool(
		"trello_card_comments_since",
		"Return every comment posted strictly after the supplied RFC3339 timestamp, oldest-first. Pass an empty `since` to return the full history of comments on the card (paged through transparently).",
		func(p trelloCardCommentsSinceParams, inv copilot.ToolInvocation) (any, error) {
			ctx, cancel := withTimeout(inv)
			defer cancel()
			if err := p.validate(); err != nil {
				logToolEvent(logger, inv, "trello_card_comments_since", p.CardID, err)
				return nil, err
			}
			var since time.Time
			if p.Since != "" {
				t, err := time.Parse(time.RFC3339, p.Since)
				if err != nil {
					err = fmt.Errorf("invalid since timestamp %q: %w", p.Since, err)
					logToolEvent(logger, inv, "trello_card_comments_since", p.CardID, err)
					return nil, err
				}
				since = t
			}
			cmts, err := client.ListCommentsSince(ctx, p.CardID, since)
			logToolEvent(logger, inv, "trello_card_comments_since", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return cmts, nil
		},
	)
	cardCommentsSince.SkipPermission = true

	return []copilot.Tool{
		cardGet,
		cardList,
		boardLists,
		cardMove,
		cardComment,
		cardLatestComment,
		cardCommentsSince,
	}
}

// withTimeout returns a derived context that will be cancelled by the
// caller's defer (preventing the timer leak `go vet` would otherwise
// flag as a lostcancel). The parent is intentionally context.Background:
// the SDK's ToolInvocation only carries TraceContext for OTel
// propagation today, not a cancellation signal — using it as the parent
// would tie tool execution to a span lifetime that may already be
// closed when the handler runs.
//
// When a future copilot-sdk release surfaces a real per-invocation
// cancellation context, this is the single place to swap the parent.
func withTimeout(_ copilot.ToolInvocation) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), trelloToolTimeout)
}

// logToolEvent emits a structured log line for every Trello tool call
// so post-incident the operator can reconstruct what the worker did
// (and which session triggered it).
func logToolEvent(logger sysevent.Sink, inv copilot.ToolInvocation, tool, target string, err error) {
	session := inv.SessionID
	if session == "" {
		session = "-"
	}
	call := inv.ToolCallID
	if call == "" {
		call = "-"
	}
	if err != nil {
		sysevent.Emitf(logger, tool+"_failed", "session=%s call=%s target=%s err=%v", session, call, target, err)
		return
	}
	sysevent.Emitf(logger, tool+"_ok", "session=%s call=%s target=%s", session, call, target)
}

// trelloCardGetParams matches the JSON args for tools that operate on
// a single card by id (trello_card_get, trello_card_list,
// trello_card_latest_comment).
//
// Note on jsonschema tags: github.com/google/jsonschema-go treats the
// whole `jsonschema:` value as a description. Required vs optional is
// derived from the json tag's `omitempty`/`omitzero` settings (no
// omitempty == required), so we do NOT prefix descriptions with
// "required,". The struct-level `validate()` method enforces it again
// at runtime.
type trelloCardGetParams struct {
	CardID string `json:"card_id" jsonschema:"Trello card id (24-char hex)"`
}

func (p trelloCardGetParams) validate() error {
	if strings.TrimSpace(p.CardID) == "" {
		return errors.New("card_id is required")
	}
	return nil
}

type trelloBoardListsParams struct {
	BoardID string `json:"board_id" jsonschema:"Trello board id (24-char hex)"`
}

func (p trelloBoardListsParams) validate() error {
	if strings.TrimSpace(p.BoardID) == "" {
		return errors.New("board_id is required")
	}
	return nil
}

type trelloCardCommentParams struct {
	CardID string `json:"card_id" jsonschema:"Trello card id (24-char hex)"`
	Text   string `json:"text" jsonschema:"Comment body. Markdown is supported by Trello."`
}

func (p trelloCardCommentParams) validate() error {
	if strings.TrimSpace(p.CardID) == "" {
		return errors.New("card_id is required")
	}
	if strings.TrimSpace(p.Text) == "" {
		return errors.New("text is required")
	}
	return nil
}

type trelloCardMoveParams struct {
	CardID         string `json:"card_id" jsonschema:"Trello card id (24-char hex)"`
	TargetListID   string `json:"target_list_id,omitempty" jsonschema:"Target list id. Preferred over target_list_name when known."`
	TargetListName string `json:"target_list_name,omitempty" jsonschema:"Target list name. The handler resolves it against the card's board."`
}

func (p trelloCardMoveParams) validate() error {
	if strings.TrimSpace(p.CardID) == "" {
		return errors.New("card_id is required")
	}
	if strings.TrimSpace(p.TargetListID) == "" && strings.TrimSpace(p.TargetListName) == "" {
		return errors.New("either target_list_id or target_list_name must be set")
	}
	return nil
}

type trelloCardCommentsSinceParams struct {
	CardID string `json:"card_id" jsonschema:"Trello card id (24-char hex)"`
	Since  string `json:"since,omitempty" jsonschema:"RFC3339 cutoff timestamp; empty returns the full comment history on the card."`
}

func (p trelloCardCommentsSinceParams) validate() error {
	if strings.TrimSpace(p.CardID) == "" {
		return errors.New("card_id is required")
	}
	return nil
}

// trelloCardMoveResult is the JSON shape returned by trello_card_move.
type trelloCardMoveResult struct {
	From trelloclient.List `json:"from"`
	To   trelloclient.List `json:"to"`
}

// moveCard implements the trello_card_move tool: it reads the card's
// current state, resolves a target list name when necessary, then
// issues PUT /cards/{id}. Returning {from, to} keeps the audit trail
// rich enough that operators can reconstruct the move from a single
// log line.
//
// We fetch the board's lists at most once: when moving by name the
// listing is mandatory; when moving by id we still consult the list
// once to populate `to.Name` / `from.Name` for the audit log, but only
// when the move actually went out (no-op moves skip the extra GET).
func moveCard(ctx context.Context, client trelloclient.Client, p trelloCardMoveParams) (trelloCardMoveResult, error) {
	card, err := client.GetCard(ctx, p.CardID)
	if err != nil {
		return trelloCardMoveResult{}, fmt.Errorf("look up card %s: %w", p.CardID, err)
	}

	var (
		boardLists      []trelloclient.List
		boardListsCache bool
	)
	listsForBoard := func() ([]trelloclient.List, error) {
		if boardListsCache {
			return boardLists, nil
		}
		ls, err := client.ListBoardLists(ctx, card.IDBoard)
		if err != nil {
			return nil, fmt.Errorf("list board %s lists: %w", card.IDBoard, err)
		}
		boardLists = ls
		boardListsCache = true
		return boardLists, nil
	}

	from, fromErr := client.GetCardList(ctx, p.CardID)
	if fromErr != nil {
		// Surface but don't fail: callers usually still want the move.
		from = trelloclient.List{ID: card.IDList}
	}

	targetID := strings.TrimSpace(p.TargetListID)
	var to trelloclient.List
	if targetID != "" {
		to.ID = targetID
	} else {
		lists, err := listsForBoard()
		if err != nil {
			return trelloCardMoveResult{}, err
		}
		match := findListByName(lists, p.TargetListName)
		if match == nil {
			return trelloCardMoveResult{}, fmt.Errorf("no list named %q on board %s", p.TargetListName, card.IDBoard)
		}
		targetID = match.ID
		to = *match
	}
	if targetID == card.IDList {
		// No-op: don't issue the PUT, just return current state. This
		// keeps the tool idempotent and saves a round-trip when the
		// worker's view of "current list" lags reality.
		if to.Name == "" {
			to.Name = from.Name
		}
		return trelloCardMoveResult{From: from, To: to}, nil
	}
	if err := client.MoveCard(ctx, p.CardID, targetID); err != nil {
		return trelloCardMoveResult{}, err
	}
	if to.Name == "" {
		// Reuse the cached board listing if we already fetched it
		// above; otherwise fetch once for audit-log readability.
		if lists, lerr := listsForBoard(); lerr == nil {
			if name, ok := findListID(lists, targetID); ok {
				to.Name = name
			}
		}
	}
	return trelloCardMoveResult{From: from, To: to}, nil
}

func findListByName(lists []trelloclient.List, name string) *trelloclient.List {
	want := strings.ToLower(strings.TrimSpace(name))
	for i := range lists {
		if strings.ToLower(lists[i].Name) == want {
			return &lists[i]
		}
	}
	return nil
}

// findListID returns the name of the list with the given id, or
// ("", false) when not present.
func findListID(lists []trelloclient.List, listID string) (string, bool) {
	for _, l := range lists {
		if l.ID == listID {
			return l.Name, true
		}
	}
	return "", false
}
