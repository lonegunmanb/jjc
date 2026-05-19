package kanban

import (
	"context"
	"errors"
	"fmt"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

// LoadAndResolve is the single entry point main wires at startup. It
//
//  1. Parses the `kanban {}` block out of router.hcl at hclPath.
//  2. Asks fetcher for the open lists on boardID.
//  3. Resolves every role name to a unique board list ID.
//  4. Logs every unclaimed board list as a WARN-style line (see the
//     issue's "Board lists not claimed by any role → default to wait
//     category" decision).
//
// boardID must be non-empty; an empty value returns an error that the
// caller can log as `event=kanban_board_id_missing`. A failure inside
// the Trello fetch is returned wrapped so the caller can log it as
// `event=trello_board_lists_fetch_failed`. A failure inside Resolve is
// a *ResolveError suitable for `event=kanban_resolve_failed`.
//
// `logger` may be nil; the function defaults to sysevent.Default() for the
// unclaimed-list WARN lines.
func LoadAndResolve(ctx context.Context, hclPath, boardID string, fetcher BoardListsFetcher, logger sysevent.Sink) (*Resolved, error) {
	if boardID == "" {
		return nil, errors.New("kanban: board_id is empty")
	}
	if fetcher == nil {
		return nil, errors.New("kanban: fetcher is nil")
	}
	if logger == nil {
		logger = sysevent.Default()
	}

	cfg, err := LoadConfig(hclPath)
	if err != nil {
		return nil, err
	}

	lists, err := fetcher.ListBoardLists(ctx, boardID)
	if err != nil {
		return nil, fmt.Errorf("kanban: fetch board lists: %w", err)
	}

	resolved, err := Resolve(boardID, cfg, lists)
	if err != nil {
		return nil, err
	}

	// Issue requirement: log every unclaimed board list at startup so
	// the operator notices a board column that no role claimed.
	// Build a single name → id index up front so the WARN loop is O(n).
	idByName := make(map[string]string, len(lists))
	for _, l := range lists {
		idByName[l.Name] = l.ID
	}
	for _, name := range resolved.UnclaimedListNames {
		sysevent.Emitf(logger, "kanban_unclaimed_list", "name=%q id=%s fallback_category=wait",
			name, idByName[name])
	}

	return resolved, nil
}
