package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lonegunmanb/trello-copilot/internal/app/aiassistedrefresh"
)

func writeGoMod(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}

func TestIsAzureRMProviderModule(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, dir string)
		want     bool
		wantsErr bool
	}{
		{
			name:  "no go.mod file means false (and no error)",
			setup: func(*testing.T, string) {},
			want:  false,
		},
		{
			name: "exact match on first non-empty line",
			setup: func(t *testing.T, dir string) {
				writeGoMod(t, dir, "module github.com/hashicorp/terraform-provider-azurerm\n\ngo 1.22\n")
			},
			want: true,
		},
		{
			name: "leading whitespace and blank lines are tolerated",
			setup: func(t *testing.T, dir string) {
				writeGoMod(t, dir, "\n\n   module github.com/hashicorp/terraform-provider-azurerm   \n\ngo 1.22\n")
			},
			want: true,
		},
		{
			name: "different module path means false",
			setup: func(t *testing.T, dir string) {
				writeGoMod(t, dir, "module github.com/hashicorp/terraform-provider-aws\n")
			},
			want: false,
		},
		{
			name: "comment as first line means false",
			setup: func(t *testing.T, dir string) {
				writeGoMod(t, dir, "// header\nmodule github.com/hashicorp/terraform-provider-azurerm\n")
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			got, err := isAzureRMProviderModule(dir)
			if tc.wantsErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantsErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// refresherRecorder is the test stub for the Refresher interface — it
// captures every Options the hook passes through and lets tests programme
// an error reply. It is concurrency-safe so it can be reused across
// goroutines if a future test needs that.
type refresherRecorder struct {
	mu    sync.Mutex
	calls []aiassistedrefresh.Options
	err   error
}

func (r *refresherRecorder) Refresh(_ context.Context, opts aiassistedrefresh.Options) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, opts)
	return r.err
}

func (r *refresherRecorder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *refresherRecorder) latest() aiassistedrefresh.Options {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

func TestNewAzureRMRefreshHookValidatesOptions(t *testing.T) {
	if _, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{}); err == nil {
		t.Fatal("expected error when Refresher is nil")
	}
}

func TestAzureRMRefreshHookSkipsWhenGoModMissing(t *testing.T) {
	rec := &refresherRecorder{}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		Refresher: rec,
		Logger:    silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewAzureRMRefreshHook: %v", err)
	}

	dir := t.TempDir()
	if err := hook(context.Background(), WorkDirInfo{CardID: "card", WorkDir: dir}); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if got := rec.callCount(); got != 0 {
		t.Fatalf("hook should not refresh when go.mod is missing, got %d calls", got)
	}
}

func TestAzureRMRefreshHookSkipsForOtherModules(t *testing.T) {
	rec := &refresherRecorder{}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		Refresher: rec,
		Logger:    silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewAzureRMRefreshHook: %v", err)
	}

	dir := t.TempDir()
	writeGoMod(t, dir, "module github.com/hashicorp/terraform-provider-aws\n")

	if err := hook(context.Background(), WorkDirInfo{CardID: "card", WorkDir: dir}); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if got := rec.callCount(); got != 0 {
		t.Fatalf("hook should not refresh for non-azurerm provider, got %d calls", got)
	}
}

func TestAzureRMRefreshHookInvokesRefresherWithExpectedOptions(t *testing.T) {
	rec := &refresherRecorder{}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		Refresher: rec,
		Logger:    silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewAzureRMRefreshHook: %v", err)
	}

	dir := t.TempDir()
	writeGoMod(t, dir, "module github.com/hashicorp/terraform-provider-azurerm\n\ngo 1.22\n")

	info := WorkDirInfo{
		CardID:  "69fbf8b2fa478054c540d2d3",
		WorkDir: dir,
		Classification: CardClassification{
			WorkType: WorkTypeProviderAzureRM,
			Kind:     KindIssue,
			Owner:    "hashicorp",
			Repo:     "terraform-provider-azurerm",
			Number:   "32258",
		},
	}
	if err := hook(context.Background(), info); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if got := rec.callCount(); got != 1 {
		t.Fatalf("hook should invoke Refresher exactly once for azurerm provider, got %d", got)
	}

	got := rec.latest()
	if got.RepoDirectory != dir {
		t.Errorf("RepoDirectory: got %q want %q", got.RepoDirectory, dir)
	}
	if got.Issue != "32258" {
		t.Errorf("Issue: got %q want %q", got.Issue, "32258")
	}
	if got.Clean {
		t.Errorf("Clean must default to false; got %v", got.Clean)
	}
}

func TestAzureRMRefreshHookFallsBackToFetcherWhenClassificationMissing(t *testing.T) {
	rec := &refresherRecorder{}
	fetcher := func(_ context.Context, _ string) (string, error) {
		return "https://github.com/hashicorp/terraform-provider-azurerm/issues/4242", nil
	}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		Refresher:       rec,
		CardInfoFetcher: fetcher,
		Logger:          silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewAzureRMRefreshHook: %v", err)
	}
	dir := t.TempDir()
	writeGoMod(t, dir, "module github.com/hashicorp/terraform-provider-azurerm\n")

	info := WorkDirInfo{CardID: "card-x", WorkDir: dir} // no Classification
	if err := hook(context.Background(), info); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if rec.callCount() != 1 {
		t.Fatalf("expected 1 refresh after fetcher fallback, got %d", rec.callCount())
	}
	if got := rec.latest(); got.Issue != "4242" {
		t.Errorf("Issue: got %q want %q", got.Issue, "4242")
	}
}

func TestAzureRMRefreshHookSkipsWhenIssueNumberCannotBeResolved(t *testing.T) {
	rec := &refresherRecorder{}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		Refresher: rec,
		Logger:    silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewAzureRMRefreshHook: %v", err)
	}
	dir := t.TempDir()
	writeGoMod(t, dir, "module github.com/hashicorp/terraform-provider-azurerm\n")
	info := WorkDirInfo{CardID: "card-x", WorkDir: dir} // no Number, no fetcher
	if err := hook(context.Background(), info); err != nil {
		t.Fatalf("hook should swallow unresolved-number, got %v", err)
	}
	if rec.callCount() != 0 {
		t.Fatalf("hook must not refresh when issue number is unknown, got %d calls", rec.callCount())
	}
}

func TestAzureRMRefreshHookPropagatesRefresherError(t *testing.T) {
	rec := &refresherRecorder{err: errors.New("boom")}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		Refresher: rec,
		Logger:    silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewAzureRMRefreshHook: %v", err)
	}

	dir := t.TempDir()
	writeGoMod(t, dir, "module github.com/hashicorp/terraform-provider-azurerm\n")

	info := WorkDirInfo{
		CardID:  "card",
		WorkDir: dir,
		// Pre-populate Classification.Number so the hook proceeds past
		// the issue-number resolver and actually invokes the Refresher.
		Classification: CardClassification{Number: "9"},
	}
	err = hook(context.Background(), info)
	if err == nil {
		t.Fatal("expected error to bubble up from Refresher")
	}
	// The hook wraps the underlying error with fmt.Errorf("…: %w", err),
	// so errors.Is must still see the recorder's sentinel.
	if !errors.Is(err, rec.err) {
		t.Fatalf("error chain does not include underlying cause: %v", err)
	}
}
