package prompttmpl

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
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

// TestRender_RejectsOversizedSourceFile asserts that a single playbook
// larger than MaxPlaybookFileBytes causes New to fail and clean up.
func TestRender_RejectsOversizedSourceFile(t *testing.T) {
	src := t.TempDir()
	// MaxPlaybookFileBytes + 1 byte of payload triggers the cap.
	huge := strings.Repeat("A", int(MaxPlaybookFileBytes)+1)
	writeFile(t, src, "huge.md", huge)

	_, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
	if err == nil {
		t.Fatal("expected error for oversized source playbook")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-related error, got: %v", err)
	}
}

// TestRender_RejectsOversizedEmbeddedDefault asserts that an embedded
// default content larger than MaxPlaybookFileBytes is rejected at New
// time so a future buggy embed cannot ship past the size invariant.
func TestRender_RejectsOversizedEmbeddedDefault(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "ok.md", "ok\n")
	huge := strings.Repeat("X", int(MaxPlaybookFileBytes)+1)

	_, err := New(Options{
		PlaybooksDir:     src,
		EmbeddedDefaults: map[string]string{"BIG.md": huge},
		Logger:           discardLogger(),
	})
	if err == nil {
		t.Fatal("expected error for oversized embedded default")
	}
}

// TestRender_RejectsSymlinkSource asserts that a symlink in the
// PlaybooksDir is refused at New time. Without this guard a malicious
// or misconfigured deployment could point a `.md` symlink at
// /dev/zero, /etc/shadow, or any other host file and either stall
// startup or leak its content into a worker prompt.
//
// Skips on Windows where creating a symlink without elevation is not
// reliable; the underlying os.Lstat / os.ModeSymlink contract is
// platform-independent so the lint of the check itself is covered by
// the unix build.
func TestRender_RejectsSymlinkSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	src := t.TempDir()
	target := filepath.Join(src, "real.md")
	if err := os.WriteFile(target, []byte("real body\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(src, "linked.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported on this filesystem: %v", err)
	}

	_, err := New(Options{PlaybooksDir: src, Logger: discardLogger()})
	if err == nil {
		t.Fatal("expected error for symlink in PlaybooksDir")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink-related error, got: %v", err)
	}
}

// TestRender_SubstitutesKanbanVar verifies the core happy path: a
// `{{kanban.<key>}}` reference is replaced by the matching entry in
// Options.KanbanVars and the result no longer contains any `{{`.
func TestRender_SubstitutesKanbanVar(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "rules.md",
		"target_list_id = {{kanban.action.id}} (default name {{kanban.action.name}})\n")

	r, err := New(Options{
		PlaybooksDir: src,
		KanbanVars: map[string]string{
			"kanban.action.id":   "abc123",
			"kanban.action.name": "In action",
		},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Cleanup()

	got, err := r.Read("rules.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "target_list_id = abc123 (default name In action)\n"
	if got != want {
		t.Fatalf("unexpected render output\nwant: %q\ngot:  %q", want, got)
	}
	if strings.Contains(got, "{{") {
		t.Fatalf("expected no `{{` left in rendered output, got: %s", got)
	}
}

// TestRender_UnknownKanbanKeyFails asserts strict mode: a
// `{{kanban.<key>}}` whose `<key>` is not in KanbanVars (typo,
// renamed key, etc.) must fail New with a *RenderError carrying
// reason=unknown_kanban_key and the original line/column of the `{{`.
func TestRender_UnknownKanbanKeyFails(t *testing.T) {
	src := t.TempDir()
	// reference on line 3 of the file; deliberate typo `.di` for `.id`.
	writeFile(t, src, "rules.md",
		"line one\nline two\nuse {{ kanban.plan.di }} here\n")

	_, err := New(Options{
		PlaybooksDir: src,
		KanbanVars: map[string]string{
			"kanban.plan.id":   "p1",
			"kanban.plan.name": "Analyze",
		},
		Logger: discardLogger(),
	})
	if err == nil {
		t.Fatal("expected error for unknown kanban key")
	}
	var rerr *RenderError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *RenderError, got %T: %v", err, err)
	}
	if rerr.Reason != "unknown_kanban_key" {
		t.Fatalf("unexpected reason: %s", rerr.Reason)
	}
	if rerr.File != "rules.md" {
		t.Fatalf("unexpected file: %s", rerr.File)
	}
	if rerr.Line != 3 {
		t.Fatalf("expected line 3, got %d", rerr.Line)
	}
	if strings.TrimSpace(rerr.Reference) != "kanban.plan.di" {
		t.Fatalf("unexpected reference: %q", rerr.Reference)
	}
}

// TestRender_UnknownKanbanKeyWithNilKanbanVarsFails ensures the strict
// behaviour also applies when KanbanVars is nil: a playbook that uses
// the kanban namespace at all without anyone supplying values must
// fail at boot, not at runtime.
func TestRender_UnknownKanbanKeyWithNilKanbanVarsFails(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "rules.md", "use {{kanban.action.id}} here\n")

	_, err := New(Options{
		PlaybooksDir: src,
		// KanbanVars deliberately left nil
		Logger: discardLogger(),
	})
	if err == nil {
		t.Fatal("expected error for unknown kanban key with nil KanbanVars")
	}
	var rerr *RenderError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *RenderError, got %T: %v", err, err)
	}
	if rerr.Reason != "unknown_kanban_key" {
		t.Fatalf("unexpected reason: %s", rerr.Reason)
	}
}

