package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/lonegunmanb/trello-copilot/internal/app/prompts"
	"github.com/lonegunmanb/trello-copilot/internal/app/prompttmpl"
)

// DefaultCopilotModel is the model used when none is configured.
const DefaultCopilotModel = "claude-opus-4.6-1m"

// CopilotRunner owns the long-lived Copilot SDK client and produces one
// fresh worker session per Trello card on demand. It implements
// SessionFactory so a Dispatcher can ask it for new worker sessions.
//
// Routing decisions and per-card serialisation live in the Dispatcher; the
// runner is intentionally narrow: client lifecycle + worker-session
// construction.
type CopilotRunner struct {
	model      string
	logger     *log.Logger
	tmpDir     string
	dispatcher *Dispatcher

	// routerDir is the directory containing the per-work_type entry
	// playbook markdown files. cardInfoFetcher is used at session
	// creation time to derive work_type from the Trello card description.
	// Both are optional: when routerDir is empty or cardInfoFetcher is
	// nil, the worker session falls back to deriving work_type itself
	// per the legacy WORKER.md §0 bootstrap procedure.
	routerDir       string
	cardInfoFetcher CardInfoFetcher

	// playbooks is the per-process pre-rendered playbook directory. When
	// non-nil it supplies the skeleton prompts (BOOTSTRAP / IDENTITY /
	// WORKER / TOOLS / USER) and any per-work_type entry playbook
	// referenced by EntryPlaybookFilename. When nil, the runner falls
	// back to the //go:embed snapshots in internal/app/prompts (so
	// historic call sites and tests keep working).
	playbooks *prompttmpl.Renderer

	// preparer turns a card classification into a ready-to-use per-card
	// work_dir (mkdir + optional git clone) and runs registered hooks.
	// It replaces the inline os.MkdirAll that used to live in
	// NewWorkerSession and is the single extension point for "do
	// something the moment a worker's work_dir is ready" (e.g.
	// refresh-copilot-setup.ps1, prefetching submodules, ...).
	preparer *WorkDirPreparer

	clientMu sync.Mutex
	client   *copilot.Client

	// dedupMu guards the recent action.id set used to drop redelivered
	// Trello webhook events (Trello retries when the callback takes longer
	// than ~30s to acknowledge).
	dedupMu     sync.Mutex
	dedupSeen   map[string]struct{}
	dedupOrder  []string
	dedupMaxLen int

	// auditMu guards lazy creation of the per-process audit directory
	// that holds the .md copies of every dispatched prompt; the
	// directory is removed by Stop so audit copies do not pile up
	// across process restarts.
	auditMu  sync.Mutex
	auditDir string
}

// NewCopilotRunner builds a runner targeting the given Copilot model. Pass
// an empty string to use DefaultCopilotModel.
func NewCopilotRunner(model string, logger *log.Logger) *CopilotRunner {
	if model == "" {
		model = DefaultCopilotModel
	}
	if logger == nil {
		logger = log.Default()
	}
	r := &CopilotRunner{
		model:       model,
		logger:      logger,
		dedupMaxLen: 256,
		preparer:    NewWorkDirPreparer(logger),
	}
	r.dispatcher = NewDispatcher(logger, r)
	return r
}

// WorkDirPreparer returns the preparer responsible for creating each
// per-card work_dir. Use it to install custom WorkDirHooks (see
// WorkDirHook) before the first worker session is created. Returned
// preparer is safe for concurrent registration.
func (r *CopilotRunner) WorkDirPreparer() *WorkDirPreparer { return r.preparer }

// RegisterWorkDirHook is a convenience wrapper around
// WorkDirPreparer().RegisterHook for callers that don't need direct
// access to the preparer (e.g. main wiring code).
func (r *CopilotRunner) RegisterWorkDirHook(h WorkDirHook) {
	if r.preparer == nil {
		return
	}
	r.preparer.RegisterHook(h)
}

