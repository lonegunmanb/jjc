package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	catppuccin "github.com/catppuccin/go"
)

const (
	tuiDefaultTerminalWidth  = 100
	tuiDefaultTerminalHeight = 30
	tuiWideMainBreakpoint    = 80
	tuiMainPanelGapWidth     = 1
	tuiPanelBorderWidth      = 2
	tuiPanelPaddingWidth     = 2
	tuiPanelFrameHeight      = 2
)

type tuiLayout struct {
	Width       int
	Height      int
	BodyHeight  int
	MainHeight  int
	EventsHeight int
	WideMain    bool
	WorkersWidth int
	ActivityWidth int
}

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
	deleter    WorkerDeleter
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

	// confirmDelete drives the modal confirmation overlay shown after the
	// operator presses "d". When true, all key input is routed to the
	// modal until the user confirms ("y") or cancels ("n"/"esc").
	confirmDelete  bool
	deleteCardID   string // card whose worker the modal is asking about
	deleteWorkDir  string // workdir snapshot to display in the modal
	deleteInFlight bool   // true while DeleteWorker is running
}

// WorkerDeleter is the subset of *Dispatcher the TUI needs to forcibly
// tear down a worker. Optional: when nil, the "d" key in the TUI is a
// no-op (with a footer message). Kept separate from StatusProvider so
// read-only consumers (REPL) need not implement it.
type WorkerDeleter interface {
	DeleteWorker(cardID string) (workDir string, err error)
}

type tuiTickMsg time.Time

// tuiDeleteResultMsg is delivered back to Update once a background
// DeleteWorker call has finished, so the UI can surface success/error in
// the footer status line.
type tuiDeleteResultMsg struct {
	cardID  string
	workDir string
	err     error
}

// NewTUIProgram creates a bubbletea Program for the full-screen TUI.
// deleter may be nil: in that case the "d" delete-worker shortcut is
// disabled (it shows an explanatory footer message instead).
func NewTUIProgram(provider StatusProvider, deleter WorkerDeleter, globalLog *GlobalEventLog, listenAddr, modelName string) *tea.Program {
	m := tuiModel{
		provider:   provider,
		deleter:    deleter,
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
		// While the delete-confirmation modal is up, all key input is
		// routed there. Bubbletea has no built-in modal stack, so we
		// just intercept here.
		if m.confirmDelete {
			return m.updateConfirmDelete(msg)
		}
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
		case "a":
			m.dumpSelected()
		case "d":
			m.openDeleteConfirm()
		}
		return m, nil
	case tuiDeleteResultMsg:
		m.deleteInFlight = false
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("delete failed for %s: %v", msg.cardID, msg.err)
		} else if msg.workDir == "" {
			m.statusMsg = fmt.Sprintf("deleted worker %s (no workdir)", msg.cardID)
		} else {
			m.statusMsg = fmt.Sprintf("deleted worker %s, removed %s", msg.cardID, msg.workDir)
		}
		m.statusUntil = time.Now().Add(15 * time.Second)
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Force a full repaint on resize. Without this the alt-screen
		// buffer can keep stale wider/taller content from the previous
		// layout (especially noticeable when shrinking the window).
		return m, tea.ClearScreen
	case tuiTickMsg:
		m.refresh()
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg { return tuiTickMsg(t) })
	}
	return m, nil
}

// openDeleteConfirm raises the delete-confirmation modal for the currently
// selected worker. Drops a footer message (without raising the modal) when
// nothing is selected or no deleter was wired in.
func (m *tuiModel) openDeleteConfirm() {
	if m.deleter == nil {
		m.statusMsg = "delete: no deleter configured"
		m.statusUntil = time.Now().Add(8 * time.Second)
		return
	}
	if !m.selOK || len(m.workers) == 0 {
		m.statusMsg = "delete: no worker selected"
		m.statusUntil = time.Now().Add(8 * time.Second)
		return
	}
	if m.deleteInFlight {
		m.statusMsg = "delete: a previous delete is still running"
		m.statusUntil = time.Now().Add(8 * time.Second)
		return
	}
	m.confirmDelete = true
	m.deleteCardID = m.workers[m.selectedIdx].CardID
	m.deleteWorkDir = m.selStatus.WorkDir
}

