// Package prompttmpl pre-renders playbook .md files at process startup.
//
// At startup the gateway is given a single playbooks source directory
// (the resolved local view of --config-src / JJC_CONFIG_SRC, which
// holds both router.hcl and every playbook .md file). All .md files
// under that directory are copied into a process-level temp directory
// created via os.MkdirTemp, then a tiny template pass rewrites every
// `{{<basename>}}` reference inside the copied files to the absolute
// path of <basename> in the same temp directory. The runner then loads
// the assembled worker system prompt from these rendered files
// instead of the //go:embed snapshots in internal/app/prompts.
//
// The renderer also seeds a small set of "embedded defaults" (the
// skeleton prompts: BOOTSTRAP/IDENTITY/WORKER/TOOLS/USER) so the gateway
// runs without requiring the operator to copy them into <playbooks-dir>;
// any user file with the same basename overrides the embedded copy.
//
// The temp dir lives for the lifetime of the process and is removed on
// shutdown via Renderer.Cleanup. Source playbooks (under
// <playbooks-dir>) and the embedded defaults are never modified.
package prompttmpl

import (
	"errors"
	"fmt"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Renderer owns the per-process temp directory holding the rendered
// playbook copies. Construct one via New, query it via Path / Read /
// Files, and call Cleanup at process exit.
type Renderer struct {
	dir        string            // absolute path to the temp directory
	files      map[string]string // basename -> absolute path inside dir
	kanbanVars map[string]string // accept-list for `{{kanban.*}}` references (nil means "none allowed")
	logger     sysevent.Sink
}

// Options configures how a Renderer is built.
type Options struct {
	// PlaybooksDir is the user-controlled source directory. Required;
	// must exist and be a directory (the caller is expected to validate
	// it before passing it in, but New double-checks).
	PlaybooksDir string

	// EmbeddedDefaults maps basename -> file content for the built-in
	// skeleton prompts that ship with the binary. Each entry is written
	// to the temp dir before PlaybooksDir is overlaid, so a user file of
	// the same basename wins.
	EmbeddedDefaults map[string]string

	// KanbanVars is the substitution table the renderer uses for any
	// `{{...}}` reference whose trimmed body starts with `kanban.`.
	// The keys are the well-known template variables declared in
	// docs/playbook-template-variables.md §2 (and exported as
	// constants from internal/app/kanban for compile-time safety).
	//
	// The renderer is strict: any `{{kanban.<name>}}` reference whose
	// `<name>` is not present in this map causes startup to fail with
	// `event=playbook_render_failed reason=unknown_kanban_key` and a
	// non-zero exit. There is no fallback, by design — a typo such as
	// `{{kanban.plan.di}}` must surface at boot, not at the first
	// worker turn.
	//
	// Optional: when nil or empty, any `{{kanban.*}}` reference is
	// treated as an unknown key. A playbook that does not use the
	// kanban namespace at all is unaffected.
	KanbanVars map[string]string

	// Logger receives structured event lines (one per significant
	// step). Optional; defaults to sysevent.Default().
	Logger sysevent.Sink
}

// New builds a Renderer: it creates a temp directory, materialises the
// embedded defaults, copies every .md from PlaybooksDir on top, and
// then renders `{{<basename>}}` references to absolute paths inside the
// temp dir.
//
// On any failure (invalid PlaybooksDir, copy error, missing reference,
// invalid reference name) the temp dir is removed before returning so a
// failed boot leaves no stale state behind.
func New(opts Options) (*Renderer, error) {
	logger := opts.Logger
	if logger == nil {
		logger = sysevent.Default()
	}

	if opts.PlaybooksDir == "" {
		return nil, errors.New("prompttmpl: PlaybooksDir is required")
	}
	info, err := os.Stat(opts.PlaybooksDir)
	if err != nil {
		return nil, fmt.Errorf("prompttmpl: stat playbooks dir %q: %w", opts.PlaybooksDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("prompttmpl: playbooks dir %q is not a directory", opts.PlaybooksDir)
	}

	tempDir, err := os.MkdirTemp("", "jjc-playbooks-*")
	if err != nil {
		return nil, fmt.Errorf("prompttmpl: create temp dir: %w", err)
	}
	sysevent.Emitf(logger, "playbooks_tempdir_created", "path=%s source=%s", tempDir, opts.PlaybooksDir)

	r := &Renderer{
		dir:        tempDir,
		files:      map[string]string{},
		kanbanVars: opts.KanbanVars,
		logger:     logger,
	}

	cleanupOnError := func() {
		if rmErr := os.RemoveAll(tempDir); rmErr != nil {
			sysevent.Emitf(logger, "playbooks_tempdir_cleanup_failed", "path=%s err=%v", tempDir, rmErr)
		}
	}

	// 1. Materialise embedded defaults (lowest priority).
	for name, content := range opts.EmbeddedDefaults {
		if err := validateBasename(name); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("prompttmpl: embedded default %q: %w", name, err)
		}
		if int64(len(content)) > MaxPlaybookFileBytes {
			cleanupOnError()
			return nil, fmt.Errorf("prompttmpl: embedded default %q exceeds %d bytes",
				name, MaxPlaybookFileBytes)
		}
		dst := filepath.Join(tempDir, name)
		if werr := os.WriteFile(dst, []byte(content), 0o644); werr != nil {
			cleanupOnError()
			return nil, fmt.Errorf("prompttmpl: write embedded default %q: %w", name, werr)
		}
		r.files[name] = dst
	}

	// 2. Overlay user-provided .md files (overwrites same-name embedded
	//    defaults). Recurse one level only? Issue says flat layout. Use
	//    ReadDir to keep things simple and reject subdirs implicitly.
	entries, err := os.ReadDir(opts.PlaybooksDir)
	if err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("prompttmpl: read playbooks dir %q: %w", opts.PlaybooksDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		if err := validateBasename(name); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("prompttmpl: source file %q: %w", name, err)
		}
		src := filepath.Join(opts.PlaybooksDir, name)
		// Reject symlinks: combined with the size cap the renderer is
		// safe against an oversized .md, but a symlink target like
		// /dev/zero or /etc/shadow could otherwise let a misconfigured
		// PlaybooksDir leak host content into a worker prompt or stall
		// startup. The intent of --config-src is "real .md files
		// here, period"; reflect that with an explicit Lstat check.
		lst, lerr := os.Lstat(src)
		if lerr != nil {
			cleanupOnError()
			return nil, fmt.Errorf("prompttmpl: lstat %q: %w", src, lerr)
		}
		if lst.Mode()&os.ModeSymlink != 0 {
			cleanupOnError()
			return nil, fmt.Errorf("prompttmpl: source file %q is a symlink (not allowed)", src)
		}
		if !lst.Mode().IsRegular() {
			cleanupOnError()
			return nil, fmt.Errorf("prompttmpl: source file %q is not a regular file", src)
		}
		dst := filepath.Join(tempDir, name)
		if cerr := copyFile(src, dst); cerr != nil {
			cleanupOnError()
			return nil, fmt.Errorf("prompttmpl: copy %q -> %q: %w", src, dst, cerr)
		}
		r.files[name] = dst
	}

	// 3. Render every file in temp dir: substitute `{{basename}}` -> abs path,
	//    and `{{kanban.*}}` -> the corresponding entry from r.kanbanVars.
	for name, path := range r.files {
		if err := r.renderFile(name, path); err != nil {
			cleanupOnError()
			return nil, err
		}
	}

	sysevent.Emitf(logger, "playbooks_rendered", "count=%d dir=%s kanban_vars=%d",
		len(r.files), tempDir, len(r.kanbanVars))
	return r, nil
}