// SessionSpawner abstracts "give me a new ephemeral Copilot session
// configured per the supplied SessionConfig". It exists so WorkDirHooks
// (or any other peripheral component) can spin up a one-shot session
// without holding a *copilot.Client themselves — and so tests can
// substitute a stub spawner without standing up the real SDK client.
type SessionSpawner interface {
	Spawn(ctx context.Context, cfg *copilot.SessionConfig) (*copilot.Session, error)
}

// SessionSpawner returns a spawner backed by the runner's underlying
// Copilot client. The spawner resolves the client lazily on every call,
// so it is safe to obtain (and pass into hook constructors) before
// Start() is invoked. Spawning before Start returns an error.
func (r *CopilotRunner) SessionSpawner() SessionSpawner {
	return runnerSessionSpawner{r: r}
}

type runnerSessionSpawner struct{ r *CopilotRunner }

func (s runnerSessionSpawner) Spawn(ctx context.Context, cfg *copilot.SessionConfig) (*copilot.Session, error) {
	s.r.clientMu.Lock()
	client := s.r.client
	s.r.clientMu.Unlock()
	if client == nil {
		return nil, errors.New("copilot client not started; call Start before spawning a session")
	}
	return client.CreateSession(ctx, cfg)
}

// Model returns the configured Copilot model name.
func (r *CopilotRunner) Model() string { return r.model }

// Dispatcher exposes the underlying dispatcher (chiefly for tests).
func (r *CopilotRunner) Dispatcher() *Dispatcher { return r.dispatcher }

// SetRouterDir configures the directory used to look up per-work_type
// entry playbook files. Pass "" to disable playbook injection (worker
// must self-derive per WORKER.md §0 fallback). Must be called before
// the first NewWorkerSession invocation.
func (r *CopilotRunner) SetRouterDir(dir string) { r.routerDir = dir }

// SetCardInfoFetcher installs the function used at session-creation time
// to obtain the card's first description line (input to ClassifyCard).
// Pass nil to disable auto-classification (worker must self-derive per
// WORKER.md §0 fallback). Must be called before the first
// NewWorkerSession invocation.
func (r *CopilotRunner) SetCardInfoFetcher(f CardInfoFetcher) { r.cardInfoFetcher = f }

// SetPlaybooks installs the pre-rendered playbook directory used to
// build worker system prompts and look up per-work_type entry
// playbooks. Pass nil to fall back to the embedded skeleton prompts
// (used by tests that don't need a real PlaybooksDir). Must be called
// before the first NewWorkerSession invocation.
func (r *CopilotRunner) SetPlaybooks(p *prompttmpl.Renderer) { r.playbooks = p }

// Start initialises the underlying Copilot SDK client. It is safe to call
// multiple times; subsequent calls are no-ops once the client is running.
func (r *CopilotRunner) Start(ctx context.Context) error {
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	if r.client != nil {
		return nil
	}
	r.logger.Printf("event=copilot_client_starting model=%s", r.model)
	c := copilot.NewClient(&copilot.ClientOptions{LogLevel: "error"})
	started := time.Now()
	if err := c.Start(ctx); err != nil {
		r.logger.Printf("event=copilot_client_start_error err=%v", err)
		return fmt.Errorf("start copilot client: %w", err)
	}
	r.client = c
	r.logger.Printf("event=copilot_client_started model=%s duration=%s", r.model, time.Since(started))
	return nil
}

// Stop shuts the dispatcher down (waiting for every per-card worker
// goroutine to finish) and tears down the underlying SDK client. It
// also removes the per-process audit directory created by
// writeAuditCopy; doing it here (rather than in main) keeps the
// runner's lifecycle self-contained.
func (r *CopilotRunner) Stop() error {
	r.dispatcher.Stop()
	r.removeAuditDir()
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	return r.stopLocked()
}

// stopLocked stops the current client. Caller must hold r.clientMu.
func (r *CopilotRunner) stopLocked() error {
	if r.client == nil {
		return nil
	}
	r.logger.Printf("event=copilot_client_stopping")
	err := r.client.Stop()
	r.client = nil
	if err != nil {
		r.logger.Printf("event=copilot_client_stop_error err=%v", err)
	} else {
		r.logger.Printf("event=copilot_client_stopped")
	}
	return err
}

