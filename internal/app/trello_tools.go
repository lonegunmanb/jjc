package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/lonegunmanb/trello-copilot/internal/app/trelloclient"
)

// BuildTrelloTools returns the slice of copilot.Tool definitions that
// the gateway registers on every per-card worker session. Each tool is
// a thin wrapper around one trelloclient method, named per the
// trello-copilot tool vocabulary (`trello_*` snake_case verbs).
//
// Read-only tools (GET) set SkipPermission = true so the worker doesn't
// stall on a permission prompt for harmless lookups; write tools
// (comment / move) leave it false and route through the session's
// permission handler — callers stay in control of every Trello mutation.
//
// Returns an empty slice when client is nil so callers (and tests)
// don't have to special-case that.
func BuildTrelloTools(client trelloclient.Client, logger *log.Logger) []copilot.Tool {
	if client == nil {
		return nil
	}
	if logger == nil {
		logger = log.Default()
	}

	cardGet := copilot.DefineTool(
		"trello_card_get",
		"Fetch a Trello card's metadata. Returns id, name, desc, firstLine, idList and idBoard. The Go gateway hosts the Trello credentials; do NOT inline `Invoke-RestMethod` calls.",
		func(p trelloCardGetParams, _ copilot.ToolInvocation) (any, error) {
			if err := p.validate(); err != nil {
				return nil, err
			}
			card, err := client.GetCard(invocCtx(), p.CardID)
			logToolEvent(logger, "trello_card_get", p.CardID, err)
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
		func(p trelloCardGetParams, _ copilot.ToolInvocation) (any, error) {
			if err := p.validate(); err != nil {
				return nil, err
			}
			lst, err := client.GetCardList(invocCtx(), p.CardID)
			logToolEvent(logger, "trello_card_list", p.CardID, err)
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
		func(p trelloBoardListsParams, _ copilot.ToolInvocation) (any, error) {
			if err := p.validate(); err != nil {
				return nil, err
			}
			lists, err := client.ListBoardLists(invocCtx(), p.BoardID)
			logToolEvent(logger, "trello_board_lists", p.BoardID, err)
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
		func(p trelloCardMoveParams, _ copilot.ToolInvocation) (any, error) {
			if err := p.validate(); err != nil {
				logToolEvent(logger, "trello_card_move", p.CardID, err)
				return nil, err
			}
			res, err := moveCard(invocCtx(), client, p)
			logToolEvent(logger, "trello_card_move", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	)

	cardComment := copilot.DefineTool(
		"trello_card_comment",
		"Post a new comment on a Trello card. Returns {id, text, by, at} of the created comment. Use this instead of an `Invoke-RestMethod` block targeting api.trello.com.",
		func(p trelloCardCommentParams, _ copilot.ToolInvocation) (any, error) {
			if err := p.validate(); err != nil {
				logToolEvent(logger, "trello_card_comment", p.CardID, err)
				return nil, err
			}
			cmt, err := client.AddComment(invocCtx(), p.CardID, p.Text)
			logToolEvent(logger, "trello_card_comment", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return cmt, nil
		},
	)

	cardLatestComment := copilot.DefineTool(
		"trello_card_latest_comment",
		"Return the most recent comment on a Trello card. Returns {id, text, by, at}; errors when the card has no comments.",
		func(p trelloCardGetParams, _ copilot.ToolInvocation) (any, error) {
			if err := p.validate(); err != nil {
				return nil, err
			}
			cmt, err := client.GetLatestComment(invocCtx(), p.CardID)
			logToolEvent(logger, "trello_card_latest_comment", p.CardID, err)
			if err != nil {
				return nil, err
			}
			return cmt, nil
		},
	)
	cardLatestComment.SkipPermission = true

	cardCommentsSince := copilot.DefineTool(
		"trello_card_comments_since",
		"Return every comment posted strictly after the supplied RFC3339 timestamp, oldest-first. Pass an empty `since` to return one page of recent comments.",
		func(p trelloCardCommentsSinceParams, _ copilot.ToolInvocation) (any, error) {
			if err := p.validate(); err != nil {
				return nil, err
			}
			var since time.Time
			if p.Since != "" {
				t, err := time.Parse(time.RFC3339, p.Since)
				if err != nil {
					return nil, fmt.Errorf("invalid since timestamp %q: %w", p.Since, err)
				}
				since = t
			}
			cmts, err := client.ListCommentsSince(invocCtx(), p.CardID, since)
			logToolEvent(logger, "trello_card_comments_since", p.CardID, err)
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

// logToolEvent emits a structured log line for every Trello tool call
// so post-incident the operator can reconstruct what the worker did.
// We log the same `event=trello_<tool>_ok|failed` shape regardless of
// whether the SDK round-tripped or not.
func logToolEvent(logger *log.Logger, tool, target string, err error) {
	if err != nil {
		logger.Printf("event=%s_failed target=%s err=%v", tool, target, err)
		return
	}
	logger.Printf("event=%s_ok target=%s", tool, target)
}

// invocCtx returns the context the tool handler should use. The current
// copilot-sdk Go API does not surface a per-invocation context on
// ToolInvocation, so we fall back to a fresh background context with a
// generous timeout. If/when the SDK adds invocation.Ctx() this becomes
// a one-line update.
func invocCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	// Cancel is intentionally fire-and-forget at handler exit; the
	// timeout still fires even if the handler returns early.
	_ = cancel
	return ctx
}

// trelloCardGetParams matches the JSON args for tools that operate on
// a single card by id (trello_card_get, trello_card_list,
// trello_card_latest_comment).
type trelloCardGetParams struct {
	CardID string `json:"card_id" jsonschema:"required,Trello card id"`
}

func (p trelloCardGetParams) validate() error {
	if strings.TrimSpace(p.CardID) == "" {
		return errors.New("card_id is required")
	}
	return nil
}

type trelloBoardListsParams struct {
	BoardID string `json:"board_id" jsonschema:"required,Trello board id"`
}

func (p trelloBoardListsParams) validate() error {
	if strings.TrimSpace(p.BoardID) == "" {
		return errors.New("board_id is required")
	}
	return nil
}

type trelloCardCommentParams struct {
	CardID string `json:"card_id" jsonschema:"required,Trello card id"`
	Text   string `json:"text" jsonschema:"required,Comment body. Markdown is supported by Trello."`
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
	CardID         string `json:"card_id" jsonschema:"required,Trello card id"`
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
	CardID string `json:"card_id" jsonschema:"required,Trello card id"`
	Since  string `json:"since,omitempty" jsonschema:"RFC3339 cutoff timestamp; empty returns one page of recent comments."`
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
func moveCard(ctx context.Context, client trelloclient.Client, p trelloCardMoveParams) (trelloCardMoveResult, error) {
	card, err := client.GetCard(ctx, p.CardID)
	if err != nil {
		return trelloCardMoveResult{}, fmt.Errorf("look up card %s: %w", p.CardID, err)
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
		lists, err := client.ListBoardLists(ctx, card.IDBoard)
		if err != nil {
			return trelloCardMoveResult{}, fmt.Errorf("list board %s lists: %w", card.IDBoard, err)
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
		// We moved by id and didn't pre-fetch board lists; do a cheap
		// extra GET so the result is human-readable.
		if name, ok := lookupListName(ctx, client, card.IDBoard, targetID); ok {
			to.Name = name
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

// lookupListName best-effort resolves a list id to its name by listing
// the board's lists. Returns ("", false) on any error so move
// reporting is graceful when the lookup fails.
func lookupListName(ctx context.Context, client trelloclient.Client, boardID, listID string) (string, bool) {
	lists, err := client.ListBoardLists(ctx, boardID)
	if err != nil {
		return "", false
	}
	for _, l := range lists {
		if l.ID == listID {
			return l.Name, true
		}
	}
	return "", false
}

// JSONForToolResult is a helper used by tests to assert on the JSON
// shape a tool would emit.
func JSONForToolResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
