package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfigSrc_EmptyRejected(t *testing.T) {
	_, _, err := ResolveConfigSrc(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error for empty src")
	}
}

func TestResolveConfigSrc_LocalDirReturnedInPlace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "router.hcl"), []byte("# stub\n"), 0o644); err != nil {
		t.Fatalf("write router.hcl: %v", err)
	}

	resolved, cleanup, err := ResolveConfigSrc(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	t.Cleanup(cleanup)

	wantAbs, _ := filepath.Abs(dir)
	if resolved != wantAbs {
		t.Fatalf("expected resolved to be %q, got %q", wantAbs, resolved)
	}
	// cleanup must be a no-op for local directories: the original
	// directory must still exist after invoking it.
	cleanup()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("local config dir was unexpectedly removed by cleanup: %v", err)
	}
}

func TestResolveConfigSrc_LocalFileRejected(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, _, err := ResolveConfigSrc(context.Background(), f, nil)
	if err == nil {
		t.Fatal("expected error when src exists locally but is a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error should mention not a directory, got %v", err)
	}
}
