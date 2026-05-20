package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// WorkDirInfo describes the per-card working directory after the gateway
// has finished preparing it (mkdir + optional git clone). It is what each
// registered WorkDirHook receives and what the runner uses to seed the
// Copilot SDK session's WorkingDirectory.
//
// The struct is intentionally a flat snapshot — hooks must not mutate it.
type WorkDirInfo struct {
	// CardID is the Trello card id this work_dir belongs to.
	CardID string

	// WorkDir is the absolute path of the per-card working directory
	// (currently always C:\project\<card_id>).
	WorkDir string

	// Classification carries rule/GitHub metadata for the card.
	// Owner / Repo / Number / URL may be empty when no GitHub URL was
	// found in the card description (generic cards).
	Classification CardClassification

	// CreatedDir is true when WorkDir did not exist before this preparation
	// pass and we created it. False when WorkDir already existed.
	CreatedDir bool

	// Cloned is true when this preparation pass performed a fresh
	// `git clone` into WorkDir. False when no clone was attempted (no
	// GitHub repo on the card) or when WorkDir already had a .git
	// directory and the clone step was skipped.
	Cloned bool

	// CloneSkippedExisting is true when WorkDir already contained a
	// `.git` entry so the clone step was deliberately skipped (idempotent
	// re-prepare). Mutually exclusive with Cloned.
	CloneSkippedExisting bool

	// CloneError, when non-nil, indicates that a clone was attempted but
	// failed. The runner still returns the WorkDir so the worker can try
	// to recover; hooks can decide what to do about it (e.g. surface to
	// Trello).
	CloneError error
}

// HasGitHubRepo reports whether the classification carries a usable
// owner/repo pair to drive a git clone.
func (i WorkDirInfo) HasGitHubRepo() bool {
	return i.Classification.GitHub.Present()
}

// CloneURL returns the canonical HTTPS clone URL derived from the
// classification, or "" when no GitHub repo was attached.
func (i WorkDirInfo) CloneURL() string {
	if !i.HasGitHubRepo() {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/%s.git",
		i.Classification.GitHub.Owner, i.Classification.GitHub.Repo)
}

// WorkDirHook is a callback invoked by the runner immediately after the
// per-card work_dir has been prepared (mkdir + optional clone) and before
// the Copilot session is created.
//
// Hooks run sequentially in registration order. A hook returning an error
// is logged but does not abort the rest of the chain or the session
// creation — registering a hook is a "best effort enrichment" affordance,
// not a way to gate worker creation.
//
// Hooks must not mutate the supplied WorkDirInfo. They may run external
// commands, talk to Trello, refresh upstream instruction files, etc., as
// long as they return promptly (< a few seconds). Long-running setup
// belongs in a separate goroutine the hook spawns itself.
type WorkDirHook func(ctx context.Context, info WorkDirInfo) error

// GitRunner abstracts the `git` invocation so tests can substitute a fake
// without shelling out. The default implementation uses os/exec with the
// system `git` binary. Output is the combined stdout+stderr to make logs
// useful when an error is returned.
type GitRunner func(ctx context.Context, args ...string) (output []byte, err error)

// defaultGitRunner shells out to `git`.
func defaultGitRunner(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	return cmd.CombinedOutput()
}

// WorkDirPreparer turns a card classification into a ready-to-use
// per-card working directory and runs registered hooks against it.
//
// The preparer is safe for concurrent use: independent cards do not
// contend, and hook registration is allowed at any time (new hooks will
// be picked up by the next Prepare call).
type WorkDirPreparer struct {
	logger    sysevent.Sink
	gitRunner GitRunner
	// baseDir is the parent directory under which each card's work_dir is
	// created (work_dir = filepath.Join(baseDir, cardID)). Defaults to
	// C:\project to match the convention baked into WORKER.md and the
	// rest of the gateway. Tests override this with t.TempDir().
	baseDir string
	// cloneTimeout bounds a single `git clone` invocation. Zero means no
	// timeout (the parent context still applies).
	cloneTimeout time.Duration

	mu    sync.RWMutex
	hooks []WorkDirHook
}

// NewWorkDirPreparer returns a preparer wired to the real git binary, the
// canonical C:\project base directory, and a 60-second clone timeout.
// Pass nil logger to use log.Default.
func NewWorkDirPreparer(logger sysevent.Sink) *WorkDirPreparer {
	if logger == nil {
		logger = sysevent.Default()
	}
	return &WorkDirPreparer{
		logger:       logger,
		gitRunner:    defaultGitRunner,
		baseDir:      `C:\project`,
		cloneTimeout: 60 * time.Second,
	}
}

// SetBaseDir overrides the parent directory under which each card's
// work_dir is created. Empty string is rejected to avoid accidentally
// creating work dirs in the gateway's CWD.
func (p *WorkDirPreparer) SetBaseDir(dir string) {
	if dir == "" {
		return
	}
	p.baseDir = dir
}

// SetGitRunner installs a custom git invoker; intended for tests.
func (p *WorkDirPreparer) SetGitRunner(g GitRunner) {
	if g == nil {
		g = defaultGitRunner
	}
	p.gitRunner = g
}

// SetCloneTimeout overrides the per-clone timeout. Pass 0 to disable.
func (p *WorkDirPreparer) SetCloneTimeout(d time.Duration) { p.cloneTimeout = d }