// Dir returns the absolute path of the per-process temp directory.
func (r *Renderer) Dir() string { return r.dir }

// Files returns the basenames of every rendered file, sorted for stable
// iteration.
func (r *Renderer) Files() []string {
	out := make([]string, 0, len(r.files))
	for n := range r.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Path returns the absolute temp-dir path for the named playbook (by
// bare basename). The boolean is false if the playbook is not present.
func (r *Renderer) Path(name string) (string, bool) {
	p, ok := r.files[name]
	return p, ok
}

// Read returns the rendered content of the named playbook (by bare
// basename) or an error if it is not present or unreadable.
func (r *Renderer) Read(name string) (string, error) {
	p, ok := r.files[name]
	if !ok {
		return "", fmt.Errorf("prompttmpl: playbook %q not found in %s", name, r.dir)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("prompttmpl: read %q: %w", p, err)
	}
	return string(b), nil
}

// Cleanup removes the temp directory. Safe to call multiple times.
func (r *Renderer) Cleanup() error {
	if r == nil || r.dir == "" {
		return nil
	}
	err := os.RemoveAll(r.dir)
	if err != nil {
		sysevent.Emitf(r.logger, "playbooks_tempdir_cleanup_failed", "path=%s err=%v", r.dir, err)
		return err
	}
	sysevent.Emitf(r.logger, "playbooks_tempdir_cleaned", "path=%s", r.dir)
	r.dir = ""
	r.files = nil
	return nil
}

// MaxRenderedFileBytes is the per-file ceiling on rendered output.
// Substitution can grow a file when `{{X.md}}` references resolve to
// long absolute paths; this cap keeps a pathological playbook (or a
// tempdir on a deeply-nested path) from producing arbitrarily large
// rendered output. Generous relative to MaxPlaybookFileBytes.
const MaxRenderedFileBytes int64 = 4 << 20

// renderFile reads the file at path, substitutes every `{{<basename>}}`
// occurrence with the absolute path of <basename> in r.dir and every
// `{{kanban.*}}` occurrence with the corresponding entry from
// r.kanbanVars, and writes the result back. Any reference whose target
// is missing or whose name is invalid (contains `/`, `\`, `..`, or is
// empty) causes an error carrying the source file, 1-based line/column,
// and the offending reference.
func (r *Renderer) renderFile(name, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("prompttmpl: read %q: %w", path, err)
	}
	rendered, rerr := renderBytes(src, name, r.files, r.kanbanVars)
	if rerr != nil {
		sysevent.Emitf(r.logger, "playbook_render_failed", "file=%s line=%d column=%d reference=%q reason=%s",
			name, rerr.Line, rerr.Column, rerr.Reference, rerr.Reason)
		return rerr
	}
	if int64(len(rendered)) > MaxRenderedFileBytes {
		sysevent.Emitf(r.logger, "playbook_render_failed", "file=%s reason=rendered_size_exceeded size=%d limit=%d",
			name, len(rendered), MaxRenderedFileBytes)
		return fmt.Errorf("prompttmpl: rendered %q is %d bytes, exceeds %d",
			name, len(rendered), MaxRenderedFileBytes)
	}
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return fmt.Errorf("prompttmpl: write rendered %q: %w", path, err)
	}
	return nil
}

