package app

import (
	"testing"
)

func TestGlobalEventLogRecordAndEntries(t *testing.T) {
	g := NewGlobalEventLog(4)
	g.Record("c1", "routed", "dispatch")
	g.Record("c2", "registered", "")
	entries := g.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2, got %d", len(entries))
	}
	if entries[0].CardID != "c1" || entries[1].CardID != "c2" {
		t.Fatalf("wrong order: %+v", entries)
	}
}

func TestGlobalEventLogRingWraparound(t *testing.T) {
	g := NewGlobalEventLog(3)
	g.Record("a", "x", "")
	g.Record("b", "x", "")
	g.Record("c", "x", "")
	g.Record("d", "x", "") // evicts "a"

	entries := g.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3, got %d", len(entries))
	}
	if entries[0].CardID != "b" {
		t.Fatalf("oldest should be b, got %s", entries[0].CardID)
	}
	if entries[2].CardID != "d" {
		t.Fatalf("newest should be d, got %s", entries[2].CardID)
	}
}

func TestGlobalEventLogNilSafe(t *testing.T) {
	var g *GlobalEventLog
	entries := g.Entries()
	if entries != nil {
		t.Fatalf("expected nil, got %+v", entries)
	}
}
