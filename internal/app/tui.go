package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	catppuccin "github.com/catppuccin/go"
)

// ---------- Catppuccin Mocha theme ----------

var mocha = catppuccin.Mocha

// mc converts a catppuccin Color to a lipgloss TerminalColor.
func mc(c catppuccin.Color) lipgloss.TerminalColor {
	return lipgloss.Color(c.Hex)
}

// ---------- Styles ----------

var (
	sHeaderTitle = lipgloss.NewStyle().Bold(true).Foreground(mc(mocha.Lavender()))
	sHeaderInfo  = lipgloss.NewStyle().Foreground(mc(mocha.Subtext0()))

	sPanelTitle = lipgloss.NewStyle().Bold(true).Foreground(mc(mocha.Blue()))
	sMuted      = lipgloss.NewStyle().Foreground(mc(mocha.Overlay0()))
	sSubtext    = lipgloss.NewStyle().Foreground(mc(mocha.Subtext0()))
	sSelected   = lipgloss.NewStyle().Bold(true).Foreground(mc(mocha.Text()))

	sFooterKey  = lipgloss.NewStyle().Bold(true).Foreground(mc(mocha.Mauve()))
	sFooterDesc = lipgloss.NewStyle().Foreground(mc(mocha.Overlay0()))

	// Worker state colors
	sStateIdle    = lipgloss.NewStyle().Foreground(mc(mocha.Overlay0()))
	sStateThink   = lipgloss.NewStyle().Foreground(mc(mocha.Yellow()))
	sStateTool    = lipgloss.NewStyle().Foreground(mc(mocha.Green()))
	sStateError   = lipgloss.NewStyle().Foreground(mc(mocha.Red()))
	sStateDisconn = lipgloss.NewStyle().Foreground(mc(mocha.Surface2()))
	sStateStart   = lipgloss.NewStyle().Foreground(mc(mocha.Peach()))
	sStatePerm    = lipgloss.NewStyle().Foreground(mc(mocha.Mauve()))

	// Activity kind colors
	sKindTool      = lipgloss.NewStyle().Foreground(mc(mocha.Green()))
	sKindAssistant = lipgloss.NewStyle().Foreground(mc(mocha.Lavender()))
	sKindUser      = lipgloss.NewStyle().Foreground(mc(mocha.Blue()))
	sKindError     = lipgloss.NewStyle().Foreground(mc(mocha.Red()))
	sKindIdle      = lipgloss.NewStyle().Foreground(mc(mocha.Overlay0()))
	sKindSession   = lipgloss.NewStyle().Foreground(mc(mocha.Teal()))
)

func panelStyle(focused bool) lipgloss.Style {
	s := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1)
	if focused {
		return s.BorderForeground(mc(mocha.Blue()))
	}
	return s.BorderForeground(mc(mocha.Surface1()))
}

// ---------- Focus ----------

type tuiFocus int

const (
	focusWorkers tuiFocus = iota
	focusActivity
	focusEvents
	focusCount // sentinel
)

// ---------- Model ----------

type tuiModel struct {
	provider   StatusProvider
	globalLog  *GlobalEventLog
	listenAddr string
	modelName  string
	startTime  time.Time

	workers     []WorkerStatus
	selectedIdx int
	selStatus   WorkerStatus
	selEntries  []ActivityEntry
	selOK       bool

	globalEvents []GlobalEvent

	focus      tuiFocus
	width      int
	height     int
	statusMsg  string    // transient message (e.g. dump result), shown in footer
	statusUntil time.Time // when statusMsg should disappear
}

type tuiTickMsg time.Time

