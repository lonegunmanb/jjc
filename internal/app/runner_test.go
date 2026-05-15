package app

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/trello-copilot/internal/app/prompttmpl"
)

func TestNewCopilotRunnerDefaultsModel(t *testing.T) {
	r := NewCopilotRunner("", nil)
	if r.model != DefaultCopilotModel {
		t.Fatalf("expected default model %q, got %q", DefaultCopilotModel, r.model)
	}
	if r.dispatcher == nil {
		t.Fatal("dispatcher must be set")
	}
}

func TestRunnerHandleInvalidJSONFails(t *testing.T) {
	r := newStubbedRunner(t, newFakeFactory())
	if _, err := r.Handle(context.Background(), "evt-bad", []byte("not-json")); err == nil {
		t.Fatal("expected slim error for non-json body")
	}
}

func TestRunnerHandleDuplicateActionIsDropped(t *testing.T) {
	factory := newFakeFactory()
	r := newStubbedRunner(t, factory)

	body := []byte(`{"action":{"id":"a1","type":"updateCard","data":{"card":{"id":"card-c"},"listAfter":{"name":"Analyze"}}}}`)

	if _, err := r.Handle(context.Background(), "evt-1", body); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if _, err := r.Handle(context.Background(), "evt-2", body); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		s := factory.get("card-c")
		if s == nil {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.prompts) == 1
	}, "first prompt landed")
	time.Sleep(50 * time.Millisecond)

	s := factory.get("card-c")
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.prompts) != 1 {
		t.Fatalf("expected dedup to keep only one prompt, got %d", len(s.prompts))
	}
}

func TestNewWorkerSessionWithoutClientReturnsError(t *testing.T) {
	r := NewCopilotRunner("model", log.Default())
	if _, err := r.NewWorkerSession(context.Background(), "card", nil); err == nil {
		t.Fatal("expected error when client is not started")
	} else if !strings.Contains(err.Error(), "client not started") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAssembleWorkerSystemPromptContainsExpectedSections(t *testing.T) {
	got := assembleWorkerSystemPrompt("the-card-id", workerBootstrap{cardID: "the-card-id"}, nil)
	for _, must := range []string{"# BOOTSTRAP", "# IDENTITY", "# WORKER", "# TOOLS", "# USER", "# CARD CONTEXT", "the-card-id"} {
		if !strings.Contains(got, must) {
			t.Fatalf("worker system prompt missing %q", must)
		}
	}
	// MANAGER.md must NOT appear; this run is worker-centric.
	if strings.Contains(got, "# MANAGER") {
		t.Fatalf("worker system prompt should not contain a MANAGER section:\n%s", got)
	}
}

func TestAssembleWorkerSystemPromptInlinesPlaybook(t *testing.T) {
	bs := workerBootstrap{
		cardID: "card-1",
		classification: CardClassification{
			WorkType: WorkTypeProviderAzureRM,
			Kind:     KindIssue,
			Owner:    "hashicorp",
			Repo:     "terraform-provider-azurerm",
			Number:   "32258",
			URL:      "https://github.com/hashicorp/terraform-provider-azurerm/issues/32258",
		},
		playbookFilename: "azurerm_provider_issue.md",
		playbookPath:     `C:\fake\azurerm_provider_issue.md`,
		playbookContent:  "# AZURERM ISSUE PLAYBOOK\n\nStep A: classify.\n",
	}
	got := assembleWorkerSystemPrompt("card-1", bs, nil)
	for _, must := range []string{
		"work_type: terraform-provider-azurerm",
		"kind: issue",
		"github_repo: hashicorp/terraform-provider-azurerm",
		"github_number: 32258",
		"entry_playbook: ",
		"## ENTRY PLAYBOOK — azurerm_provider_issue.md",
		"# AZURERM ISSUE PLAYBOOK",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("expected substring %q in prompt", must)
		}
	}
	// Fallback wording must NOT appear when a playbook is inlined.
	if strings.Contains(got, "Fall back to the WORKER.md §0 self-bootstrap") {
		t.Fatalf("fallback notice should not appear when playbook is inlined")
	}
}

func TestAssembleWorkerSystemPromptFallback(t *testing.T) {
	got := assembleWorkerSystemPrompt("card-x", workerBootstrap{cardID: "card-x"}, nil)
	if !strings.Contains(got, "Fall back to the WORKER.md §0 self-bootstrap") {
		t.Fatalf("expected fallback notice, got:\n%s", got)
	}
}

func TestAssembleWorkerSystemPromptUsesRenderedSkeletons(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "WORKER.md"), []byte("custom worker body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := prompttmpl.New(prompttmpl.Options{
		PlaybooksDir: src,
		EmbeddedDefaults: map[string]string{
			"BOOTSTRAP.md": "embedded-bootstrap",
			"IDENTITY.md":  "embedded-identity",
			"WORKER.md":    "embedded-worker",
			"TOOLS.md":     "embedded-tools",
			"USER.md":      "embedded-user",
		},
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}
	defer r.Cleanup()

	got := assembleWorkerSystemPrompt("card-y", workerBootstrap{cardID: "card-y"}, r)
	if !strings.Contains(got, "custom worker body") {
		t.Fatalf("expected user-supplied WORKER content; got:\n%s", got)
	}
	if !strings.Contains(got, "embedded-bootstrap") {
		t.Fatalf("expected embedded BOOTSTRAP fallback; got:\n%s", got)
	}
	workerPath, _ := r.Path("WORKER.md")
	if !strings.Contains(got, workerPath) {
		t.Fatalf("expected WORKER override comment to mention %s; got:\n%s", workerPath, got)
	}
}

func TestAssembleEventPromptHasTaskOnly(t *testing.T) {
	raw := []byte(`{"action":{"type":"updateCard","data":{"card":{"id":"card1","name":"Card A"}}}}`)
	slim, err := slimRawBody(raw)
	if err != nil {
		t.Fatalf("slim: %v", err)
	}
	got := assembleEventPrompt(raw, slim)
	if !strings.Contains(got, "# TASK") {
		t.Fatalf("expected # TASK section: %s", got)
	}
	for _, mustNot := range []string{"# BOOTSTRAP", "# IDENTITY", "# WORKER", "# TOOLS", "# USER"} {
		if strings.Contains(got, mustNot) {
			t.Fatalf("event prompt should not contain %q", mustNot)
		}
	}
}

// Sanity: when stop is called multiple times no panic occurs.
func TestRunnerStopIsIdempotent(t *testing.T) {
	r := NewCopilotRunner("m", log.Default())
	if err := r.Stop(); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

var _ = errors.New // keep errors imported for future use