// Handle is the entry point used by the HTTP layer. It de-duplicates
// retried webhook deliveries by action.id and then asks the dispatcher to
// route the event. The returned audit-prompt path is purely informational
// (it is the per-event TASK message that would be sent to the worker had
// the event not been dropped); empty string is returned for dropped or
// duplicate events.
func (r *CopilotRunner) Handle(ctx context.Context, eventID string, rawBody []byte) (string, error) {
	if actionID, ok := nestedString(parseRawBody(rawBody), "action", "id"); ok {
		if r.markActionSeen(actionID) {
			r.logger.Printf("event=duplicate_action_dropped event_id=%s action_id=%s", eventID, actionID)
			return "", nil
		}
	}

	slim, err := slimRawBody(rawBody)
	if err != nil {
		r.logger.Printf("event=prompt_slim_error event_id=%s err=%v", eventID, err)
		return "", err
	}
	r.logger.Printf("event=prompt_slim_done event_id=%s slim_bytes=%d", eventID, len(slim))

	// Always write an audit copy of what the worker would see for a normal
	// dispatch. The dispatcher may end up routing this as a departure or
	// terminate notice (different prompt wording); the audit copy here is
	// a quick reference for "what was the underlying TASK content".
	taskPrompt := assembleEventPrompt(rawBody, slim)
	r.logger.Printf("event=prompt_assembled event_id=%s task_bytes=%d", eventID, len(taskPrompt))
	promptPath, err := r.writeAuditCopy(eventID, taskPrompt)
	if err != nil {
		r.logger.Printf("event=prompt_audit_error event_id=%s err=%v", eventID, err)
		promptPath = ""
	} else {
		r.logger.Printf("event=prompt_audit_written event_id=%s file=%s bytes=%d", eventID, promptPath, len(taskPrompt))
	}

	if err := r.dispatcher.Dispatch(ctx, eventID, rawBody); err != nil {
		r.logger.Printf("event=dispatch_error event_id=%s err=%v", eventID, err)
		return promptPath, fmt.Errorf("dispatch: %w", err)
	}
	return promptPath, nil
}

// writeAuditCopy persists the per-event task prompt to a temp file so
// operators can inspect what the worker would have seen.
//
// The file lives under r.auditDir, which is a per-process directory
// created on first use and removed by Stop. Without a dedicated dir the
// per-event audit copies would accumulate under the OS-wide temp dir
// and never get cleaned up; with one, a single os.RemoveAll on shutdown
// reclaims everything at once and makes the audit lifecycle explicit.
func (r *CopilotRunner) writeAuditCopy(eventID, content string) (string, error) {
	dir, err := r.ensureAuditDir()
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "trello-prompt-*.md")
	if err != nil {
		return "", fmt.Errorf("create temp prompt file: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp prompt file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("close temp prompt file: %w", err)
	}
	abs, err := filepath.Abs(tmp.Name())
	if err != nil {
		abs = tmp.Name()
	}
	_ = eventID
	return abs, nil
}

// ensureAuditDir lazily creates the per-process directory under
// r.tmpDir (or the OS temp dir when r.tmpDir is empty) used to hold
// audit copies of dispatched prompts. Subsequent calls return the
// already-created path.
func (r *CopilotRunner) ensureAuditDir() (string, error) {
	r.auditMu.Lock()
	defer r.auditMu.Unlock()
	if r.auditDir != "" {
		return r.auditDir, nil
	}
	dir, err := os.MkdirTemp(r.tmpDir, "openclaw-audit-*")
	if err != nil {
		return "", fmt.Errorf("create audit dir: %w", err)
	}
	r.auditDir = dir
	r.logger.Printf("event=audit_dir_created path=%s", dir)
	return dir, nil
}