// RegisterHook appends a WorkDirHook to the chain. Hooks are invoked in
// the order they were registered. Calling with nil is a no-op.
func (p *WorkDirPreparer) RegisterHook(h WorkDirHook) {
	if h == nil {
		return
	}
	p.mu.Lock()
	p.hooks = append(p.hooks, h)
	p.mu.Unlock()
}

// hooksSnapshot returns a copy of the current hook slice so a long-running
// hook chain doesn't observe a torn slice if a new hook is registered
// mid-flight.
func (p *WorkDirPreparer) hooksSnapshot() []WorkDirHook {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.hooks) == 0 {
		return nil
	}
	out := make([]WorkDirHook, len(p.hooks))
	copy(out, p.hooks)
	return out
}

// Prepare ensures C:\project\<card_id> exists, optionally clones the
// GitHub repo derived from the classification, then runs every registered
// hook against the resulting WorkDirInfo.
//
// Errors from hook execution are logged but do not bubble up — a misbehaving
// hook must not be able to brick worker session creation. Errors from the
// directory or clone phases are returned so the caller can decide whether
// to abort session creation (the runner aborts only when MkdirAll itself
// fails: a non-existent WorkingDirectory is rejected by the SDK).
func (p *WorkDirPreparer) Prepare(ctx context.Context, cardID string, c CardClassification) (WorkDirInfo, error) {
	if cardID == "" {
		return WorkDirInfo{}, errors.New("prepare workdir: empty card id")
	}
	// Final guard before cardID is filepath.Joined. Route already drops
	// invalid ids, but Prepare is also called from tests and from any
	// future codepath that doesn't go through Route, so we re-check
	// here. A bad id at this point is a programmer error, not a routing
	// decision — return an error rather than a routing-style "drop".
	if err := ValidateCardID(cardID); err != nil {
		return WorkDirInfo{}, fmt.Errorf("prepare workdir: %w", err)
	}

	workDir := filepath.Join(p.baseDir, cardID)
	info := WorkDirInfo{
		CardID:         cardID,
		WorkDir:        workDir,
		Classification: c,
	}

	preexisted := dirExists(workDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		sysevent.Emitf(p.logger, "workdir_mkdir_failed", "card_id=%s work_dir=%s err=%v",
			cardID, workDir, err)
		return info, fmt.Errorf("ensure work_dir %s: %w", workDir, err)
	}
	info.CreatedDir = !preexisted
	if info.CreatedDir {
		sysevent.Emitf(p.logger, "workdir_created", "card_id=%s work_dir=%s", cardID, workDir)
	} else {
		sysevent.Emitf(p.logger, "workdir_reused", "card_id=%s work_dir=%s", cardID, workDir)
	}

	if info.HasGitHubRepo() {
		p.maybeClone(ctx, &info)
	}

	p.runHooks(ctx, info)
	return info, nil
}

// maybeClone runs `git clone --depth 1 <url> <work_dir>` when WorkDir does
// not already contain a .git entry. It records the outcome on info and
// never returns — clone failures are non-fatal at this layer.
func (p *WorkDirPreparer) maybeClone(ctx context.Context, info *WorkDirInfo) {
	gitDir := filepath.Join(info.WorkDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		info.CloneSkippedExisting = true
		sysevent.Emitf(p.logger, "workdir_clone_skipped_existing", "card_id=%s work_dir=%s",
			info.CardID, info.WorkDir)
		return
	}

	cloneCtx := ctx
	if p.cloneTimeout > 0 {
		var cancel context.CancelFunc
		cloneCtx, cancel = context.WithTimeout(ctx, p.cloneTimeout)
		defer cancel()
	}

	url := info.CloneURL()
	start := time.Now()
	out, err := p.gitRunner(cloneCtx, "clone", "--depth", "1", url, info.WorkDir)
	if err != nil {
		info.CloneError = err
		sysevent.Emitf(p.logger, "workdir_clone_failed", "card_id=%s url=%s duration=%s err=%v output=%q",
			info.CardID, url, time.Since(start), err, truncateBytes(out, 400))
		return
	}
	info.Cloned = true
	sysevent.Emitf(p.logger, "workdir_cloned", "card_id=%s url=%s work_dir=%s duration=%s",
		info.CardID, url, info.WorkDir, time.Since(start))
}

// runHooks invokes each registered hook in order. Errors are logged with
// the hook index so the offending hook can be tracked, but execution
// continues so a single bad hook can't disable downstream ones.
func (p *WorkDirPreparer) runHooks(ctx context.Context, info WorkDirInfo) {
	hooks := p.hooksSnapshot()
	if len(hooks) == 0 {
		return
	}
	for idx, h := range hooks {
		start := time.Now()
		err := safeInvokeHook(ctx, h, info)
		dur := time.Since(start)
		if err != nil {
			sysevent.Emitf(p.logger, "workdir_hook_failed", "card_id=%s hook_index=%d duration=%s err=%v",
				info.CardID, idx, dur, err)
			continue
		}
		sysevent.Emitf(p.logger, "workdir_hook_ok", "card_id=%s hook_index=%d duration=%s",
			info.CardID, idx, dur)
	}
}

// safeInvokeHook converts panics in user-supplied hooks into errors so a
// bad hook can't take down the gateway.
func safeInvokeHook(ctx context.Context, h WorkDirHook, info WorkDirInfo) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("hook panic: %v", r)
		}
	}()
	return h(ctx, info)
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.IsDir()
}

func truncateBytes(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}
