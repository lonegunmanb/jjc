package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// StatusProvider is the subset of *Dispatcher the REPL needs. It exists so
// the REPL is trivially unit-testable with a fake provider.
type StatusProvider interface {
	ListCards() []WorkerStatus
	Snapshot(cardID string) (WorkerStatus, []ActivityEntry, bool)
}

// REPL is a tiny line-oriented interactive shell that lets an operator ask
// what each per-card worker is doing without scrolling through the SDK
// event log. It reads commands from in and writes responses to out.
//
// Commands:
//   ls                - list every active worker (one line each)
//   show <card_id>    - full status + recent activity for one card
//   dump <card_id>    - write full activity to a temp file; print the path
//   help              - print command help
//   quit | exit       - leave the REPL
type REPL struct {
	provider StatusProvider
	in       io.Reader
	out      io.Writer
}

// NewREPL constructs a REPL bound to the given provider, input and output.
func NewREPL(provider StatusProvider, in io.Reader, out io.Writer) *REPL {
	return &REPL{provider: provider, in: in, out: out}
}

// Run blocks until ctx is cancelled, EOF on input, or the user types
// quit/exit. It always returns nil; transient command errors are printed
// to out instead of bubbling up.
func (r *REPL) Run(ctx context.Context) error {
	fmt.Fprintln(r.out, "trello-gateway REPL ready. Type 'help' for commands.")
	scanner := bufio.NewScanner(r.in)
	scanner.Buffer(make([]byte, 4*1024), 64*1024)

	lines := make(chan string)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	r.prompt()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(r.out, "\nREPL exiting (context cancelled).")
			return nil
		case line, ok := <-lines:
			if !ok {
				fmt.Fprintln(r.out, "\nREPL exiting (EOF).")
				return nil
			}
			if r.dispatch(strings.TrimSpace(line)) {
				return nil
			}
			r.prompt()
		}
	}
}

func (r *REPL) prompt() { fmt.Fprint(r.out, "> ") }

// dispatch returns true if the REPL should exit.
func (r *REPL) dispatch(line string) bool {
	if line == "" {
		return false
	}
	parts := strings.Fields(line)
	cmd := parts[0]
	args := parts[1:]
	switch cmd {
	case "quit", "exit":
		fmt.Fprintln(r.out, "bye.")
		return true
	case "help", "?":
		r.printHelp()
	case "ls":
		r.cmdLs()
	case "show":
		if len(args) != 1 {
			fmt.Fprintln(r.out, "usage: show <card_id>")
			return false
		}
		r.cmdShow(args[0])
	case "dump":
		if len(args) != 1 {
			fmt.Fprintln(r.out, "usage: dump <card_id>")
			return false
		}
		r.cmdDump(args[0])
	default:
		fmt.Fprintf(r.out, "unknown command: %s (try 'help')\n", cmd)
	}
	return false
}

func (r *REPL) printHelp() {
	fmt.Fprintln(r.out, "Commands:")
	fmt.Fprintln(r.out, "  ls               list every active worker")
	fmt.Fprintln(r.out, "  show <card_id>   full status + recent activity for one card")
	fmt.Fprintln(r.out, "  dump <card_id>   print path of the worker's full activity log file")
	fmt.Fprintln(r.out, "  help             print this help")
	fmt.Fprintln(r.out, "  quit | exit      leave the REPL")
}

func (r *REPL) cmdLs() {
	statuses := r.provider.ListCards()
	if len(statuses) == 0 {
		fmt.Fprintln(r.out, "(no active workers)")
		return
	}
	fmt.Fprintf(r.out, "%-26s  %-18s  %-6s  %s\n", "CARD_ID", "STATE", "INBOX", "LAST_EVENT")
	for _, s := range statuses {
		fmt.Fprintf(r.out, "%-26s  %-18s  %-6d  %s ago\n",
			s.CardID, s.State, s.InboxDepth, ago(s.LastEventAt))
	}
}

func (r *REPL) cmdShow(cardID string) {
	st, entries, ok := r.provider.Snapshot(cardID)
	if !ok {
		fmt.Fprintf(r.out, "no active worker for card %q\n", cardID)
		return
	}
	fmt.Fprintf(r.out, "card_id:        %s\n", st.CardID)
	fmt.Fprintf(r.out, "state:          %s\n", st.State)
	if st.CurrentTool != "" {
		fmt.Fprintf(r.out, "current_tool:   %s (running %s)\n", st.CurrentTool, since(st.CurrentToolStartedAt))
	}
	fmt.Fprintf(r.out, "inbox_depth:    %d\n", st.InboxDepth)
	fmt.Fprintf(r.out, "model:          %s\n", st.Model)
	fmt.Fprintf(r.out, "session_id:     %s\n", st.SessionID)
	fmt.Fprintf(r.out, "turn_tokens:    in=%d out=%d\n", st.TurnTokensIn, st.TurnTokensOut)
	fmt.Fprintf(r.out, "last_event_at:  %s ago\n", ago(st.LastEventAt))
	if st.LastError != "" {
		fmt.Fprintf(r.out, "last_error:     %s\n", st.LastError)
	}
	if st.LastAssistantPreview != "" {
		fmt.Fprintf(r.out, "last_assistant: %s\n", st.LastAssistantPreview)
	}
	fmt.Fprintln(r.out)
	fmt.Fprintf(r.out, "recent activity (oldest first, %d entries):\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(r.out, "  %s  %-18s  %s\n", e.At.Format("15:04:05"), e.Kind, formatEntryDetail(e))
	}
}

func (r *REPL) cmdDump(cardID string) {
	st, _, ok := r.provider.Snapshot(cardID)
	if !ok {
		fmt.Fprintf(r.out, "no active worker for card %q\n", cardID)
		return
	}
	if st.LogPath == "" {
		fmt.Fprintf(r.out, "activity log unavailable for card %q\n", cardID)
		return
	}
	fmt.Fprintf(r.out, "activity log: %s\n", st.LogPath)
}

func formatEntryDetail(e ActivityEntry) string {
	if e.Tool != "" && e.Summary != "" {
		return e.Tool + " " + e.Summary
	}
	if e.Tool != "" {
		return e.Tool
	}
	return e.Summary
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	return d.String()
}

func since(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return time.Since(t).Round(time.Second).String()
}