// removeAuditDir best-effort tears down the audit directory. Called
// from Stop. Errors are logged but never returned — Stop must succeed
// even if the OS refuses to delete the directory.
func (r *CopilotRunner) removeAuditDir() {
	r.auditMu.Lock()
	dir := r.auditDir
	r.auditDir = ""
	r.auditMu.Unlock()
	if dir == "" {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		r.logger.Printf("event=audit_dir_cleanup_failed path=%s err=%v", dir, err)
		return
	}
	r.logger.Printf("event=audit_dir_cleaned path=%s", dir)
}

// NewWorkerSession implements SessionFactory: it creates a brand-new
// Copilot session pre-seeded with the worker system prompt and per-card
// metadata. The dispatcher invokes this lazily on the first message for a
// given card.
func (r *CopilotRunner) NewWorkerSession(ctx context.Context, cardID string, tracker *ActivityTracker) (WorkerSession, error) {
	r.clientMu.Lock()
	client := r.client
	r.clientMu.Unlock()
	if client == nil {
		return nil, errors.New("copilot client not started; call Start before dispatching")
	}

	bs := r.classifyForWorker(ctx, cardID)
	if tracker != nil {
		tracker.SetClassification(bs.classification)
	}
	systemPrompt := assembleWorkerSystemPrompt(cardID, bs, r.playbooks)
	r.logger.Printf("event=worker_session_create_attempt card_id=%s model=%s system_bytes=%d",
		cardID, r.model, len(systemPrompt))

	// Anchor every tool call (view/grep/glob/exec relative paths) to the
	// per-card work_dir so the session cannot accidentally roam into the
	// gateway repo (which is the parent process CWD on most operator
	// machines). The preparer creates the directory eagerly (the SDK
	// rejects a non-existent WorkingDirectory), clones the GitHub repo
	// when one is attached to the card, and fans out to every registered
	// WorkDirHook (e.g. refresh-copilot-setup.ps1).
	info, err := r.preparer.Prepare(ctx, cardID, bs.classification)
	if err != nil {
		return nil, err
	}
	workDir := info.WorkDir
	if tracker != nil {
		tracker.SetWorkDir(workDir)
	}

	cfg := &copilot.SessionConfig{
		Model:               r.model,
		WorkingDirectory:    workDir,
		OnPermissionRequest: approveAllUserIntent, // see permissions.go
		// Hooks layer also gates tool calls (PreToolUse) — return "allow"
		// so it can't second-guess what the permission handler approved.
		Hooks: &copilot.SessionHooks{
			OnPreToolUse: func(_ copilot.PreToolUseHookInput, _ copilot.HookInvocation) (*copilot.PreToolUseHookOutput, error) {
				return &copilot.PreToolUseHookOutput{PermissionDecision: "allow"}, nil
			},
		},
		SystemMessage: &copilot.SystemMessageConfig{
			Mode:    "append",
			Content: systemPrompt,
		},
	}

	start := time.Now()
	session, err := client.CreateSession(ctx, cfg)
	if err != nil {
		r.logger.Printf("event=worker_session_create_failed card_id=%s err=%v", cardID, err)
		return nil, fmt.Errorf("create worker session for card %s: %w", cardID, err)
	}
	r.logger.Printf("event=worker_session_workdir_set card_id=%s work_dir=%s", cardID, workDir)
	r.logger.Printf("event=worker_session_created_ok card_id=%s create_duration=%s",
		cardID, time.Since(start))

	return &copilotWorkerSession{session: session, logger: r.logger, cardID: cardID, tracker: tracker}, nil
}

