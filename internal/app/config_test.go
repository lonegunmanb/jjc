package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// setupPlaybooksDir creates an empty .playbooks directory in a per-test
// temp dir and returns its path. Tests use this to satisfy the
// playbooks-dir validation that requires the directory to exist.
func setupPlaybooksDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".playbooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir playbooks: %v", err)
	}
	return dir
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9090")
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("COPILOT_MODEL", "env-model")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", setupPlaybooksDir(t))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")

	cfg, err := LoadConfig([]string{"cmd"})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Fatalf("unexpected listen: %s", cfg.ListenAddr)
	}
	if cfg.TrelloSecret != "env-secret" {
		t.Fatalf("unexpected secret: %s", cfg.TrelloSecret)
	}
	if cfg.TrelloAPIKey != "env-key" || cfg.TrelloAPIToken != "env-token" {
		t.Fatalf("unexpected key/token: %q/%q", cfg.TrelloAPIKey, cfg.TrelloAPIToken)
	}
	if cfg.CallbackURL != "https://env.example.com/trello" {
		t.Fatalf("unexpected callback: %s", cfg.CallbackURL)
	}
	if cfg.CopilotModel != "env-model" {
		t.Fatalf("unexpected copilot model: %s", cfg.CopilotModel)
	}
	if cfg.KanbanBoardID != "env-board" {
		t.Fatalf("unexpected kanban board id: %s", cfg.KanbanBoardID)
	}
}

func TestLoadConfigFlagOverridesEnv(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9090")
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("COPILOT_MODEL", "env-model")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", setupPlaybooksDir(t))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")

	cfg, err := LoadConfig([]string{"cmd", "--listen", ":8088", "--trello-api-secret", "flag-secret", "--copilot-model", "flag-model", "--kanban-board-id", "flag-board"})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ListenAddr != ":8088" {
		t.Fatalf("expected listen from flag, got %s", cfg.ListenAddr)
	}
	if cfg.TrelloSecret != "flag-secret" {
		t.Fatalf("expected secret from flag, got %s", cfg.TrelloSecret)
	}
	if cfg.CopilotModel != "flag-model" {
		t.Fatalf("expected copilot model from flag, got %s", cfg.CopilotModel)
	}
	if cfg.KanbanBoardID != "flag-board" {
		t.Fatalf("expected kanban board id from flag, got %s", cfg.KanbanBoardID)
	}
}

func TestLoadConfigMissingRequired(t *testing.T) {
	_, err := LoadConfig([]string{"cmd"})
	if err == nil {
		t.Fatal("expected error when required fields are missing")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", setupPlaybooksDir(t))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")

	cfg, err := LoadConfig([]string{"cmd"})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ListenAddr != ":18790" {
		t.Fatalf("expected default listen addr :18790, got %s", cfg.ListenAddr)
	}
	if cfg.CopilotModel != DefaultCopilotModel {
		t.Fatalf("expected default copilot model %q, got %q", DefaultCopilotModel, cfg.CopilotModel)
	}
}

func TestLoadConfigPlaybooksDirMissing(t *testing.T) {
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")

	_, err := LoadConfig([]string{"cmd"})
	if err == nil {
		t.Fatal("expected error when playbooks-dir does not exist")
	}
}

// TestLoadConfigKanbanBoardIDRequired pins the issue's requirement that
// startup fails if --kanban-board-id / TRELLO_KANBAN_BOARD_ID is empty.
func TestLoadConfigKanbanBoardIDRequired(t *testing.T) {
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", setupPlaybooksDir(t))
	// Intentionally do NOT set TRELLO_KANBAN_BOARD_ID.
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "")

	_, err := LoadConfig([]string{"cmd"})
	if err == nil {
		t.Fatal("expected error when kanban board id is missing")
	}
	if !strings.Contains(err.Error(), "kanban board id") {
		t.Fatalf("error should mention kanban board id: %v", err)
	}
}

