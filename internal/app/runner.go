package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/lonegunmanb/jjc/internal/app/kanban"
	"github.com/lonegunmanb/jjc/internal/app/prompts"
	"github.com/lonegunmanb/jjc/internal/app/prompttmpl"
	"github.com/lonegunmanb/jjc/internal/app/router"
	"github.com/lonegunmanb/jjc/internal/app/sysevent"
	"github.com/lonegunmanb/jjc/internal/app/trelloclient"
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
	logger     sysevent.Sink
	tmpDir     string
	dispatcher *Dispatcher

	// routerDir is the directory containing the per-work_type entry
	// playbook markdown files. cardInfoFetcher is used at session
	// creation time to derive work_type from the Trello card description.
	// Both are optional: when routerDir is empty or cardInfoFetcher is
	// nil, the worker session falls back to deriving work_type itself
	// per the legacy WORKER.md §0 bootstrap procedure.
	routerDir          string
	cardInfoFetcher    CardInfoFetcher
	cardSignalsFetcher CardSignalsFetcher
	ruleEngine         *router.RuleEngine

	// playbooks is the per-process pre-rendered playbook directory. When
	// non-nil it supplies the skeleton prompts (BOOTSTRAP / IDENTITY /
	// WORKER / TOOLS / USER) and any rule-selected playbook. When nil, the runner falls
	// back to the //go:embed snapshots in internal/app/prompts (so
	// historic call sites and tests keep working).
	playbooks *prompttmpl.Renderer

	// preparer turns a card classification into a ready-to-use per-card
	// work_dir (mkdir + optional git clone) and runs registered hooks.
	// It replaces the inline os.MkdirAll that used to live in
	// NewWorkerSession and is the single extension point for "do
	// something the moment a worker's work_dir is ready" (e.g.
	// aiassistedrefresh.Service, prefetching submodules, ...).
	preparer *WorkDirPreparer

	// trelloClient is the project-local SDK wrapper used to expose
	// `trello_*` tools on every worker session and to back any other
	// in-process Trello traffic the gateway needs (e.g. classifier
	// fallback in the AzureRM refresh hook). Nil is allowed — unit tests
	// that don't need real Trello traffic skip wiring it.
	trelloClient trelloclient.Client

	// kanbanView is the resolved list-name → list-id mapping produced
	// at startup by internal/app/kanban.LoadAndResolve. The runner
	// inlines the per-role IDs and agent-comment prefix list into the
	// per-card CARD CONTEXT so worker prompts can reference
	// `kanban_*_id` directly instead of the legacy `TRELLO_*` env
	// vars. Nil is allowed — unit tests that don't talk to Trello skip
	// the injection.
	kanbanView *kanban.Resolved

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
func NewCopilotRunner(model string, logger sysevent.Sink) *CopilotRunner {
	if model == "" {
		model = DefaultCopilotModel
	}
	if logger == nil {
		logger = sysevent.Default()
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
// to obtain the card's first description line (legacy fallback input).
// Pass nil to disable auto-classification (worker must self-derive per
// WORKER.md §0 fallback). Must be called before the first
// NewWorkerSession invocation.
func (r *CopilotRunner) SetCardInfoFetcher(f CardInfoFetcher) { r.cardInfoFetcher = f }

// SetCardSignalsFetcher installs the structured function used at session
// creation time to obtain the card.* inputs for HCL rule evaluation.
func (r *CopilotRunner) SetCardSignalsFetcher(f CardSignalsFetcher) { r.cardSignalsFetcher = f }

// SetRuleEngine installs the HCL rule engine used to choose rule playbooks.
func (r *CopilotRunner) SetRuleEngine(e *router.RuleEngine) { r.ruleEngine = e }

// SetPlaybooks installs the pre-rendered playbook directory used to
// build worker system prompts and look up per-work_type entry
// playbooks. Pass nil to fall back to the embedded skeleton prompts
// (used by tests that don't need a real PlaybooksDir). Must be called
// before the first NewWorkerSession invocation.
func (r *CopilotRunner) SetPlaybooks(p *prompttmpl.Renderer) { r.playbooks = p }

// SetTrelloClient installs the SDK-backed Trello wrapper used to
// expose `trello_*` tools on every worker session. Passing nil leaves
// worker sessions without those tools — used by unit tests that don't
// need to talk to Trello. Must be called before the first
// NewWorkerSession invocation.
func (r *CopilotRunner) SetTrelloClient(c trelloclient.Client) { r.trelloClient = c }

// SetKanbanView installs the resolved kanban view (see
// internal/app/kanban) so per-card CARD CONTEXT can inline role IDs
// and the agent-comment prefix list. Also propagated to the dispatcher
// so Route consults it for category decisions. Pass nil to disable
// injection (unit tests that skip the Trello bootstrap path). Must be
// called before the first NewWorkerSession invocation.
func (r *CopilotRunner) SetKanbanView(view *kanban.Resolved) {
	r.kanbanView = view
	if r.dispatcher != nil {
		r.dispatcher.SetKanbanView(view)
	}
}

// Start initialises the underlying Copilot SDK client. It is safe to call
// multiple times; subsequent calls are no-ops once the client is running.
func (r *CopilotRunner) Start(ctx context.Context) error {
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	if r.client != nil {
		return nil
	}
	sysevent.Emitf(r.logger, "copilot_client_starting", "model=%s", r.model)
	c := copilot.NewClient(&copilot.ClientOptions{LogLevel: "error"})
	started := time.Now()
	if err := c.Start(ctx); err != nil {
		sysevent.Emitf(r.logger, "copilot_client_start_error", "err=%v", err)
		return fmt.Errorf("start copilot client: %w", err)
	}
	r.client = c
	sysevent.Emitf(r.logger, "copilot_client_started", "model=%s duration=%s", r.model, time.Since(started))
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
	sysevent.Emit(r.logger, "copilot_client_stopping")
	err := r.client.Stop()
	r.client = nil
	if err != nil {
		sysevent.Emitf(r.logger, "copilot_client_stop_error", "err=%v", err)
	} else {
		sysevent.Emit(r.logger, "copilot_client_stopped")
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
			sysevent.Emitf(r.logger, "duplicate_action_dropped", "event_id=%s action_id=%s", eventID, actionID)
			return "", nil
		}
	}

	slim, err := slimRawBody(rawBody)
	if err != nil {
		sysevent.Emitf(r.logger, "prompt_slim_error", "event_id=%s err=%v", eventID, err)
		return "", err
	}
	sysevent.Emitf(r.logger, "prompt_slim_done", "event_id=%s slim_bytes=%d", eventID, len(slim))

	// Always write an audit copy of what the worker would see for a normal
	// dispatch. The dispatcher may end up routing this as a departure or
	// terminate notice (different prompt wording); the audit copy here is
	// a quick reference for "what was the underlying TASK content".
	taskPrompt := assembleEventPrompt(rawBody, slim)
	sysevent.Emitf(r.logger, "prompt_assembled", "event_id=%s task_bytes=%d", eventID, len(taskPrompt))
	promptPath, err := r.writeAuditCopy(eventID, taskPrompt)
	if err != nil {
		sysevent.Emitf(r.logger, "prompt_audit_error", "event_id=%s err=%v", eventID, err)
		promptPath = ""
	} else {
		sysevent.Emitf(r.logger, "prompt_audit_written", "event_id=%s file=%s bytes=%d", eventID, promptPath, len(taskPrompt))
	}

	if err := r.dispatcher.Dispatch(ctx, eventID, rawBody); err != nil {
		sysevent.Emitf(r.logger, "dispatch_error", "event_id=%s err=%v", eventID, err)
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
	sysevent.Emitf(r.logger, "audit_dir_created", "path=%s", dir)
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
		sysevent.Emitf(r.logger, "audit_dir_cleanup_failed", "path=%s err=%v", dir, err)
		return
	}
	sysevent.Emitf(r.logger, "audit_dir_cleaned", "path=%s", dir)
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
	systemPrompt := assembleWorkerSystemPrompt(cardID, bs, r.playbooks, r.kanbanView)
	sysevent.Emitf(r.logger, "worker_session_create_attempt", "card_id=%s model=%s system_bytes=%d",
		cardID, r.model, len(systemPrompt))

	// Anchor every tool call (view/grep/glob/exec relative paths) to the
	// per-card work_dir so the session cannot accidentally roam into the
	// gateway repo (which is the parent process CWD on most operator
	// machines). The preparer creates the directory eagerly (the SDK
	// rejects a non-existent WorkingDirectory), clones the GitHub repo
	// when one is attached to the card, and fans out to every registered
	// WorkDirHook (e.g. the aiassistedrefresh-backed AzureRM hook).
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
		Tools: BuildTrelloTools(r.trelloClient, r.logger),
	}

	start := time.Now()
	session, err := client.CreateSession(ctx, cfg)
	if err != nil {
		sysevent.Emitf(r.logger, "worker_session_create_failed", "card_id=%s err=%v", cardID, err)
		return nil, fmt.Errorf("create worker session for card %s: %w", cardID, err)
	}
	sysevent.Emitf(r.logger, "worker_session_workdir_set", "card_id=%s work_dir=%s", cardID, workDir)
	sysevent.Emitf(r.logger, "worker_session_created_ok", "card_id=%s create_duration=%s",
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
	if r.cardSignalsFetcher == nil && r.cardInfoFetcher == nil {
		sysevent.Emitf(r.logger, "worker_bootstrap_skip", "card_id=%s reason=no_fetcher", cardID)
		return bs
	}
	// Bound the rule-input fetch so a hung Trello API can't stall
	// session creation indefinitely.
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	signals := router.CardSignals{ID: cardID}
	if r.cardSignalsFetcher != nil {
		var err error
		signals, err = r.cardSignalsFetcher(fetchCtx, cardID)
		if err != nil {
			sysevent.Emitf(r.logger, "worker_bootstrap_fetch_failed", "card_id=%s err=%v", cardID, err)
			return bs
		}
		if signals.ID == "" {
			signals.ID = cardID
		}
	} else {
		firstLine, err := r.cardInfoFetcher(fetchCtx, cardID)
		if err != nil {
			sysevent.Emitf(r.logger, "worker_bootstrap_fetch_failed", "card_id=%s err=%v", cardID, err)
			return bs
		}
		signals.FirstLine = firstLine
	}
	bs.signals = signals
	bs.firstLine = signals.FirstLine
	bs.classification = classifyGitHubRef(signals.FirstLine)
	if !bs.classification.GitHub.Present() {
		// Help operators figure out why we couldn't extract a GitHub
		// owner/repo/number — usually because the script returned an
		// unexpected JSON shape or the card body has no GitHub URL.
		sysevent.Emitf(r.logger, "worker_bootstrap_no_github_url", "card_id=%s text_preview=%q",
			cardID, preview(signals.FirstLine, 400))
	}
	if r.ruleEngine == nil {
		sysevent.Emitf(r.logger, "worker_bootstrap_no_rule_engine", "card_id=%s", cardID)
		return bs
	}
	match, ok := r.ruleEngine.Match(signals)
	if !ok {
		return bs
	}
	bs.classification.RuleName = match.RuleName
	bs.ruleName = match.RuleName
	bs.promptNames = append([]string(nil), match.PromptNames...)
	sysevent.Emitf(r.logger, "worker_bootstrap_rule_matched", "card_id=%s rule=%s prompt_count=%d kind=%s owner=%s repo=%s number=%s text_bytes=%d",
		cardID, match.RuleName, len(match.PromptNames), bs.classification.GitHub.ItemKind,
		bs.classification.GitHub.Owner, bs.classification.GitHub.Repo, bs.classification.GitHub.Number, len(signals.FirstLine))
	if len(match.PromptNames) == 0 {
		sysevent.Emitf(r.logger, "worker_bootstrap_no_playbook", "card_id=%s rule=%s", cardID, match.RuleName)
		return bs
	}

	// Prefer the pre-rendered copy in the playbooks temp dir (where any
	// `{{<basename>}}` cross-references have already been substituted to
	// absolute paths). Fall back to the legacy <router-dir>/<playbook>
	// path so operators that run without --playbooks-dir wired up still
	// get an entry playbook (just without template substitution).
	for _, playbook := range match.PromptNames {
		if r.playbooks != nil {
			if path, ok := r.playbooks.Path(playbook); ok {
				content, rerr := r.playbooks.Read(playbook)
				if rerr != nil {
					sysevent.Emitf(r.logger, "worker_bootstrap_playbook_read_failed", "card_id=%s path=%s err=%v",
						cardID, path, rerr)
					continue
				}
				bs.playbooks = append(bs.playbooks, workerPlaybook{Name: playbook, Path: path, Content: content})
				sysevent.Emitf(r.logger, "worker_bootstrap_playbook_ready", "card_id=%s rule=%s playbook=%s playbook_bytes=%d source=playbooks_tempdir",
					cardID, match.RuleName, playbook, len(content))
				continue
			}
		}
		if r.routerDir == "" {
			sysevent.Emitf(r.logger, "worker_bootstrap_playbook_read_failed", "card_id=%s playbook=%s err=%v",
				cardID, playbook, "router dir is empty")
			continue
		}
		playbookPath := filepath.Join(r.routerDir, playbook)
		content, rerr := os.ReadFile(playbookPath)
		if rerr != nil {
			sysevent.Emitf(r.logger, "worker_bootstrap_playbook_read_failed", "card_id=%s path=%s err=%v",
				cardID, playbookPath, rerr)
			continue
		}
		bs.playbooks = append(bs.playbooks, workerPlaybook{Name: playbook, Path: playbookPath, Content: string(content)})
		sysevent.Emitf(r.logger, "worker_bootstrap_playbook_ready", "card_id=%s rule=%s playbook=%s playbook_bytes=%d source=router_dir",
			cardID, match.RuleName, playbook, len(content))
	}
	if len(bs.playbooks) == 0 {
		return bs
	}
	sysevent.Emitf(r.logger, "worker_bootstrap_ready", "card_id=%s rule=%s prompt_count=%d",
		cardID, match.RuleName, len(bs.playbooks))
	return bs
}

// workerBootstrap is the per-card classification + entry-playbook
// payload assembled at session-creation time and rendered into the
// CARD CONTEXT section of the worker's system prompt.
type workerBootstrap struct {
	cardID         string
	routerDir      string
	firstLine      string
	signals        router.CardSignals
	classification CardClassification
	ruleName       string
	promptNames    []string
	playbooks      []workerPlaybook
}

type workerPlaybook struct {
	Name    string
	Path    string
	Content string
}

// assembleWorkerSystemPrompt builds the per-card worker system prompt:
// the rendered BOOTSTRAP / IDENTITY / WORKER / TOOLS / USER sections
// (with per-card metadata appended at the very end so it overrides any
// generic guidance in the base files). When playbooks is non-nil the
// section bodies come from the per-process pre-rendered temp directory
// (so any `{{<basename>}}` cross-references have already been
// substituted to absolute paths); otherwise the //go:embed snapshots in
// internal/app/prompts are used unchanged. When view is non-nil the
// CARD CONTEXT section additionally lists the resolved
// `kanban_<role>_id` values and `kanban_agent_comment_prefixes` so the
// worker can reference list IDs by stable role name without the
// legacy `TRELLO_*` env-var bridge.
func assembleWorkerSystemPrompt(cardID string, bs workerBootstrap, playbooks *prompttmpl.Renderer, view *kanban.Resolved) string {
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
	writeSection(&b, "CARD CONTEXT", buildCardContext(bs, view))
	return b.String()
}

// loadSkeletonPrompts returns the five skeleton prompt bodies in
// declaration order, plus a non-empty override path when WORKER.md was
// supplied by the user (for the audit comment at the top of the
// rendered system prompt).
func loadSkeletonPrompts(playbooks *prompttmpl.Renderer) (bootstrap, identity, worker, tools, user, override string) {
	if playbooks == nil {
		// No --playbooks-dir wired in (only happens in unit tests):
		// fall back to the embedded skeleton snapshots. There is no
		// override path to surface in this branch.
		return prompts.Bootstrap, prompts.Identity, prompts.EmbeddedWorker(), prompts.Tools, prompts.User, ""
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

func buildCardContext(bs workerBootstrap, view *kanban.Resolved) string {
	var b strings.Builder
	b.WriteString("You have been spawned by the Trello webhook gateway as the dedicated worker ")
	b.WriteString("for the following card. This metadata is authoritative — do not infer card_id, ")
	b.WriteString("matched_rule, or work_dir from any other source. The gateway has already prepared ")
	b.WriteString("work_dir for you (mkdir + `git clone --depth 1` when a github_repo is attached) ")
	b.WriteString("and fired every registered work_dir hook before this session was created — do ")
	b.WriteString("NOT re-run `git clone` or recreate the directory yourself.\n\n")
	b.WriteString("- card_id: ")
	b.WriteString(bs.cardID)
	b.WriteString("\n- work_dir: C:\\project\\")
	b.WriteString(bs.cardID)
	b.WriteString("\n")

	if bs.ruleName != "" {
		fmt.Fprintf(&b, "- matched_rule: %s\n", bs.ruleName)
	}
	if bs.classification.GitHub.ItemKind != "" {
		fmt.Fprintf(&b, "- kind: %s\n", bs.classification.GitHub.ItemKind)
	}
	if bs.classification.GitHub.Present() {
		fmt.Fprintf(&b, "- github_repo: %s/%s\n", bs.classification.GitHub.Owner, bs.classification.GitHub.Repo)
	}
	if bs.classification.GitHub.Number != "" {
		fmt.Fprintf(&b, "- github_number: %s\n", bs.classification.GitHub.Number)
	}
	if bs.classification.GitHub.URL != "" {
		fmt.Fprintf(&b, "- github_url: %s\n", bs.classification.GitHub.URL)
	}
	for _, pb := range bs.playbooks {
		fmt.Fprintf(&b, "- rule_playbook: %s\n", pb.Path)
	}

	// Resolved kanban view: one `kanban_<role>_id` line per role plus a
	// formatted `kanban_agent_comment_prefixes` list. WORKER.md §2
	// references these directly; the legacy `TRELLO_*` env-var bridge
	// has been retired (see issue #5).
	if view != nil {
		fmt.Fprintf(&b, "- kanban_board_id: %s\n", view.BoardID)
		fmt.Fprintf(&b, "- kanban_plan_id: %s\n", view.Plan.ID)
		fmt.Fprintf(&b, "- kanban_action_id: %s\n", view.Action.ID)
		fmt.Fprintf(&b, "- kanban_wait_plan_review_id: %s\n", view.Wait.PlanReview.ID)
		fmt.Fprintf(&b, "- kanban_wait_action_review_id: %s\n", view.Wait.ActionReview.ID)
		fmt.Fprintf(&b, "- kanban_wait_generic_id: %s\n", view.Wait.Generic.ID)
		fmt.Fprintf(&b, "- kanban_wait_exception_id: %s\n", view.Wait.Exception.ID)
		fmt.Fprintf(&b, "- kanban_done_id: %s\n", view.Done.ID)
		fmt.Fprintf(&b, "- kanban_agent_comment_prefixes: %s\n", formatAgentPrefixes(view.AgentCommentPrefixes))
	}

	b.WriteString("\n")

	if len(bs.playbooks) > 0 {
		b.WriteString("The gateway has already matched your card against an HCL rule and inlined the rule playbook(s) ")
		b.WriteString("below — treat it as authoritative system-prompt-grade guidance and do not ")
		b.WriteString("re-derive the rule yourself. On your first turn, if the repository is not already ")
		b.WriteString("present in work_dir, `git clone --depth 1` it there, then proceed straight into ")
		b.WriteString("the workflow defined by the inlined playbook(s).\n\n")
		for _, pb := range bs.playbooks {
			fmt.Fprintf(&b, "## RULE PLAYBOOK — %s\n\n", pb.Name)
			b.WriteString(pb.Content)
			if !strings.HasSuffix(pb.Content, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	} else {
		// Fallback path: gateway could not classify the card or no
		// playbook is registered for this rule. The worker must
		// follow the legacy WORKER.md §0 self-bootstrap procedure.
		b.WriteString("The gateway could not pre-classify this card, or no rule playbook is ")
		b.WriteString("registered for its matched rule. Fall back to the WORKER.md §0 self-bootstrap ")
		b.WriteString("procedure: run `trello-get-card-info.ps1 -CardId ")
		b.WriteString(bs.cardID)
		b.WriteString("`, inspect the firstLine, and `view` the matching entry ")
		b.WriteString("file under the workspace-trello-router directory.\n")
	}
	return b.String()
}

// formatAgentPrefixes renders the AgentCommentPrefixes list as a
// JSON-like array of quoted strings so the worker can parse it
// unambiguously (a comma-separated bare list would be hard to read
// when a prefix legitimately contains a comma).
func formatAgentPrefixes(prefixes []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, p := range prefixes {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", p)
	}
	b.WriteByte(']')
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