// markActionSeen records the given Trello action.id and returns true if it
// was already seen. The set is bounded to dedupMaxLen entries; the oldest
// id is evicted FIFO when the set is full.
func (r *CopilotRunner) markActionSeen(actionID string) bool {
	if actionID == "" {
		return false
	}
	r.dedupMu.Lock()
	defer r.dedupMu.Unlock()
	if r.dedupSeen == nil {
		r.dedupSeen = make(map[string]struct{}, r.dedupMaxLen)
	}
	if _, ok := r.dedupSeen[actionID]; ok {
		return true
	}
	r.dedupSeen[actionID] = struct{}{}
	r.dedupOrder = append(r.dedupOrder, actionID)
	if r.dedupMaxLen > 0 && len(r.dedupOrder) > r.dedupMaxLen {
		// Use copy + truncate instead of `r.dedupOrder = r.dedupOrder[1:]`.
		// The naive re-slice keeps the underlying array growing
		// unboundedly: every append after a re-slice reuses the original
		// backing array up to its capacity, then doubles. copy() shifts
		// the live elements down so cap stays ~= dedupMaxLen and the
		// evicted slot is overwritten on the next append.
		evict := r.dedupOrder[0]
		copy(r.dedupOrder, r.dedupOrder[1:])
		r.dedupOrder = r.dedupOrder[:len(r.dedupOrder)-1]
		delete(r.dedupSeen, evict)
	}
	return false
}