// TestRedactedDoesNotLeakPrefix pins the contract that redact() never
// surfaces any meaningful slice of the underlying secret. A short
// prefix may look harmless but is enough to fingerprint a Trello API
// key (32-char hex) across hosts, which is precisely what we want the
// boot log to NOT expose. We use highly distinct secret values so a
// failure can only come from redact() itself, not from random byte
// collisions with the format string.
func TestRedactedDoesNotLeakPrefix(t *testing.T) {
	c := Config{
		ListenAddr:     ":1",
		TrelloSecret:   "QQQQsecretQQQQ12345678",
		TrelloAPIKey:   "WWWWapikeyWWWW0123456789abcdef0123456789abcdef",
		TrelloAPIToken: "EEEEapitokenEEEE9876543210abcdef9876543210",
		CallbackURL:    "https://example.com/trello",
		CopilotModel:   "m",
		RouterDir:      "r",
		PlaybooksDir:   "p",
		KanbanBoardID:  "b",
	}
	out := c.Redacted()
	for _, secret := range []string{c.TrelloSecret, c.TrelloAPIKey, c.TrelloAPIToken} {
		// Distinctive 4-byte prefixes / suffixes must not appear
		// verbatim. 4 bytes is short enough to be "obviously a
		// fingerprint" but long enough that random collisions with the
		// format string are statistically negligible.
		const n = 4
		if strings.Contains(out, secret[:n]) {
			t.Errorf("redact leaked %d-byte prefix %q of %q in: %s", n, secret[:n], secret, out)
		}
		if strings.Contains(out, secret[len(secret)-n:]) {
			t.Errorf("redact leaked %d-byte suffix %q of %q in: %s", n, secret[len(secret)-n:], secret, out)
		}
	}
}

// TestRedactedCoversEverySensitiveField fails if a new Config field is
// added that LOOKS sensitive (name contains "secret" / "token" /
// "password" / "key" / "api") but does not appear redacted in
// Redacted(). The reflection check is conservative: any new
// secret-shaped field must either be wrapped with redact() in
// Redacted() or be explicitly whitelisted by editing this test (with
// reviewer sign-off in the PR description).
func TestRedactedCoversEverySensitiveField(t *testing.T) {
	c := Config{
		ListenAddr:     ":1",
		TrelloSecret:   "trellosecretvalue",
		TrelloAPIKey:   "trelloapikeyvalue",
		TrelloAPIToken: "trelloapitokenvalue",
		CallbackURL:    "url",
		CopilotModel:   "model",
		RouterDir:      "router",
		PlaybooksDir:   "playbooks",
		KanbanBoardID:  "board",
	}
	out := c.Redacted()

	sensitiveSubstrings := []string{"secret", "token", "password", "api"}
	rt := reflect.TypeOf(c)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		lower := strings.ToLower(f.Name)
		isSensitive := false
		for _, sub := range sensitiveSubstrings {
			if strings.Contains(lower, sub) {
				isSensitive = true
				break
			}
		}
		if !isSensitive {
			continue
		}
		// The raw value must NOT appear verbatim in Redacted output.
		v := reflect.ValueOf(c).Field(i)
		if v.Kind() != reflect.String {
			continue
		}
		raw := v.String()
		if raw == "" {
			continue
		}
		if strings.Contains(out, raw) {
			t.Fatalf("Config.%s (sensitive) leaked verbatim into Redacted output: %s",
				f.Name, out)
		}
	}
}

// TestRedactedMentionsEveryNonSensitiveField is the dual of the test
// above: every non-secret string field in Config should appear in the
// Redacted output so operators see a complete picture in the boot log.
// Adding a new non-secret field without updating Redacted is almost
// always an oversight; this test fails so the author makes a
// deliberate choice.
func TestRedactedMentionsEveryNonSensitiveField(t *testing.T) {
	c := Config{
		ListenAddr:     ":REDACTED_TEST_LISTEN",
		TrelloSecret:   "REDACTED_TEST_SECRET",
		TrelloAPIKey:   "REDACTED_TEST_KEY",
		TrelloAPIToken: "REDACTED_TEST_TOKEN",
		CallbackURL:    "REDACTED_TEST_URL",
		CopilotModel:   "REDACTED_TEST_MODEL",
		RouterDir:      "REDACTED_TEST_ROUTER",
		PlaybooksDir:   "REDACTED_TEST_PLAYBOOKS",
		KanbanBoardID:  "REDACTED_TEST_BOARD",
	}
	out := c.Redacted()
	sensitive := []string{"secret", "token", "password", "api"}
	rt := reflect.TypeOf(c)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		v := reflect.ValueOf(c).Field(i)
		if v.Kind() != reflect.String {
			continue
		}
		lower := strings.ToLower(f.Name)
		isSensitive := false
		for _, sub := range sensitive {
			if strings.Contains(lower, sub) {
				isSensitive = true
				break
			}
		}
		if isSensitive {
			continue
		}
		raw := v.String()
		if raw == "" {
			continue
		}
		if !strings.Contains(out, raw) {
			t.Fatalf("Config.%s (non-sensitive) is missing from Redacted output — please extend Redacted to mention it: %s",
				f.Name, out)
		}
	}
}
