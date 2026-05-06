package prompts

import (
	"strings"
	"testing"
)

func TestEmbeddedFilesNonEmpty(t *testing.T) {
	cases := map[string]string{
		"MANAGER":   EmbeddedManager(),
		"WORKER":    EmbeddedWorker(),
		"BOOTSTRAP": Bootstrap,
		"IDENTITY":  Identity,
		"TOOLS":     Tools,
		"USER":      User,
	}
	for name, content := range cases {
		if strings.TrimSpace(content) == "" {
			t.Fatalf("embedded %s.md is empty", name)
		}
	}
}

func TestResolveManagerFallsBackToEmbedded(t *testing.T) {
	got, override := ResolveManager()
	if override != "" {
		t.Logf("MANAGER.md override active at %s; skipping embedded equality", override)
		return
	}
	if got != EmbeddedManager() {
		t.Fatal("expected ResolveManager to return embedded content when no override exists")
	}
}

func TestResolveWorkerFallsBackToEmbedded(t *testing.T) {
	got, override := ResolveWorker()
	if override != "" {
		t.Logf("WORKER.md override active at %s; skipping embedded equality", override)
		return
	}
	if got != EmbeddedWorker() {
		t.Fatal("expected ResolveWorker to return embedded content when no override exists")
	}
}