// NewTUIProgram creates a bubbletea Program for the full-screen TUI.
func NewTUIProgram(provider StatusProvider, globalLog *GlobalEventLog, listenAddr, modelName string) *tea.Program {
	m := tuiModel{
		provider:   provider,
		globalLog:  globalLog,
		listenAddr: listenAddr,
		modelName:  modelName,
		startTime:  time.Now(),
	}
	return tea.NewProgram(m, tea.WithAltScreen())
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tuiTickMsg(t) })
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.focus = (m.focus + 1) % focusCount
		case "shift+tab":
			m.focus = (m.focus + focusCount - 1) % focusCount
		case "j", "down":
			if m.focus == focusWorkers && m.selectedIdx < len(m.workers)-1 {
				m.selectedIdx++
			}
		case "k", "up":
			if m.focus == focusWorkers && m.selectedIdx > 0 {
				m.selectedIdx--
			}
		case "d":
			(&m).dumpSelected()
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tuiTickMsg:
		m.refresh()
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg { return tuiTickMsg(t) })
	}
	return m, nil
}

func (m *tuiModel) refresh() {
	m.workers = m.provider.ListCards()
	if m.selectedIdx >= len(m.workers) {
		m.selectedIdx = max(0, len(m.workers)-1)
	}
	if len(m.workers) > 0 && m.selectedIdx < len(m.workers) {
		cardID := m.workers[m.selectedIdx].CardID
		m.selStatus, m.selEntries, m.selOK = m.provider.Snapshot(cardID)
	} else {
		m.selOK = false
		m.selEntries = nil
	}
	m.globalEvents = m.globalLog.Entries()
}

// dumpSelected reports the absolute path of the selected worker's
// per-worker activity log file (allocated when the worker started; the
// dispatcher continually appends every event to it). Bound to the "d"
// key in Update. Updates statusMsg with the path or an error reason.
func (m *tuiModel) dumpSelected() {
	if !m.selOK || len(m.workers) == 0 {
		m.statusMsg = "dump: no worker selected"
		m.statusUntil = time.Now().Add(8 * time.Second)
		return
	}
	cardID := m.workers[m.selectedIdx].CardID
	st, _, ok := m.provider.Snapshot(cardID)
	if !ok {
		m.statusMsg = "dump: snapshot unavailable for " + cardID
		m.statusUntil = time.Now().Add(8 * time.Second)
		return
	}
	if st.LogPath == "" {
		m.statusMsg = "dump: activity log unavailable for " + cardID
	} else {
		m.statusMsg = "activity log: " + st.LogPath
	}
	m.statusUntil = time.Now().Add(15 * time.Second)
}

// ---------- View ----------

func (m tuiModel) View() string {
	if m.width < 40 || m.height < 12 {
		return sMuted.Render("Terminal too small (need at least 40×12)")
	}

	header := m.viewHeader()
	footer := m.viewFooter()

	// Height accounting: header(1) + gap(1) + main + events + footer(1) = m.height
	remaining := m.height - 3
	eventsH := tuiClamp(remaining/4, 5, 9)
	mainH := remaining - eventsH

	var main string
	if m.width >= 80 {
		main = m.viewWideMain(mainH)
	} else {
		main = m.viewNarrowMain(mainH)
	}
	events := m.viewEventsPanel(m.width, eventsH)

	return lipgloss.JoinVertical(lipgloss.Left, header, "", main, events, footer)
}

func (m tuiModel) viewHeader() string {
	up := tuiFmtDuration(time.Since(m.startTime))
	parts := []string{
		sHeaderTitle.Render("Trello Gateway"),
		sHeaderInfo.Render(m.listenAddr),
		sHeaderInfo.Render(m.modelName),
		sHeaderInfo.Render(fmt.Sprintf("%d workers", len(m.workers))),
		sHeaderInfo.Render("up " + up),
	}
	sep := sHeaderInfo.Render("  ·  ")
	return " " + strings.Join(parts, sep)
}

func (m tuiModel) viewFooter() string {
	keys := []struct{ key, desc string }{
		{"q", "quit"},
		{"tab", "switch panel"},
		{"j/k", "select worker"},
		{"d", "dump activity"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, sFooterKey.Render(k.key)+sFooterDesc.Render(":"+k.desc))
	}
	line := " " + strings.Join(parts, "  ")
	if m.statusMsg != "" && time.Now().Before(m.statusUntil) {
		line += "   " + sFooterDesc.Render(m.statusMsg)
	}
	return line
}

// ---------- Wide layout (≥80 cols) ----------