// updateConfirmDelete handles key input while the modal is up. "y" runs
// the delete in a tea.Cmd so the UI thread keeps refreshing while the
// dispatcher waits for the worker goroutine to drain. Any other key
// ("n", "esc", "q") cancels.
func (m tuiModel) updateConfirmDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		cardID := m.deleteCardID
		m.confirmDelete = false
		m.deleteCardID = ""
		m.deleteWorkDir = ""
		m.deleteInFlight = true
		m.statusMsg = "deleting worker " + cardID + "..."
		m.statusUntil = time.Now().Add(60 * time.Second)
		deleter := m.deleter
		cmd := func() tea.Msg {
			workDir, err := deleter.DeleteWorker(cardID)
			return tuiDeleteResultMsg{cardID: cardID, workDir: workDir, err: err}
		}
		return m, cmd
	default:
		// n / N / esc / q / any other key cancels.
		m.confirmDelete = false
		m.deleteCardID = ""
		m.deleteWorkDir = ""
		return m, nil
	}
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
// dispatcher continually appends every event to it). Bound to the "a"
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
	width, height := tuiNormalizeTerminalSize(m.width, m.height)
	if width < 40 || height < 12 {
		return sMuted.Render("Terminal too small (need at least 40×12)")
	}
	if m.confirmDelete {
		return m.viewConfirmDelete(width, height)
	}

	header := m.viewHeader(width)
	footer := m.viewFooter(width)
	layout := tuiCalculateLayout(width, height, lipgloss.Height(header), lipgloss.Height(footer))

	var main string
	if layout.WideMain {
		main = m.viewWideMain(layout.WorkersWidth, layout.ActivityWidth, layout.MainHeight)
	} else {
		main = m.viewNarrowMain(layout.Width, layout.MainHeight)
	}
	events := m.viewEventsPanel(layout.Width, layout.EventsHeight)

	return lipgloss.JoinVertical(lipgloss.Left, header, main, events, footer)
}