// parseRawBody best-effort parses the Trello webhook JSON. Returns an
// empty map on parse error so callers can pass it directly to nestedString
// without nil-handling.
func parseRawBody(rawBody []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// classifyForWorker is invoked at session creation to compute the
// per-card classification + entry-playbook content that gets injected
// into the worker's system prompt. It returns a zero-value bootstrap on
// any failure so the worker can fall back to self-classification per
// WORKER.md §0; non-fatal errors are logged.
func (r *CopilotRunner) classifyForWorker(ctx context.Context, cardID string) workerBootstrap {
	bs := workerBootstrap{cardID: cardID, routerDir: r.routerDir}
	if r.cardInfoFetcher == nil {
		r.logger.Printf("event=worker_bootstrap_skip card_id=%s reason=no_fetcher", cardID)
		return bs
	}
	// Bound the classification call so a hung Trello API can't stall
	// session creation indefinitely.
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	firstLine, err := r.cardInfoFetcher(fetchCtx, cardID)
	if err != nil {
		r.logger.Printf("event=worker_bootstrap_fetch_failed card_id=%s err=%v", cardID, err)
		return bs
	}
	bs.firstLine = firstLine
	bs.classification = ClassifyCard(firstLine)
	r.logger.Printf("event=worker_bootstrap_classified card_id=%s work_type=%s kind=%s owner=%s repo=%s number=%s text_bytes=%d",
		cardID, bs.classification.WorkType, bs.classification.Kind,
		bs.classification.Owner, bs.classification.Repo, bs.classification.Number, len(firstLine))
	if bs.classification.Owner == "" || bs.classification.Repo == "" {
		// Help operators figure out why we couldn't extract a GitHub
		// owner/repo/number — usually because the script returned an
		// unexpected JSON shape or the card body has no GitHub URL.
		r.logger.Printf("event=worker_bootstrap_no_github_url card_id=%s text_preview=%q",
			cardID, preview(firstLine, 400))
	}
	if r.routerDir == "" {
		r.logger.Printf("event=worker_bootstrap_no_router card_id=%s", cardID)
		return bs
	}
	playbook := EntryPlaybookFilename(bs.classification)
	if playbook == "" {
		r.logger.Printf("event=worker_bootstrap_no_playbook card_id=%s work_type=%s kind=%s",
			cardID, bs.classification.WorkType, bs.classification.Kind)
		return bs
	}
	bs.playbookFilename = playbook

	// Prefer the pre-rendered copy in the playbooks temp dir (where any
	// `{{<basename>}}` cross-references have already been substituted to
	// absolute paths). Fall back to the legacy <router-dir>/<playbook>
	// path so operators that run without --playbooks-dir wired up still
	// get an entry playbook (just without template substitution).
	if r.playbooks != nil {
		if path, ok := r.playbooks.Path(playbook); ok {
			content, rerr := r.playbooks.Read(playbook)
			if rerr != nil {
				r.logger.Printf("event=worker_bootstrap_playbook_read_failed card_id=%s path=%s err=%v",
					cardID, path, rerr)
				return bs
			}
			bs.playbookPath = path
			bs.playbookContent = content
			r.logger.Printf("event=worker_bootstrap_ready card_id=%s work_type=%s kind=%s playbook=%s playbook_bytes=%d source=playbooks_tempdir",
				cardID, bs.classification.WorkType, bs.classification.Kind, playbook, len(content))
			return bs
		}
	}

	playbookPath := filepath.Join(r.routerDir, playbook)
	content, rerr := os.ReadFile(playbookPath)
	if rerr != nil {
		r.logger.Printf("event=worker_bootstrap_playbook_read_failed card_id=%s path=%s err=%v",
			cardID, playbookPath, rerr)
		return bs
	}
	bs.playbookPath = playbookPath
	bs.playbookContent = string(content)
	r.logger.Printf("event=worker_bootstrap_ready card_id=%s work_type=%s kind=%s playbook=%s playbook_bytes=%d source=router_dir",
		cardID, bs.classification.WorkType, bs.classification.Kind, playbook, len(content))
	return bs
}

// workerBootstrap is the per-card classification + entry-playbook
// payload assembled at session-creation time and rendered into the
// CARD CONTEXT section of the worker's system prompt.
type workerBootstrap struct {
	cardID           string
	routerDir        string
	firstLine        string
	classification   CardClassification
	playbookFilename string
	playbookPath     string
	playbookContent  string
}

// assembleWorkerSystemPrompt builds the per-card worker system prompt:
// the rendered BOOTSTRAP / IDENTITY / WORKER / TOOLS / USER sections
// (with per-card metadata appended at the very end so it overrides any
// generic guidance in the base files). When playbooks is non-nil the
// section bodies come from the per-process pre-rendered temp directory
// (so any `{{<basename>}}` cross-references have already been
// substituted to absolute paths); otherwise the //go:embed snapshots in
// internal/app/prompts are used unchanged.
func assembleWorkerSystemPrompt(cardID string, bs workerBootstrap, playbooks *prompttmpl.Renderer) string {
	bootstrap, identity, worker, tools, user, override := loadSkeletonPrompts(playbooks)
	var b strings.Builder
	if override != "" {
		fmt.Fprintf(&b, "<!-- WORKER.md overridden from %s -->\n", override)
	}
	writeSection(&b, "BOOTSTRAP", bootstrap)
	writeSection(&b, "IDENTITY", identity)
	writeSection(&b, "WORKER", worker)
	writeSection(&b, "TOOLS", tools)
	writeSection(&b, "USER", user)
	writeSection(&b, "CARD CONTEXT", buildCardContext(bs))
	return b.String()
}

// loadSkeletonPrompts returns the five skeleton prompt bodies in
// declaration order, plus a non-empty override path when WORKER.md was
// supplied by the user (for the audit comment at the top of the
// rendered system prompt).
func loadSkeletonPrompts(playbooks *prompttmpl.Renderer) (bootstrap, identity, worker, tools, user, override string) {
	if playbooks == nil {
		w, src := prompts.ResolveWorker()
		return prompts.Bootstrap, prompts.Identity, w, prompts.Tools, prompts.User, src
	}
	bootstrap = readSkeleton(playbooks, "BOOTSTRAP.md", prompts.Bootstrap)
	identity = readSkeleton(playbooks, "IDENTITY.md", prompts.Identity)
	worker = readSkeleton(playbooks, "WORKER.md", prompts.EmbeddedWorker())
	tools = readSkeleton(playbooks, "TOOLS.md", prompts.Tools)
	user = readSkeleton(playbooks, "USER.md", prompts.User)
	if path, ok := playbooks.Path("WORKER.md"); ok {
		override = path
	}
	return
}

// readSkeleton fetches one rendered skeleton from the playbooks dir and
// falls back to the supplied embedded copy on any I/O error so a
// transient read failure can never produce a half-empty system prompt.
func readSkeleton(playbooks *prompttmpl.Renderer, name, fallback string) string {
	if playbooks == nil {
		return fallback
	}
	body, err := playbooks.Read(name)
	if err != nil {
		return fallback
	}
	return body
}

func buildCardContext(bs workerBootstrap) string {
	var b strings.Builder
	b.WriteString("You have been spawned by the Trello webhook gateway as the dedicated worker ")
	b.WriteString("for the following card. This metadata is authoritative — do not infer card_id, ")
	b.WriteString("work_type, or work_dir from any other source. The gateway has already prepared ")
	b.WriteString("work_dir for you (mkdir + `git clone --depth 1` when a github_repo is attached) ")
	b.WriteString("and fired every registered work_dir hook before this session was created — do ")
	b.WriteString("NOT re-run `git clone` or recreate the directory yourself.\n\n")
	b.WriteString("- card_id: ")
	b.WriteString(bs.cardID)
	b.WriteString("\n- work_dir: C:\\project\\")
	b.WriteString(bs.cardID)
	b.WriteString("\n")

	if bs.classification.WorkType != "" {
		fmt.Fprintf(&b, "- work_type: %s\n", bs.classification.WorkType)
	}
	if bs.classification.Kind != "" {
		fmt.Fprintf(&b, "- kind: %s\n", bs.classification.Kind)
	}
	if bs.classification.Owner != "" && bs.classification.Repo != "" {
		fmt.Fprintf(&b, "- github_repo: %s/%s\n", bs.classification.Owner, bs.classification.Repo)
	}
	if bs.classification.Number != "" {
		fmt.Fprintf(&b, "- github_number: %s\n", bs.classification.Number)
	}
	if bs.classification.URL != "" {
		fmt.Fprintf(&b, "- github_url: %s\n", bs.classification.URL)
	}
	if bs.playbookFilename != "" {
		fmt.Fprintf(&b, "- entry_playbook: %s\n", bs.playbookPath)
	}
	b.WriteString("\n")

	if bs.playbookContent != "" {
		b.WriteString("The gateway has already classified your card and inlined the entry playbook ")
		b.WriteString("below — treat it as authoritative system-prompt-grade guidance and do not ")
		b.WriteString("re-derive `work_type` yourself. On your first turn just `git clone --depth 1` ")
		b.WriteString("the repo into work_dir (skip if already present), then proceed straight into ")
		b.WriteString("the workflow defined by the inlined playbook.\n\n")
		fmt.Fprintf(&b, "## ENTRY PLAYBOOK — %s\n\n", bs.playbookFilename)
		b.WriteString(bs.playbookContent)
		if !strings.HasSuffix(bs.playbookContent, "\n") {
			b.WriteString("\n")
		}
	} else {
		// Fallback path: gateway could not classify the card or no
		// playbook is registered for this work_type. The worker must
		// follow the legacy WORKER.md §0 self-bootstrap procedure.
		b.WriteString("The gateway could not pre-classify this card, or no entry playbook is ")
		b.WriteString("registered for its work_type. Fall back to the WORKER.md §0 self-bootstrap ")
		b.WriteString("procedure: run `trello-get-card-info.ps1 -CardId ")
		b.WriteString(bs.cardID)
		b.WriteString("`, derive work_type from the firstLine, and `view` the matching entry ")
		b.WriteString("file under the workspace-trello-router directory.\n")
	}
	return b.String()
}

// assembleEventPrompt builds the per-event task message: only a TASK
// section describing the Trello event. The system prompt is intentionally
// omitted because it is delivered once via the session's
// SystemMessageConfig.
func assembleEventPrompt(rawBody, slimBody []byte) string {
	message := BuildPromptSummary(rawBody)

	var b strings.Builder
	b.WriteString("# TASK\n\n")
	b.WriteString("A Trello webhook event has been received for your card. ")
	b.WriteString("Re-derive the human-expected task per your worker contract and continue ")
	b.WriteString("(or transition cleanly if expectations changed).\n\n")
	b.WriteString("## Human-readable summary\n\n")
	b.WriteString(message)
	b.WriteString("\n\n## Slimmed event payload (JSON)\n\n```json\n")
	b.Write(slimBody)
	b.WriteString("\n```\n")
	return b.String()
}

func writeSection(b *strings.Builder, title, body string) {
	if body == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	fmt.Fprintf(b, "# %s\n\n%s", title, body)
}
