package app

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTUICalculateLayout_WideAndNarrow(t *testing.T) {
	wide := tuiCalculateLayout(120, 30, 1, 1)
	if !wide.WideMain {
		t.Fatal("wide layout should use side-by-side main panels")
	}
	if got := wide.WorkersWidth + wide.ActivityWidth + tuiMainPanelGapWidth; got != 120 {
		t.Fatalf("wide panel widths mismatch: got=%d want=120", got)
	}
	if got := wide.MainHeight + wide.EventsHeight; got != wide.BodyHeight {
		t.Fatalf("wide body height mismatch: main+events=%d body=%d", got, wide.BodyHeight)
	}

	narrow := tuiCalculateLayout(60, 18, 1, 1)
	if narrow.WideMain {
		t.Fatal("narrow layout should use single-column main panel")
	}
	if narrow.WorkersWidth != 60 || narrow.ActivityWidth != 60 {
		t.Fatalf("narrow panel widths mismatch: workers=%d activity=%d", narrow.WorkersWidth, narrow.ActivityWidth)
	}
	if got := narrow.MainHeight + narrow.EventsHeight; got != narrow.BodyHeight {
		t.Fatalf("narrow body height mismatch: main+events=%d body=%d", got, narrow.BodyHeight)
	}
}

func TestTUIViewReflowsOnWindowResize(t *testing.T) {
	m := tuiModel{
		listenAddr: ":18790",
		modelName:  "claude-opus-4.6-1m",
		startTime:  time.Now().Add(-2 * time.Minute),
		workers: []WorkerStatus{
			{CardID: "card-1", State: StateThinking, LastEventAt: time.Now()},
			{CardID: "card-2", State: StateRunningTool, LastEventAt: time.Now()},
		},
		selOK: true,
		selStatus: WorkerStatus{
			CardID:      "card-1",
			State:       StateThinking,
			LastEventAt: time.Now(),
			SessionID:   "session-123",
			Model:       "claude-opus-4.6-1m",
		},
		selEntries: []ActivityEntry{{At: time.Now(), Kind: "assistant_message", Summary: strings.Repeat("session output ", 18)}},
	}

	wideValue, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	wide := wideValue.(tuiModel)
	wideView := wide.View()
	if got := lipgloss.Width(wideView); got > 120 {
		t.Fatalf("wide view width = %d, want <= 120", got)
	}
	if got := lipgloss.Height(wideView); got > 30 {
		t.Fatalf("wide view height = %d, want <= 30", got)
	}

	narrowValue, _ := wide.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	narrow := narrowValue.(tuiModel)
	narrowView := narrow.View()
	if got := lipgloss.Width(narrowView); got > 60 {
		t.Fatalf("narrow view width = %d, want <= 60", got)
	}
	if got := lipgloss.Height(narrowView); got > 18 {
		t.Fatalf("narrow view height = %d, want <= 18", got)
	}
}

func TestTUIViewKeepsHeaderVisibleWithLongCJKLogs(t *testing.T) {
	m := tuiModel{
		width:      100,
		height:     22,
		listenAddr: ":18790",
		modelName:  "claude-opus-4.6-1m",
		startTime:  time.Now().Add(-3 * time.Minute),
		workers:    []WorkerStatus{{CardID: "card-cjk", State: StateRunningTool, LastEventAt: time.Now()}},
		selOK:      true,
		selStatus: WorkerStatus{
			CardID:      "card-cjk",
			State:       StateRunningTool,
			LastEventAt: time.Now(),
			SessionID:   "session-cjk",
			Model:       "claude-opus-4.6-1m",
		},
	}

	for i := 0; i < 20; i++ {
		m.selEntries = append(m.selEntries, ActivityEntry{
			At:      time.Now(),
			Kind:    "assistant_message",
			Summary: strings.Repeat("中文输出片段与调查路径", 12),
		})
		m.globalEvents = append(m.globalEvents, GlobalEvent{
			At:      time.Now(),
			CardID:  "card-cjk",
			Kind:    "tool_start",
			Summary: strings.Repeat("关键因素与监管路径", 10),
		})
	}

	view := m.View()
	if !strings.Contains(view, "Trello Gateway") {
		t.Fatalf("header disappeared from view:\n%s", view)
	}
	if got := lipgloss.Height(view); got > 22 {
		t.Fatalf("view height = %d, want <= 22", got)
	}
}

