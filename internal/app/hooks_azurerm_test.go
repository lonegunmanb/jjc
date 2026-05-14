package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
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

// captureRunSession returns a SessionRunFunc that records every call so
// tests can assert on what would have been spawned.
type capturedRun struct {
	cfg    *copilot.SessionConfig
	prompt string
}

type runSessionRecorder struct {
	mu    sync.Mutex
	calls []capturedRun
	err   error
}

func (r *runSessionRecorder) run() SessionRunFunc {
	return func(ctx context.Context, cfg *copilot.SessionConfig, prompt string) error {
		r.mu.Lock()
		r.calls = append(r.calls, capturedRun{cfg: cfg, prompt: prompt})
		err := r.err
		r.mu.Unlock()
		_ = ctx
		return err
	}
}

func (r *runSessionRecorder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestNewAzureRMRefreshHookValidatesOptions(t *testing.T) {
	if _, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		ScriptPath: "x",
	}); err == nil {
		t.Fatal("expected error when neither Spawner nor RunSession given")
	}
	rec := &runSessionRecorder{}
	if _, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		RunSession: rec.run(),
	}); err == nil {
		t.Fatal("expected error when ScriptPath empty")
	}
}

func TestAzureRMRefreshHookSkipsWhenGoModMissing(t *testing.T) {
	rec := &runSessionRecorder{}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		RunSession: rec.run(),
		ScriptPath: `C:\fake\refresh-copilot-setup.ps1`,
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewAzureRMRefreshHook: %v", err)
	}

	dir := t.TempDir()
	if err := hook(context.Background(), WorkDirInfo{CardID: "card", WorkDir: dir}); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if got := rec.callCount(); got != 0 {
		t.Fatalf("hook should not spawn when go.mod is missing, got %d calls", got)
	}
}

func TestAzureRMRefreshHookSkipsForOtherModules(t *testing.T) {
	rec := &runSessionRecorder{}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		RunSession: rec.run(),
		ScriptPath: `C:\fake\refresh-copilot-setup.ps1`,
		Logger:     silentLogger(),
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
		t.Fatalf("hook should not spawn for non-azurerm provider, got %d calls", got)
	}
}

func TestAzureRMRefreshHookSpawnsWithExpectedConfigAndPrompt(t *testing.T) {
	rec := &runSessionRecorder{}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		RunSession:         rec.run(),
		ScriptPath:         `C:\workspace\scripts\refresh-copilot-setup.ps1`,
		CardInfoScriptPath: `C:\workspace\scripts\trello-get-card-info.ps1`,
		Model:              "stub-model",
		Logger:             silentLogger(),
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
		t.Fatalf("hook should spawn exactly once for azurerm provider, got %d", got)
	}

	call := rec.calls[0]
	if call.cfg == nil {
		t.Fatal("session config must not be nil")
	}
	if call.cfg.Model != "stub-model" {
		t.Errorf("model: got %q want %q", call.cfg.Model, "stub-model")
	}
	if call.cfg.WorkingDirectory != dir {
		t.Errorf("WorkingDirectory: got %q want %q", call.cfg.WorkingDirectory, dir)
	}
	if call.cfg.SystemMessage == nil || !strings.Contains(call.cfg.SystemMessage.Content, "ephemeral Copilot session") {
		t.Errorf("system prompt missing expected ephemeral preface: %+v", call.cfg.SystemMessage)
	}

	// User prompt must reference both scripts and the card id.
	for _, must := range []string{
		"trello-get-card-info.ps1",
		"refresh-copilot-setup.ps1",
		"69fbf8b2fa478054c540d2d3",
		dir,
	} {
		if !strings.Contains(call.prompt, must) {
			t.Errorf("user prompt missing %q. prompt=\n%s", must, call.prompt)
		}
	}
	// Must not invent a hard-coded issue number; LLM derives it.
	if strings.Contains(call.prompt, "-Issue 32258") {
		t.Errorf("prompt must not embed pre-classified issue number; LLM derives from card. prompt=\n%s", call.prompt)
	}
	// Must invoke via pwsh (PowerShell 7+) — refresh-copilot-setup.ps1 relies on
	// $IsWindows which only exists in 6+. `powershell -NoProfile -File ...`
	// (Windows PowerShell 5.1) fails with a strict-mode automatic-variable error.
	// Count actual *invocations* (looks like `<exe> -NoProfile -File <path>`),
	// not mentions inside explanatory prose.
	if strings.Count(call.prompt, "pwsh -NoProfile -File ") < 2 {
		t.Errorf("prompt should issue at least 2 pwsh invocations (card-info + refresh). prompt=\n%s", call.prompt)
	}
	// Allow `powershell` to appear in prose (e.g. fence language tag, warning
	// text), but not as the executable in either fenced invocation. The two
	// invocation lines must start with `pwsh `.
	for _, mustNot := range []string{
		"powershell -NoProfile -File C:",
		"powershell -NoProfile -File <",
	} {
		if strings.Contains(call.prompt, mustNot) {
			t.Errorf("prompt must use `pwsh`, not legacy `powershell` for invocations. found %q in prompt=\n%s", mustNot, call.prompt)
		}
	}
}

func TestAzureRMRefreshHookPropagatesRunSessionError(t *testing.T) {
	rec := &runSessionRecorder{err: errors.New("boom")}
	hook, err := NewAzureRMRefreshHook(AzureRMRefreshHookOptions{
		RunSession: rec.run(),
		ScriptPath: `C:\fake\refresh-copilot-setup.ps1`,
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewAzureRMRefreshHook: %v", err)
	}

	dir := t.TempDir()
	writeGoMod(t, dir, "module github.com/hashicorp/terraform-provider-azurerm\n")

	err = hook(context.Background(), WorkDirInfo{CardID: "card", WorkDir: dir})
	if err == nil {
		t.Fatal("expected error to bubble up from RunSession")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error chain does not include underlying cause: %v", err)
	}
}

func TestSessionSpawnerBeforeStartFails(t *testing.T) {
	r := NewCopilotRunner("m", silentLogger())
	spawner := r.SessionSpawner()
	if spawner == nil {
		t.Fatal("SessionSpawner must never return nil")
	}
	if _, err := spawner.Spawn(context.Background(), &copilot.SessionConfig{}); err == nil {
		t.Fatal("spawning before Start must fail")
	}
}