func (m tuiModel) viewWideMain(h int) string {
	leftW := tuiClamp(m.width*2/5, 28, 42)
	rightW := m.width - leftW - 1 // 1 char gap
	left := m.viewWorkerList(leftW, h)
	right := m.viewActivityPanel(rightW, h)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

// ---------- Narrow layout (<80 cols) ----------

func (m tuiModel) viewNarrowMain(h int) string {
	switch m.focus {
	case focusActivity:
		return m.viewActivityPanel(m.width, h)
	default:
		return m.viewWorkerList(m.width, h)
	}
}

// ---------- Worker list panel ----------

func (m tuiModel) viewWorkerList(w, h int) string {
	innerW := w - 4 // border(2) + padding(2)
	innerH := h - 2 // border(2)
	if innerW < 10 || innerH < 3 {
		return panelStyle(m.focus == focusWorkers).Width(innerW).Height(innerH).Render("…")
	}

	var lines []string
	lines = append(lines, sPanelTitle.Render("Workers"))

	if len(m.workers) == 0 {
		lines = append(lines, sMuted.Render("(no active workers)"))
	} else {
		for i, ws := range m.workers {
			if i >= innerH-1 {
				lines = append(lines, sMuted.Render(fmt.Sprintf("  … +%d more", len(m.workers)-i)))
				break
			}
			cursor := "  "
			if i == m.selectedIdx {
				cursor = "▸ "
			}
			state := tuiStyledState(ws.State)
			dur := tuiShortAgo(ws.LastEventAt)
			line := fmt.Sprintf("%s%s  %s  %s", cursor, ws.CardID, state, dur)
			if i == m.selectedIdx {
				lines = append(lines, sSelected.Render(line))
			} else {
				lines = append(lines, line)
			}
		}
	}

	content := strings.Join(lines, "\n")
	return panelStyle(m.focus == focusWorkers).Width(innerW).Height(innerH).Render(content)
}

// ---------- Activity panel ----------

func (m tuiModel) viewActivityPanel(w, h int) string {
	innerW := w - 4
	innerH := h - 2
	if innerW < 10 || innerH < 3 {
		return panelStyle(m.focus == focusActivity).Width(innerW).Height(innerH).Render("…")
	}

	if !m.selOK || len(m.workers) == 0 {
		content := sPanelTitle.Render("Activity") + "\n\n" + sMuted.Render("No worker selected")
		return panelStyle(m.focus == focusActivity).Width(innerW).Height(innerH).Render(content)
	}

	st := m.selStatus
	header := fmt.Sprintf("%s  %s  %s",
		sPanelTitle.Render(st.CardID),
		tuiStyledState(st.State),
		sMuted.Render(tuiShortAgo(st.LastEventAt)),
	)
	target := tuiFmtTarget(st)
	meta := sSubtext.Render(fmt.Sprintf("session:%-12s  model:%-20s  tok:%d/%d  inbox:%d",
		tuiTruncate(st.SessionID, 12),
		tuiTruncate(st.Model, 20),
		st.TurnTokensIn, st.TurnTokensOut,
		st.InboxDepth,
	))

	var lines []string
	lines = append(lines, header)
	if target != "" {
		lines = append(lines, sSubtext.Render(target))
	}
	if st.WorkDir != "" {
		lines = append(lines, sSubtext.Render("work_dir: "+tuiTruncate(st.WorkDir, innerW-10)))
	}
	lines = append(lines, meta)
	lines = append(lines, "")
	lines = append(lines, sPanelTitle.Render("Activity"))

	// Show entries newest first
	maxEntries := innerH - len(lines)
	entries := m.selEntries
	start := 0
	if len(entries) > maxEntries {
		start = len(entries) - maxEntries
	}
	for i := len(entries) - 1; i >= start; i-- {
		e := entries[i]
		ts := sMuted.Render(e.At.Format("15:04:05"))
		kind := tuiStyledKind(e.Kind)
		detail := tuiTruncate(formatEntryDetail(e), innerW-22)
		lines = append(lines, fmt.Sprintf("%s  %s  %s", ts, kind, sSubtext.Render(detail)))
	}

	content := strings.Join(lines, "\n")
	return panelStyle(m.focus == focusActivity).Width(innerW).Height(innerH).Render(content)
}

// ---------- Global events panel ----------

func (m tuiModel) viewEventsPanel(w, h int) string {
	innerW := w - 4
	innerH := h - 2
	if innerW < 10 || innerH < 2 {
		return panelStyle(m.focus == focusEvents).Width(innerW).Height(innerH).Render("…")
	}

	var lines []string
	lines = append(lines, sPanelTitle.Render("Global Events"))

	if len(m.globalEvents) == 0 {
		lines = append(lines, sMuted.Render("(no events yet)"))
	} else {
		maxEntries := innerH - 1
		events := m.globalEvents
		start := 0
		if len(events) > maxEntries {
			start = len(events) - maxEntries
		}
		for i := len(events) - 1; i >= start; i-- {
			e := events[i]
			ts := sMuted.Render(e.At.Format("15:04:05"))
			cid := sSubtext.Render(e.CardID)
			kind := tuiStyledKind(e.Kind)
			detail := tuiTruncate(e.Summary, innerW-30)
			lines = append(lines, fmt.Sprintf("%s  %s  %s  %s", ts, cid, kind, sSubtext.Render(detail)))
		}
	}

	content := strings.Join(lines, "\n")
	return panelStyle(m.focus == focusEvents).Width(innerW).Height(innerH).Render(content)
}

// ---------- Helpers ----------

func tuiStyledState(s WorkerState) string {
	switch s {
	case StateIdle:
		return sStateIdle.Render(fmt.Sprintf("%-9s", "idle"))
	case StateThinking:
		return sStateThink.Render(fmt.Sprintf("%-9s", "thinking"))
	case StateRunningTool:
		return sStateTool.Render(fmt.Sprintf("%-9s", "tool"))
	case StateError:
		return sStateError.Render(fmt.Sprintf("%-9s", "error"))
	case StateDisconnected:
		return sStateDisconn.Render(fmt.Sprintf("%-9s", "disconn"))
	case StateStarting:
		return sStateStart.Render(fmt.Sprintf("%-9s", "starting"))
	case StateWaitingPermission:
		return sStatePerm.Render(fmt.Sprintf("%-9s", "perm"))
	default:
		return sMuted.Render(fmt.Sprintf("%-9s", string(s)))
	}
}

func tuiStyledKind(kind string) string {
	padded := fmt.Sprintf("%-16s", kind)
	switch {
	case strings.HasPrefix(kind, "tool"):
		return sKindTool.Render(padded)
	case kind == "assistant_message" || kind == "assistant":
		return sKindAssistant.Render(fmt.Sprintf("%-16s", "assistant"))
	case kind == "user_message":
		return sKindUser.Render(fmt.Sprintf("%-16s", "user_msg"))
	case kind == "error":
		return sKindError.Render(padded)
	case kind == "idle", kind == "session_idle_reaped":
		return sKindIdle.Render(padded)
	case strings.HasPrefix(kind, "session"), strings.HasPrefix(kind, "worker"):
		return sKindSession.Render(padded)
	case strings.HasPrefix(kind, "route"):
		return sKindUser.Render(padded)
	default:
		return sMuted.Render(padded)
	}
}

func tuiShortAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func tuiFmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}

func tuiTruncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}

func tuiClamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// tuiFmtTarget renders the GitHub target the worker is operating on, e.g.
// "owner/repo · issue #123". Returns "" when no classification metadata is
// available (generic cards or pre-classification snapshots).
func tuiFmtTarget(st WorkerStatus) string {
	if st.Owner == "" || st.Repo == "" {
		return ""
	}
	kind := "?"
	switch st.Kind {
	case KindIssue:
		kind = "issue"
	case KindPR:
		kind = "pr"
	}
	if st.Number == "" {
		return fmt.Sprintf("%s/%s · %s", st.Owner, st.Repo, kind)
	}
	return fmt.Sprintf("%s/%s · %s #%s", st.Owner, st.Repo, kind, st.Number)
}