// TestRender_KanbanAndBasenameRefsCoexist verifies that the two
// reference kinds are dispatched independently and can appear on the
// same line without interference.
func TestRender_KanbanAndBasenameRefsCoexist(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "main.md",
		"see {{shared.md}} for the rule about {{kanban.action.name}}\n")
	writeFile(t, src, "shared.md", "shared body\n")

	r, err := New(Options{
		PlaybooksDir: src,
		KanbanVars: map[string]string{
			"kanban.action.name": "In action",
		},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Cleanup()

	got, err := r.Read("main.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	sharedPath, _ := r.Path("shared.md")
	if !strings.Contains(got, sharedPath) {
		t.Fatalf("expected basename ref resolved to %q, got: %s", sharedPath, got)
	}
	if !strings.Contains(got, "In action") {
		t.Fatalf("expected kanban ref resolved to In action, got: %s", got)
	}
	if strings.Contains(got, "{{") {
		t.Fatalf("expected no `{{` left in output, got: %s", got)
	}
}

// TestRender_KanbanRefValueWithSpecialCharsIsLiteral verifies the
// substitution is a literal string write \u2014 no shell-style escaping,
// no markdown handling, no surprises. A value like `[claw]:` (the kind
// of thing an operator might pick as a custom comment prefix) must
// land verbatim.
func TestRender_KanbanRefValueWithSpecialCharsIsLiteral(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "rules.md", "prefix={{kanban.agent_comment_prefix}}\n")

	r, err := New(Options{
		PlaybooksDir: src,
		KanbanVars: map[string]string{
			"kanban.agent_comment_prefix": "[claw]:",
		},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Cleanup()

	got, _ := r.Read("rules.md")
	if got != "prefix=[claw]:\n" {
		t.Fatalf("expected literal substitution, got: %q", got)
	}
}

// TestRender_KanbanRefDoesNotShadowBasenameValidation guards against a
// regression where a non-kanban reference accidentally goes through
// the kanban path (or vice versa). Specifically, a reference that
// merely *contains* `kanban.` somewhere other than the prefix must
// still be treated as a basename reference (and therefore fail
// basename validation if it has a slash, etc.).
func TestRender_KanbanRefDoesNotShadowBasenameValidation(t *testing.T) {
	src := t.TempDir()
	// `mykanban.md` does NOT start with "kanban." \u2014 it's a basename.
	writeFile(t, src, "main.md", "see {{mykanban.md}}\n")
	writeFile(t, src, "mykanban.md", "hi\n")

	r, err := New(Options{
		PlaybooksDir: src,
		KanbanVars:   map[string]string{}, // no kanban keys at all
		Logger:       discardLogger(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Cleanup()

	got, _ := r.Read("main.md")
	wantPath, _ := r.Path("mykanban.md")
	if !strings.Contains(got, wantPath) {
		t.Fatalf("expected basename ref resolved to %q, got: %s", wantPath, got)
	}
}

// TestRender_FailedKanbanRenderCleansTempDir mirrors the existing
// missing-basename cleanup test: a failed render must remove the temp
// dir so a failed boot leaves no stale state behind.
func TestRender_FailedKanbanRenderCleansTempDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "main.md", "{{kanban.plan.di}}\n") // unknown key

	beforeEntries, _ := os.ReadDir(os.TempDir())
	beforeCount := countOpenclawDirs(beforeEntries)

	_, err := New(Options{
		PlaybooksDir: src,
		KanbanVars:   map[string]string{"kanban.plan.id": "p1"},
		Logger:       discardLogger(),
	})
	if err == nil {
		t.Fatal("expected render error")
	}

	afterEntries, _ := os.ReadDir(os.TempDir())
	afterCount := countOpenclawDirs(afterEntries)
	if afterCount > beforeCount {
		t.Fatalf("failed render should clean up its temp dir: before=%d after=%d", beforeCount, afterCount)
	}
}
