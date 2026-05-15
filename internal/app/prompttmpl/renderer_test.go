package prompttmpl

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestRender_SubstitutesBasenameToAbsPath(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "azurerm.md", "see {{shared.md}} for details\n")
	writeFile(t, src, "shared.md", "shared body\n")

	r, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Cleanup()

	got, err := r.Read("azurerm.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wantPath, _ := r.Path("shared.md")
	if !strings.Contains(got, wantPath) {
		t.Fatalf("expected rendered file to contain %q, got: %s", wantPath, got)
	}
	if strings.Contains(got, "{{") {
		t.Fatalf("expected no `{{` in rendered output, got: %s", got)
	}
}

func TestRender_EmbeddedDefaultsOverlaidByUserFile(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "WORKER.md", "user-supplied worker\n")

	r, err := New(Options{
		PlaybooksDir:     src,
		EmbeddedDefaults: map[string]string{"WORKER.md": "embedded worker", "BOOTSTRAP.md": "embedded bootstrap"},
		Logger:           discardLogger(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Cleanup()

	got, _ := r.Read("WORKER.md")
	if !strings.Contains(got, "user-supplied worker") {
		t.Fatalf("user file should override embedded; got %q", got)
	}
	got, _ = r.Read("BOOTSTRAP.md")
	if got != "embedded bootstrap" {
		t.Fatalf("embedded fallback expected; got %q", got)
	}
}

func TestRender_MissingReferenceFailsWithLineNumber(t *testing.T) {
	src := t.TempDir()
	// reference on line 3 of the file
	writeFile(t, src, "main.md", "alpha\nbeta\nsee {{ghost.md}} after this\n")

	_, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
	if err == nil {
		t.Fatal("expected error for missing reference")
	}
	var rerr *RenderError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *RenderError, got %T: %v", err, err)
	}
	if rerr.Reason != "referenced_file_not_found" {
		t.Fatalf("unexpected reason: %s", rerr.Reason)
	}
	if rerr.File != "main.md" {
		t.Fatalf("unexpected file: %s", rerr.File)
	}
	if rerr.Line != 3 {
		t.Fatalf("expected line 3, got %d", rerr.Line)
	}
	if strings.TrimSpace(rerr.Reference) != "ghost.md" {
		t.Fatalf("unexpected reference: %q", rerr.Reference)
	}
}

func TestRender_RejectsInvalidReferenceName(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"slash", "use {{sub/foo.md}} here\n"},
		{"backslash", "use {{sub\\foo.md}} here\n"},
		{"dotdot", "use {{../foo.md}} here\n"},
		{"empty", "use {{}} here\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := t.TempDir()
			writeFile(t, src, "main.md", tc.body)

			_, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
			if err == nil {
				t.Fatal("expected error for invalid reference")
			}
			var rerr *RenderError
			if !errors.As(err, &rerr) {
				t.Fatalf("expected *RenderError, got %T: %v", err, err)
			}
			if rerr.Reason != "invalid_reference_name" {
				t.Fatalf("unexpected reason: %s", rerr.Reason)
			}
		})
	}
}

func TestRender_RejectsMissingPlaybooksDir(t *testing.T) {
	_, err := New(Options{PlaybooksDir: filepath.Join(t.TempDir(), "nope"), Logger: discardLogger()})
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestRender_RejectsPlaybooksDirIsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{PlaybooksDir: f, Logger: discardLogger()})
	if err == nil {
		t.Fatal("expected error when playbooks dir is a file")
	}
}

func TestRender_CleanupRemovesTempDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "main.md", "hi\n")

	r, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	dir := r.Dir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp dir should exist before cleanup: %v", err)
	}
	if err := r.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected temp dir removed; stat err=%v", err)
	}
	// idempotent
	if err := r.Cleanup(); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

func TestRender_FailedRenderCleansTempDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "main.md", "{{missing.md}}\n")

	beforeEntries, _ := os.ReadDir(os.TempDir())
	beforeCount := countOpenclawDirs(beforeEntries)

	_, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
	if err == nil {
		t.Fatal("expected render error")
	}

	afterEntries, _ := os.ReadDir(os.TempDir())
	afterCount := countOpenclawDirs(afterEntries)
	if afterCount > beforeCount {
		t.Fatalf("failed render should clean up its temp dir: before=%d after=%d", beforeCount, afterCount)
	}
}

func countOpenclawDirs(entries []os.DirEntry) int {
	n := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "openclaw-playbooks-") {
			n++
		}
	}
	return n
}

func TestRender_IgnoresNonMarkdownFiles(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "main.md", "hi\n")
	writeFile(t, src, "ignored.txt", "{{should-not-be-parsed}}\n")

	r, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Cleanup()

	if _, ok := r.Path("ignored.txt"); ok {
		t.Fatal("non-.md file should not be loaded")
	}
}

func TestRender_FilesReturnsSortedBasenames(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "b.md", "hi\n")
	writeFile(t, src, "a.md", "hi\n")

	r, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Cleanup()

	files := r.Files()
	if len(files) != 2 || files[0] != "a.md" || files[1] != "b.md" {
		t.Fatalf("unexpected files (want sorted [a.md b.md]): %v", files)
	}
}