// RenderError is returned by the substitution pass. Fields are exported
// so callers (and tests) can assert against them.
type RenderError struct {
	File      string // source basename (e.g. "WORKER.md")
	Line      int    // 1-based line number where the offending `{{...}}` starts
	Column    int    // 1-based column (byte offset within the line) of the `{{`
	Reference string // raw text between `{{` and `}}`, including any whitespace
	Reason    string // short machine-readable code, e.g. referenced_file_not_found
	Detail    string // human-readable explanation
}

func (e *RenderError) Error() string {
	return fmt.Sprintf("prompttmpl: %s: %s:%d:%d: reference %q: %s",
		e.Reason, e.File, e.Line, e.Column, e.Reference, e.Detail)
}

// kanbanRefPrefix is the namespace under which template variables
// drawn from the resolved `kanban {}` view are exposed. Mirrors
// kanban.PromptVarPrefix; duplicated here as a literal to keep this
// package's dependency surface minimal (the renderer takes a plain
// map and does not import the kanban package).
const kanbanRefPrefix = "kanban."

// renderBytes performs the substitution pass on src and returns the
// rewritten bytes. Exposed (lower-case) for unit testing.
//
// References are dispatched by the trimmed body inside `{{...}}`:
//   - body starts with "kanban."  → looked up in kanbanVars; unknown
//     keys produce a RenderError with reason `unknown_kanban_key`
//   - otherwise                   → validated as a basename and looked
//     up in files; unknown names produce a RenderError with reason
//     `referenced_file_not_found`
func renderBytes(src []byte, file string, files map[string]string, kanbanVars map[string]string) ([]byte, *RenderError) {
	var out strings.Builder
	out.Grow(len(src))

	line, col := 1, 1
	i := 0
	for i < len(src) {
		// Track line/column for the next byte we are about to emit or
		// scan past. When we hit `{{` we capture line/col of that `{{`.
		if i+1 < len(src) && src[i] == '{' && src[i+1] == '{' {
			refStart := i + 2
			refEnd := -1
			for j := refStart; j+1 < len(src); j++ {
				if src[j] == '}' && src[j+1] == '}' {
					refEnd = j
					break
				}
			}
			if refEnd < 0 {
				// Unterminated `{{` — treat as literal and move on.
				out.WriteByte(src[i])
				if src[i] == '\n' {
					line++
					col = 1
				} else {
					col++
				}
				i++
				continue
			}
			rawRef := string(src[refStart:refEnd])
			refName := strings.TrimSpace(rawRef)

			var replacement string
			switch {
			case strings.HasPrefix(refName, kanbanRefPrefix):
				// Kanban template variable. Strict-mode lookup against
				// the operator-supplied map; an unknown key is a
				// startup-time fatal error per
				// docs/playbook-template-variables.md §3.2.
				val, ok := kanbanVars[refName]
				if !ok {
					return nil, &RenderError{
						File:      file,
						Line:      line,
						Column:    col,
						Reference: rawRef,
						Reason:    "unknown_kanban_key",
						Detail:    fmt.Sprintf("no kanban template variable named %q is defined", refName),
					}
				}
				replacement = val
			default:
				// Cross-playbook basename reference.
				if err := validateBasename(refName); err != nil {
					return nil, &RenderError{
						File:      file,
						Line:      line,
						Column:    col,
						Reference: rawRef,
						Reason:    "invalid_reference_name",
						Detail:    err.Error(),
					}
				}
				abs, ok := files[refName]
				if !ok {
					return nil, &RenderError{
						File:      file,
						Line:      line,
						Column:    col,
						Reference: rawRef,
						Reason:    "referenced_file_not_found",
						Detail:    fmt.Sprintf("no playbook named %q has been rendered", refName),
					}
				}
				replacement = abs
			}

			out.WriteString(replacement)
			// Advance i past the closing `}}` and update line/col by
			// scanning the consumed slice for newlines.
			consumed := src[i : refEnd+2]
			for _, b := range consumed {
				if b == '\n' {
					line++
					col = 1
				} else {
					col++
				}
			}
			i = refEnd + 2
			continue
		}

		out.WriteByte(src[i])
		if src[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i++
	}
	return []byte(out.String()), nil
}

// validateBasename rejects empty names, names containing path separators
// (`/`, `\`), and `..` (anywhere) so a playbook reference cannot escape
// the temp directory.
// MaxPlaybookFileBytes caps the size of any single source playbook the
// renderer is willing to copy into the temp dir. The limit also bounds
// the size of every rendered file (it can only grow if a `{{...}}`
// expansion adds a path string, which is at most a few hundred bytes).
// 1 MiB comfortably fits the largest playbook in the repo today
// (~85 KiB) while preventing an accidental or malicious multi-GiB file
// from exhausting disk and memory.
const MaxPlaybookFileBytes int64 = 1 << 20

func validateBasename(name string) error {
	if name == "" {
		return errors.New("empty name")
	}
	if strings.ContainsAny(name, `/\`) {
		return errors.New("name contains a path separator")
	}
	if strings.Contains(name, "..") {
		return errors.New("name contains \"..\"")
	}
	return nil
}

// copyFile copies src to dst, refusing to write more than
// MaxPlaybookFileBytes. Returns an error if the source exceeds the cap
// (the partial output file is left in place — the caller cleans up the
// whole temp dir on any error).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// CopyN(..., max+1) lets us detect "exactly one byte past the cap"
	// versus "smaller than the cap". A short read (n <= max) plus
	// io.EOF is the success path.
	n, copyErr := io.CopyN(out, in, MaxPlaybookFileBytes+1)
	if copyErr != nil && copyErr != io.EOF {
		_ = out.Close()
		return copyErr
	}
	if n > MaxPlaybookFileBytes {
		_ = out.Close()
		return fmt.Errorf("playbook %q exceeds %d bytes", src, MaxPlaybookFileBytes)
	}
	return out.Close()
}
