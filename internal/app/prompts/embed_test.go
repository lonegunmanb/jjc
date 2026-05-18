package prompts

import (
	"strings"
	"testing"
)

func TestEmbeddedFilesNonEmpty(t *testing.T) {
	cases := map[string]string{
		"BOOTSTRAP": Bootstrap,
		"IDENTITY":  Identity,
		"WORKER":    Worker,
		"TOOLS":     Tools,
		"USER":      User,
	}
	for name, content := range cases {
		if strings.TrimSpace(content) == "" {
			t.Fatalf("embedded %s.md is empty", name)
		}
	}
}

func TestDefaultsCoversFiveSkeletonPrompts(t *testing.T) {
	d := Defaults()
	want := []string{"BOOTSTRAP.md", "IDENTITY.md", "WORKER.md", "TOOLS.md", "USER.md"}
	for _, name := range want {
		body, ok := d[name]
		if !ok {
			t.Fatalf("Defaults() missing %s", name)
		}
		if strings.TrimSpace(body) == "" {
			t.Fatalf("Defaults()[%s] is empty", name)
		}
	}
	if len(d) != len(want) {
		t.Fatalf("expected %d entries, got %d (%v)", len(want), len(d), d)
	}
}

func TestEmbeddedWorkerMatchesPackageVar(t *testing.T) {
	if EmbeddedWorker() != Worker {
		t.Fatal("EmbeddedWorker should return the package-level Worker string")
	}
}