func TestViewActivityPanel_DoesNotOverflowWidth(t *testing.T) {
	m := tuiModel{
		workers:     []WorkerStatus{{CardID: "card-1"}},
		selectedIdx: 0,
		selOK:       true,
		selStatus: WorkerStatus{
			CardID:       "card-1",
			State:        StateRunningTool,
			LastEventAt:  time.Now(),
			SessionID:    "session-1234567890",
			Model:        "claude-opus-4.6-1m",
			TurnTokensIn: 123,
			TurnTokensOut: 456,
			InboxDepth:   1,
			WorkDir:      `C:\project\jjc\workspace\6a16b923f7b571d45f46a9a7`,
		},
		selEntries: []ActivityEntry{
			{
				At:      time.Now(),
				Kind:    "tool_start",
				Summary: strings.Repeat("path:C:/project/jjc-playbooks/workspace/6a16b923f7b571d45f46a9a7 ", 8),
			},
		},
	}

	const panelWidth = 80
	out := m.viewActivityPanel(panelWidth, 16)
	assertNoRenderedLineOverWidth(t, out, panelWidth)
}

func TestViewEventsPanel_DoesNotOverflowWidth(t *testing.T) {
	m := tuiModel{
		globalEvents: []GlobalEvent{
			{
				At:      time.Now(),
				CardID:  "6a16b923f7b571d45f46a9a7",
				Kind:    "tool_start",
				Summary: strings.Repeat("map[path:C:/project/jjc-playbooks/workspace/6a16b923f7b571d45f46a9a7]", 6),
			},
		},
	}

	const panelWidth = 80
	out := m.viewEventsPanel(panelWidth, 10)
	assertNoRenderedLineOverWidth(t, out, panelWidth)
}

func TestView_DoesNotOverflowTerminalHeightWithLongRightPanelLogs(t *testing.T) {
	m := tuiModel{
		width:      120,
		height:     30,
		listenAddr: ":18790",
		modelName:  "claude-opus-4.6-1m",
		startTime:  time.Now().Add(-5 * time.Minute),
		workers:    []WorkerStatus{{CardID: "6a16b923f7b571d45f46a9a7"}},
		selOK:      true,
		selStatus: WorkerStatus{
			CardID:      "6a16b923f7b571d45f46a9a7",
			State:       StateRunningTool,
			LastEventAt: time.Now(),
			SessionID:   "session-1234567890",
			Model:       "claude-opus-4.6-1m",
		},
	}

	for i := 0; i < 100; i++ {
		m.selEntries = append(m.selEntries, ActivityEntry{
			At:      time.Now(),
			Kind:    "tool_start",
			Summary: strings.Repeat("map[path:C:/project/jjc-playbooks/workspace/6a16b923f7b571d45f46a9a7]", 5),
		})
		m.globalEvents = append(m.globalEvents, GlobalEvent{
			At:      time.Now(),
			CardID:  "6a16b923f7b571d45f46a9a7",
			Kind:    "tool_start",
			Summary: strings.Repeat("map[path:C:/project/jjc-playbooks/workspace/6a16b923f7b571d45f46a9a7]", 5),
		})
	}

	view := m.View()
	if got := strings.Count(view, "\n") + 1; got > m.height {
		t.Fatalf("view overflow: got lines=%d, terminal height=%d", got, m.height)
	}
	assertNoRenderedLineOverWidth(t, view, m.width)
}

func assertNoRenderedLineOverWidth(t *testing.T, rendered string, maxWidth int) {
	t.Helper()
	for i, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > maxWidth {
			t.Fatalf("line %d width overflow: got=%d max=%d line=%q", i+1, got, maxWidth, line)
		}
	}
}

func TestTUIPanelInnerSize_MinAndNormal(t *testing.T) {
	w, h := tuiPanelInnerSize(42, 16)
	if w != 38 || h != 14 {
		t.Fatalf("unexpected normal panel inner size: got=(%d,%d), want=(38,14)", w, h)
	}

	w, h = tuiPanelInnerSize(0, 0)
	if w != 1 || h != 1 {
		t.Fatalf("unexpected min panel inner size: got=(%d,%d), want=(1,1)", w, h)
	}
}

func TestTUIDetailWidth_Budgeting(t *testing.T) {
	got := tuiDetailWidth(76, 5, "15:04:05", tuiKindLabel("tool_start"))
	if got != 47 {
		t.Fatalf("unexpected detail width: got=%d want=47", got)
	}

	got = tuiDetailWidth(10, 7, "long", "long")
	if got != -5 {
		t.Fatalf("unexpected negative detail width: got=%d want=-5", got)
	}
}