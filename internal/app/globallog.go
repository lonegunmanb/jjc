package app

import (
	"sync"
	"time"
)

// GlobalEvent is a single gateway-level event visible in the TUI's
// "Global Events" panel. These are high-level lifecycle events (routing
// decisions, worker registration, idle reaps) — not per-worker SDK events.
type GlobalEvent struct {
	At      time.Time
	CardID  string
	Kind    string
	Summary string
}

// GlobalEventLog is a thread-safe ring buffer of gateway-level events.
type GlobalEventLog struct {
	mu       sync.RWMutex
	entries  []GlobalEvent
	head     int
	ringSize int
}

// NewGlobalEventLog creates a ring buffer with the given capacity.
func NewGlobalEventLog(ringSize int) *GlobalEventLog {
	if ringSize <= 0 {
		ringSize = 128
	}
	return &GlobalEventLog{ringSize: ringSize}
}

// Record appends a global event to the ring buffer.
func (g *GlobalEventLog) Record(cardID, kind, summary string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := GlobalEvent{At: time.Now(), CardID: cardID, Kind: kind, Summary: summary}
	if len(g.entries) < g.ringSize {
		g.entries = append(g.entries, e)
		return
	}
	g.entries[g.head] = e
	g.head = (g.head + 1) % g.ringSize
}

// Entries returns all events in chronological order (oldest first).
func (g *GlobalEventLog) Entries() []GlobalEvent {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := len(g.entries)
	out := make([]GlobalEvent, n)
	if n < g.ringSize {
		copy(out, g.entries)
		return out
	}
	copy(out, g.entries[g.head:])
	copy(out[n-g.head:], g.entries[:g.head])
	return out
}
