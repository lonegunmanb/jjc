package app

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/jjc/internal/app/trelloclient"
)

// fakeTrelloClient is a minimal trelloclient.Client for unit tests of
// the tool wiring. Each method's behaviour is configurable per test.
type fakeTrelloClient struct {
	getCard           func(ctx context.Context, id string) (trelloclient.Card, error)
	getCardList       func(ctx context.Context, id string) (trelloclient.List, error)
	listBoardLists    func(ctx context.Context, id string) ([]trelloclient.List, error)
	moveCard          func(ctx context.Context, id, listID string) error
	addComment        func(ctx context.Context, id, body string) (trelloclient.Comment, error)
	getLatestComment  func(ctx context.Context, id string) (trelloclient.Comment, error)
	listCommentsSince func(ctx context.Context, id string, since time.Time) ([]trelloclient.Comment, error)
}

func (f *fakeTrelloClient) GetCard(ctx context.Context, id string) (trelloclient.Card, error) {
	if f.getCard == nil {
		return trelloclient.Card{}, errors.New("getCard not implemented")
	}
	return f.getCard(ctx, id)
}
func (f *fakeTrelloClient) GetCardList(ctx context.Context, id string) (trelloclient.List, error) {
	if f.getCardList == nil {
		return trelloclient.List{}, errors.New("getCardList not implemented")
	}
	return f.getCardList(ctx, id)
}
func (f *fakeTrelloClient) ListBoardLists(ctx context.Context, id string) ([]trelloclient.List, error) {
	if f.listBoardLists == nil {
		return nil, errors.New("listBoardLists not implemented")
	}
	return f.listBoardLists(ctx, id)
}
func (f *fakeTrelloClient) MoveCard(ctx context.Context, id, listID string) error {
	if f.moveCard == nil {
		return errors.New("moveCard not implemented")
	}
	return f.moveCard(ctx, id, listID)
}
func (f *fakeTrelloClient) AddComment(ctx context.Context, id, body string) (trelloclient.Comment, error) {
	if f.addComment == nil {
		return trelloclient.Comment{}, errors.New("addComment not implemented")
	}
	return f.addComment(ctx, id, body)
}
func (f *fakeTrelloClient) GetLatestComment(ctx context.Context, id string) (trelloclient.Comment, error) {
	if f.getLatestComment == nil {
		return trelloclient.Comment{}, errors.New("getLatestComment not implemented")
	}
	return f.getLatestComment(ctx, id)
}
func (f *fakeTrelloClient) ListCommentsSince(ctx context.Context, id string, since time.Time) ([]trelloclient.Comment, error) {
	if f.listCommentsSince == nil {
		return nil, errors.New("listCommentsSince not implemented")
	}
	return f.listCommentsSince(ctx, id, since)
}
func (f *fakeTrelloClient) ListTokenWebhooks(context.Context, string) ([]trelloclient.Webhook, error) {
	return nil, errors.New("listTokenWebhooks not implemented")
}
func (f *fakeTrelloClient) UpdateWebhookCallback(context.Context, string, string) error {
	return errors.New("updateWebhookCallback not implemented")
}
func (f *fakeTrelloClient) CreateTokenWebhook(context.Context, string, string, string, string) (trelloclient.Webhook, error) {
	return trelloclient.Webhook{}, errors.New("createTokenWebhook not implemented")
}
func (f *fakeTrelloClient) DeleteWebhook(context.Context, string, string) error {
	return errors.New("deleteWebhook not implemented")
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestBuildTrelloToolsReturnsExpectedNames(t *testing.T) {
	got := BuildTrelloTools(&fakeTrelloClient{}, quietLogger())
	want := []string{
		"trello_card_get",
		"trello_card_list",
		"trello_board_lists",
		"trello_card_move",
		"trello_card_comment",
		"trello_card_latest_comment",
		"trello_card_comments_since",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d tools, got %d", len(want), len(got))
	}
	for i, n := range want {
		if got[i].Name != n {
			t.Errorf("tool[%d]: got %q want %q", i, got[i].Name, n)
		}
		if got[i].Description == "" {
			t.Errorf("tool %s missing description", n)
		}
		if got[i].Handler == nil {
			t.Errorf("tool %s missing handler", n)
		}
		if len(got[i].Parameters) == 0 {
			t.Errorf("tool %s missing JSON schema", n)
		}
	}
}

func TestBuildTrelloToolsNilClient(t *testing.T) {
	if got := BuildTrelloTools(nil, quietLogger()); got != nil {
		t.Errorf("nil client must yield nil tool slice; got %d entries", len(got))
	}
}

// TestBuildTrelloToolsSkipPermissionMatrix asserts read-only tools skip
// the permission prompt and write tools do not.
func TestBuildTrelloToolsSkipPermissionMatrix(t *testing.T) {
	tools := BuildTrelloTools(&fakeTrelloClient{}, quietLogger())
	skip := map[string]bool{}
	for _, tool := range tools {
		skip[tool.Name] = tool.SkipPermission
	}
	wantSkip := map[string]bool{
		"trello_card_get":            true,
		"trello_card_list":           true,
		"trello_board_lists":         true,
		"trello_card_latest_comment": true,
		"trello_card_comments_since": true,
		"trello_card_move":           false, // write
		"trello_card_comment":        false, // write
	}
	for name, want := range wantSkip {
		got, ok := skip[name]
		if !ok {
			t.Errorf("tool %s missing", name)
			continue
		}
		if got != want {
			t.Errorf("tool %s SkipPermission: got %v want %v", name, got, want)
		}
	}
}

func TestParamsValidate(t *testing.T) {
	if err := (trelloCardGetParams{}).validate(); err == nil {
		t.Errorf("empty card_id should fail")
	}
	if err := (trelloCardGetParams{CardID: "c"}).validate(); err != nil {
		t.Errorf("non-empty card_id should pass: %v", err)
	}
	if err := (trelloBoardListsParams{}).validate(); err == nil {
		t.Errorf("empty board_id should fail")
	}
	if err := (trelloCardCommentParams{CardID: "c"}).validate(); err == nil {
		t.Errorf("missing text should fail")
	}
	if err := (trelloCardMoveParams{CardID: "c"}).validate(); err == nil {
		t.Errorf("missing target list should fail")
	}
	if err := (trelloCardMoveParams{CardID: "c", TargetListID: "L"}).validate(); err != nil {
		t.Errorf("with target_list_id must pass: %v", err)
	}
	if err := (trelloCardMoveParams{CardID: "c", TargetListName: "Done"}).validate(); err != nil {
		t.Errorf("with target_list_name must pass: %v", err)
	}
}

func TestMoveCardByID(t *testing.T) {
	moves := 0
	c := &fakeTrelloClient{
		getCard: func(_ context.Context, _ string) (trelloclient.Card, error) {
			return trelloclient.Card{ID: "c1", IDList: "L_old", IDBoard: "B"}, nil
		},
		getCardList: func(_ context.Context, _ string) (trelloclient.List, error) {
			return trelloclient.List{ID: "L_old", Name: "Todo"}, nil
		},
		listBoardLists: func(_ context.Context, _ string) ([]trelloclient.List, error) {
			return []trelloclient.List{{ID: "L_new", Name: "Done"}}, nil
		},
		moveCard: func(_ context.Context, _, listID string) error {
			if listID != "L_new" {
				t.Errorf("moveCard listID: got %q want %q", listID, "L_new")
			}
			moves++
			return nil
		},
	}
	res, err := moveCard(context.Background(), c, trelloCardMoveParams{CardID: "c1", TargetListID: "L_new"})
	if err != nil {
		t.Fatalf("moveCard: %v", err)
	}
	if moves != 1 {
		t.Errorf("expected 1 MoveCard call, got %d", moves)
	}
	if res.From.ID != "L_old" || res.To.ID != "L_new" {
		t.Errorf("from/to mismatch: %+v", res)
	}
	if res.To.Name != "Done" {
		t.Errorf("to.Name should be looked up: got %q", res.To.Name)
	}
}

func TestMoveCardByName(t *testing.T) {
	c := &fakeTrelloClient{
		getCard: func(_ context.Context, _ string) (trelloclient.Card, error) {
			return trelloclient.Card{ID: "c1", IDList: "L_old", IDBoard: "B"}, nil
		},
		getCardList: func(_ context.Context, _ string) (trelloclient.List, error) {
			return trelloclient.List{ID: "L_old", Name: "Todo"}, nil
		},
		listBoardLists: func(_ context.Context, _ string) ([]trelloclient.List, error) {
			return []trelloclient.List{
				{ID: "L_old", Name: "Todo"},
				{ID: "L_new", Name: "In Progress"},
			}, nil
		},
		moveCard: func(_ context.Context, _, listID string) error {
			if listID != "L_new" {
				t.Errorf("moveCard listID: got %q", listID)
			}
			return nil
		},
	}
	res, err := moveCard(context.Background(), c, trelloCardMoveParams{CardID: "c1", TargetListName: "in progress"})
	if err != nil {
		t.Fatalf("moveCard: %v", err)
	}
	if res.To.ID != "L_new" || res.To.Name != "In Progress" {
		t.Errorf("to: got %+v", res.To)
	}
}

func TestMoveCardByNameNotFound(t *testing.T) {
	c := &fakeTrelloClient{
		getCard: func(_ context.Context, _ string) (trelloclient.Card, error) {
			return trelloclient.Card{ID: "c1", IDList: "L_old", IDBoard: "B"}, nil
		},
		getCardList: func(_ context.Context, _ string) (trelloclient.List, error) {
			return trelloclient.List{ID: "L_old", Name: "Todo"}, nil
		},
		listBoardLists: func(_ context.Context, _ string) ([]trelloclient.List, error) {
			return []trelloclient.List{{ID: "L1", Name: "Todo"}}, nil
		},
	}
	_, err := moveCard(context.Background(), c, trelloCardMoveParams{CardID: "c1", TargetListName: "Done"})
	if err == nil || !strings.Contains(err.Error(), "no list named") {
		t.Fatalf("expected no-list-found error, got %v", err)
	}
}

func TestMoveCardNoOpWhenAlreadyOnTarget(t *testing.T) {
	moves := 0
	c := &fakeTrelloClient{
		getCard: func(_ context.Context, _ string) (trelloclient.Card, error) {
			return trelloclient.Card{ID: "c1", IDList: "L1", IDBoard: "B"}, nil
		},
		getCardList: func(_ context.Context, _ string) (trelloclient.List, error) {
			return trelloclient.List{ID: "L1", Name: "Todo"}, nil
		},
		moveCard: func(_ context.Context, _, _ string) error {
			moves++
			return nil
		},
	}
	res, err := moveCard(context.Background(), c, trelloCardMoveParams{CardID: "c1", TargetListID: "L1"})
	if err != nil {
		t.Fatalf("moveCard: %v", err)
	}
	if moves != 0 {
		t.Errorf("expected zero MoveCard calls (already on target), got %d", moves)
	}
	if res.From.ID != "L1" || res.To.ID != "L1" {
		t.Errorf("from/to should both be L1: %+v", res)
	}
}