// viewConfirmDelete renders the centered delete-confirmation modal that
// replaces the regular layout while m.confirmDelete is true.
func (m tuiModel) viewConfirmDelete(width, height int) string {
	workDir := m.deleteWorkDir
	if workDir == "" {
		workDir = "(none recorded)"
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(mc(mocha.Red())).Render("Delete worker?")
	body := fmt.Sprintf(
		"%s\n\n%s %s\n%s %s\n\nThis will disconnect the Copilot session\nand remove the local work_dir from disk.\n\n%s   %s",
		title,
		sSubtext.Render("Card:"),
		sSelected.Render(m.deleteCardID),
		sSubtext.Render("WorkDir:"),
		sSelected.Render(workDir),
		sFooterKey.Render("y/Enter")+sFooterDesc.Render(":confirm"),
		sFooterKey.Render("n/Esc")+sFooterDesc.Render(":cancel"),
	)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(mc(mocha.Red())).
		Padding(1, 3).
		Render(body)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func (m tuiModel) viewHeader(width int) string {
	up := tuiFmtDuration(time.Since(m.startTime))
	listen := tuiTruncate(m.listenAddr, 24)
	model := tuiTruncate(m.modelName, 24)
	parts := []string{
		sHeaderTitle.Render("Trello Gateway"),
		sHeaderInfo.Render(listen),
		sHeaderInfo.Render(model),
		sHeaderInfo.Render(fmt.Sprintf("%d workers", len(m.workers))),
		sHeaderInfo.Render("up " + up),
	}
	sep := sHeaderInfo.Render("  ·  ")
	line := " " + strings.Join(parts, sep)
	textWidth := tuiClamp(width-2, 1, width)
	return lipgloss.NewStyle().Width(width).Render(tuiTruncate(line, textWidth))
}

func (m tuiModel) viewFooter(width int) string {
	keys := []struct{ key, desc string }{
		{"q", "quit"},
		{"tab", "switch panel"},
		{"j/k", "select worker"},
		{"a", "dump activity"},
		{"d", "delete worker"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, sFooterKey.Render(k.key)+sFooterDesc.Render(":"+k.desc))
	}
	line := " " + strings.Join(parts, "  ")
	if m.statusMsg != "" && time.Now().Before(m.statusUntil) {
		statusMax := tuiClamp(width/3, 20, 80)
		line += "   " + sFooterDesc.Render(tuiTruncate(m.statusMsg, statusMax))
	}
	textWidth := tuiClamp(width-2, 1, width)
	return lipgloss.NewStyle().Width(width).Render(tuiTruncate(line, textWidth))
}

// ---------- Wide layout (≥80 cols) ----------

func (m tuiModel) viewWideMain(workersW, activityW, h int) string {
	left := m.viewWorkerList(workersW, h)
	right := m.viewActivityPanel(activityW, h)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", tuiMainPanelGapWidth), right)
}

// ---------- Narrow layout (<80 cols) ----------

func (m tuiModel) viewNarrowMain(totalW, h int) string {
	switch m.focus {
	case focusActivity:
		return m.viewActivityPanel(totalW, h)
	default:
		return m.viewWorkerList(totalW, h)
	}
}

// ---------- Worker list panel ----------

func (m tuiModel) viewWorkerList(w, h int) string {
	innerW, innerH := tuiPanelInnerSize(w, h)
	if innerW < 10 || innerH < 3 {
		return tuiRenderPanel(m.focus == focusWorkers, w, h, "…")
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
	return tuiRenderPanel(m.focus == focusWorkers, w, h, content)
}

// ---------- Activity panel ----------

func (m tuiModel) viewActivityPanel(w, h int) string {
	innerW, innerH := tuiPanelInnerSize(w, h)
	if innerW < 10 || innerH < 3 {
		return tuiRenderPanel(m.focus == focusActivity, w, h, "…")
	}

	if !m.selOK || len(m.workers) == 0 {
		content := sPanelTitle.Render("Activity") + "\n\n" + sMuted.Render("No worker selected")
		return tuiRenderPanel(m.focus == focusActivity, w, h, content)
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
		tsLabel := e.At.Format("15:04:05")
		kindLabel := tuiKindLabel(e.Kind)
		detailMax := tuiDetailWidth(innerW, 5, tsLabel, kindLabel)
		detail := tuiTruncate(formatEntryDetail(e), detailMax)
		ts := sMuted.Render(tsLabel)
		kind := tuiStyledKind(e.Kind)
		lines = append(lines, fmt.Sprintf("%s  %s  %s", ts, kind, sSubtext.Render(detail)))
	}

	content := strings.Join(lines, "\n")
	return tuiRenderPanel(m.focus == focusActivity, w, h, content)
}

// ---------- Global events panel ----------

func (m tuiModel) viewEventsPanel(w, h int) string {
	innerW, innerH := tuiPanelInnerSize(w, h)
	if innerW < 10 || innerH < 2 {
		return tuiRenderPanel(m.focus == focusEvents, w, h, "…")
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
			tsLabel := e.At.Format("15:04:05")
			cidLabel := tuiTruncate(e.CardID, tuiClamp(innerW/4, 8, 24))
			kindLabel := tuiKindLabel(e.Kind)
			detailMax := tuiDetailWidth(innerW, 7, tsLabel, cidLabel, kindLabel)
			detail := tuiTruncate(e.Summary, detailMax)
			ts := sMuted.Render(tsLabel)
			cid := sSubtext.Render(cidLabel)
			kind := tuiStyledKind(e.Kind)
			lines = append(lines, fmt.Sprintf("%s  %s  %s  %s", ts, cid, kind, sSubtext.Render(detail)))
		}
	}

	content := strings.Join(lines, "\n")
	return tuiRenderPanel(m.focus == focusEvents, w, h, content)
}

// ---------- Helpers ----------
// Rendering helpers (state/kind labels + human-readable durations).

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
	padded := tuiKindLabel(kind)
	switch {
	case strings.HasPrefix(kind, "tool"):
		return sKindTool.Render(padded)
	case kind == "assistant_message" || kind == "assistant":
		return sKindAssistant.Render(padded)
	case kind == "user_message":
		return sKindUser.Render(padded)
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

func tuiKindLabel(kind string) string {
	label := kind
	switch {
	case kind == "assistant_message" || kind == "assistant":
		label = "assistant"
	case kind == "user_message":
		label = "user_msg"
	}
	return fmt.Sprintf("%-16s", label)
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

// Text-width helpers (Unicode-width aware truncation and bounds clamp).

func tuiClamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func tuiTruncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if maxLen <= 1 {
		return "…"
	}
	for len(runes) > 0 {
		candidate := string(runes) + "…"
		if lipgloss.Width(candidate) <= maxLen {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return "…"
}

// Layout helpers (terminal normalization and per-view budget calculation).

func tuiNormalizeTerminalSize(width, height int) (int, int) {
	if width <= 0 {
		width = tuiDefaultTerminalWidth
	}
	if height <= 0 {
		height = tuiDefaultTerminalHeight
	}
	return width, height
}

func tuiCalculateLayout(width, height, headerHeight, footerHeight int) tuiLayout {
	bodyH := height - headerHeight - footerHeight
	if bodyH < 6 {
		bodyH = 6
	}

	eventsH := tuiClamp(bodyH/4, 3, 9)
	mainH := bodyH - eventsH
	if mainH < 3 {
		mainH = 3
		eventsH = bodyH - mainH
		if eventsH < 1 {
			eventsH = 1
		}
	}

	wide := width >= tuiWideMainBreakpoint
	workersW := width
	activityW := width
	if wide {
		workersW = tuiClamp(width*2/5, 28, 42)
		activityW = width - workersW - tuiMainPanelGapWidth
		if activityW < 1 {
			activityW = 1
		}
	}

	return tuiLayout{
		Width:         width,
		Height:        height,
		BodyHeight:    bodyH,
		MainHeight:    mainH,
		EventsHeight:  eventsH,
		WideMain:      wide,
		WorkersWidth:  workersW,
		ActivityWidth: activityW,
	}
}

// Panel helpers (shared frame math and rendering with hard clipping).

func tuiPanelInnerSize(totalWidth, totalHeight int) (int, int) {
	innerW := totalWidth - tuiPanelBorderWidth - tuiPanelPaddingWidth
	if innerW < 1 {
		innerW = 1
	}
	innerH := totalHeight - tuiPanelFrameHeight
	if innerH < 1 {
		innerH = 1
	}
	return innerW, innerH
}

func tuiRenderPanel(focused bool, totalWidth, totalHeight int, content string) string {
	innerW, innerH := tuiPanelInnerSize(totalWidth, totalHeight)
	return panelStyle(focused).
		Width(innerW).
		Height(innerH).
		MaxWidth(innerW).
		MaxHeight(innerH).
		Render(content)
}

func tuiDetailWidth(innerWidth int, reserved int, columns ...string) int {
	remaining := innerWidth - reserved
	for _, col := range columns {
		remaining -= lipgloss.Width(col)
	}
	return remaining
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
	case GitHubItemKindIssue:
		kind = "issue"
	case GitHubItemKindPR:
		kind = "pr"
	}
	if st.Number == "" {
		return fmt.Sprintf("%s/%s · %s", st.Owner, st.Repo, kind)
	}
	return fmt.Sprintf("%s/%s · %s #%s", st.Owner, st.Repo, kind, st.Number)
}
