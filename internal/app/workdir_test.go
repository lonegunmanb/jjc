package app

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// silentLogger discards everything; tests don't care about log noise.
func silentLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// stubGitRunner records each invocation and returns canned outcomes per
// call. The "create .git" side effect is faked by mkdir-ing the .git dir
// inside the target work dir on a successful clone, mirroring real git.
type stubGitRunner struct {
	mu       sync.Mutex
	calls    [][]string
	err      error
	skipMark bool // when true, do NOT create .git on success
}

func (s *stubGitRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	s.mu.Lock()
	s.calls = append(s.calls, append([]string(nil), args...))
	s.mu.Unlock()
	if s.err != nil {
		return []byte("git stub failure: " + s.err.Error()), s.err
	}
	if !s.skipMark && len(args) >= 4 && args[0] == "clone" {
		// args: clone --depth 1 <url> <work_dir>
		target := args[len(args)-1]
		if mkErr := os.MkdirAll(filepath.Join(target, ".git"), 0o755); mkErr != nil {
			return nil, mkErr
		}
	}
	return []byte("stubbed clone ok"), nil
}

func (s *stubGitRunner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func newPreparerForTest(t *testing.T) (*WorkDirPreparer, *stubGitRunner) {
	t.Helper()
	p := NewWorkDirPreparer(silentLogger())
	p.SetBaseDir(t.TempDir())
	stub := &stubGitRunner{}
	p.SetGitRunner(stub.run)
	return p, stub
}

func TestPrepareCreatesDirectoryWithoutGitHubRepo(t *testing.T) {
	p, git := newPreparerForTest(t)

	info, err := p.Prepare(context.Background(), "card-generic", CardClassification{
		WorkType: WorkTypeGeneric,
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !info.CreatedDir {
		t.Fatal("expected CreatedDir=true on first prepare")
	}
	if info.Cloned || info.CloneSkippedExisting {
		t.Fatalf("clone-related flags should be false for generic card: %+v", info)
	}
	if got := git.callCount(); got != 0 {
		t.Fatalf("expected zero git calls for generic card, got %d", got)
	}
	if !dirExists(info.WorkDir) {
		t.Fatalf("work_dir %q was not created", info.WorkDir)
	}
}

func TestPrepareClonesWhenGitHubRepoPresent(t *testing.T) {
	p, git := newPreparerForTest(t)
	c := CardClassification{
		WorkType: WorkTypeProviderAzureRM,
		GitHub: GitHubRef{
			ItemKind: GitHubItemKindIssue,
			Owner:    "hashicorp",
			Repo:     "terraform-provider-azurerm",
			Number:   "32258",
			URL:      "https://github.com/hashicorp/terraform-provider-azurerm/issues/32258",
		},
	}

	info, err := p.Prepare(context.Background(), "card-1", c)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !info.Cloned {
		t.Fatalf("expected Cloned=true, got %+v", info)
	}
	if info.CloneSkippedExisting {
		t.Fatal("CloneSkippedExisting must be false on a fresh clone")
	}
	if info.CloneError != nil {
		t.Fatalf("unexpected CloneError: %v", info.CloneError)
	}
	if got := git.callCount(); got != 1 {
		t.Fatalf("expected exactly one git invocation, got %d", got)
	}
	args := git.calls[0]
	if len(args) < 4 || args[0] != "clone" || args[1] != "--depth" || args[2] != "1" {
		t.Fatalf("unexpected git args: %v", args)
	}
	if !strings.HasPrefix(args[3], "https://github.com/hashicorp/terraform-provider-azurerm") {
		t.Fatalf("clone URL not derived from classification: %q", args[3])
	}
	if args[len(args)-1] != info.WorkDir {
		t.Fatalf("clone target should be work_dir %q, got %q", info.WorkDir, args[len(args)-1])
	}
}

func TestPrepareSkipsCloneWhenGitDirExists(t *testing.T) {
	p, git := newPreparerForTest(t)
	c := CardClassification{
		WorkType: WorkTypeProviderAzureRM,
		GitHub: GitHubRef{
			ItemKind: GitHubItemKindIssue,
			Owner:    "hashicorp",
			Repo:     "terraform-provider-azurerm",
			Number:   "1",
			URL:      "https://github.com/hashicorp/terraform-provider-azurerm/issues/1",
		},
	}

	if _, err := p.Prepare(context.Background(), "card-2", c); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if got := git.callCount(); got != 1 {
		t.Fatalf("first prepare should clone, got %d calls", got)
	}

	info2, err := p.Prepare(context.Background(), "card-2", c)
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if info2.Cloned {
		t.Fatal("second prepare must not re-clone")
	}
	if !info2.CloneSkippedExisting {
		t.Fatalf("second prepare should mark CloneSkippedExisting, got %+v", info2)
	}
	if info2.CreatedDir {
		t.Fatal("second prepare should observe pre-existing dir")
	}
	if got := git.callCount(); got != 1 {
		t.Fatalf("git must not be invoked again, got %d calls", got)
	}
}

func TestPrepareCloneFailureIsRecordedNotReturned(t *testing.T) {
	p, git := newPreparerForTest(t)
	git.err = errors.New("network down")
	c := CardClassification{
		WorkType: WorkTypeProviderAzureRM,
		GitHub: GitHubRef{
			ItemKind: GitHubItemKindIssue,
			Owner:    "hashicorp",
			Repo:     "terraform-provider-azurerm",
			Number:   "9",
		},
	}

	info, err := p.Prepare(context.Background(), "card-fail", c)
	if err != nil {
		t.Fatalf("prepare must not surface clone errors as fatal: %v", err)
	}
	if info.CloneError == nil {
		t.Fatal("expected CloneError to be populated")
	}
	if info.Cloned {
		t.Fatal("Cloned must be false when clone failed")
	}
	if !dirExists(info.WorkDir) {
		t.Fatal("work_dir must still exist even when clone failed")
	}
}

func TestHooksRunInOrderWithFullInfo(t *testing.T) {
	p, _ := newPreparerForTest(t)

	var (
		mu      sync.Mutex
		order   []int
		seen    []WorkDirInfo
	)
	for i := 0; i < 3; i++ {
		idx := i
		p.RegisterHook(func(_ context.Context, info WorkDirInfo) error {
			mu.Lock()
			order = append(order, idx)
			seen = append(seen, info)
			mu.Unlock()
			return nil
		})
	}

	c := CardClassification{
		WorkType: WorkTypeProviderAzureRM,
		GitHub: GitHubRef{
			ItemKind: GitHubItemKindIssue,
			Owner:    "hashicorp",
			Repo:     "terraform-provider-azurerm",
			Number:   "42",
			URL:      "https://github.com/hashicorp/terraform-provider-azurerm/issues/42",
		},
	}

	info, err := p.Prepare(context.Background(), "card-hooks", c)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("expected 3 hook invocations, got %d", len(order))
	}
	for i, got := range order {
		if got != i {
			t.Fatalf("hook ordering broken at %d: %v", i, order)
		}
	}
	for i, got := range seen {
		if got.CardID != "card-hooks" {
			t.Errorf("hook %d got wrong card id %q", i, got.CardID)
		}
		if got.WorkDir != info.WorkDir {
			t.Errorf("hook %d got wrong work_dir %q vs %q", i, got.WorkDir, info.WorkDir)
		}
		if got.Classification.GitHub.Number != "42" {
			t.Errorf("hook %d missing classification: %+v", i, got.Classification)
		}
		if !got.Cloned {
			t.Errorf("hook %d should observe Cloned=true", i)
		}
	}
}

func TestHookErrorDoesNotStopChain(t *testing.T) {
	p, _ := newPreparerForTest(t)

	var (
		mu     sync.Mutex
		called []string
	)
	p.RegisterHook(func(_ context.Context, _ WorkDirInfo) error {
		mu.Lock()
		called = append(called, "first")
		mu.Unlock()
		return errors.New("boom")
	})
	p.RegisterHook(func(_ context.Context, _ WorkDirInfo) error {
		mu.Lock()
		called = append(called, "second")
		mu.Unlock()
		return nil
	})

	if _, err := p.Prepare(context.Background(), "card-err", CardClassification{}); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(called) != 2 || called[0] != "first" || called[1] != "second" {
		t.Fatalf("hook chain did not continue past failure: %v", called)
	}
}

func TestHookPanicIsContained(t *testing.T) {
	p, _ := newPreparerForTest(t)
	var ran bool
	p.RegisterHook(func(_ context.Context, _ WorkDirInfo) error {
		panic("hook went bad")
	})
	p.RegisterHook(func(_ context.Context, _ WorkDirInfo) error {
		ran = true
		return nil
	})

	if _, err := p.Prepare(context.Background(), "card-panic", CardClassification{}); err != nil {
		t.Fatalf("prepare returned err on hook panic: %v", err)
	}
	if !ran {
		t.Fatal("subsequent hook must still run after panicking peer")
	}
}

func TestPrepareEmptyCardIDIsRejected(t *testing.T) {
	p, _ := newPreparerForTest(t)
	if _, err := p.Prepare(context.Background(), "", CardClassification{}); err == nil {
		t.Fatal("expected error for empty card id")
	}
}

func TestRegisterWorkDirHookOnRunner(t *testing.T) {
	r := NewCopilotRunner("m", silentLogger())
	if r.WorkDirPreparer() == nil {
		t.Fatal("runner should expose a non-nil preparer")
	}
	r.WorkDirPreparer().SetBaseDir(t.TempDir())
	r.WorkDirPreparer().SetGitRunner((&stubGitRunner{}).run)

	var fired bool
	r.RegisterWorkDirHook(func(_ context.Context, info WorkDirInfo) error {
		fired = true
		if info.CardID != "card-x" {
			t.Errorf("hook saw wrong card id %q", info.CardID)
		}
		return nil
	})

	if _, err := r.preparer.Prepare(context.Background(), "card-x", CardClassification{}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !fired {
		t.Fatal("hook registered via runner did not fire")
	}
}

// keep imports tidy in case future edits drop one
var _ = time.Second
