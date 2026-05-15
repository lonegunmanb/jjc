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
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("COPILOT_MODEL", "env-model")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", setupPlaybooksDir(t))

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
	if cfg.CallbackURL != "https://env.example.com/trello" {
		t.Fatalf("unexpected callback: %s", cfg.CallbackURL)
	}
	if cfg.CopilotModel != "env-model" {
		t.Fatalf("unexpected copilot model: %s", cfg.CopilotModel)
	}
}

func TestLoadConfigFlagOverridesEnv(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9090")
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("COPILOT_MODEL", "env-model")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", setupPlaybooksDir(t))

	cfg, err := LoadConfig([]string{"cmd", "--listen", ":8088", "--trello-api-secret", "flag-secret", "--copilot-model", "flag-model"})
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
}

func TestLoadConfigMissingRequired(t *testing.T) {
	_, err := LoadConfig([]string{"cmd"})
	if err == nil {
		t.Fatal("expected error when required fields are missing")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", setupPlaybooksDir(t))

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
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_PLAYBOOKS_DIR", filepath.Join(t.TempDir(), "does-not-exist"))

	_, err := LoadConfig([]string{"cmd"})
	if err == nil {
		t.Fatal("expected error when playbooks-dir does not exist")
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
		ListenAddr:   ":1",
		TrelloSecret: "trellosecretvalue",
		CallbackURL:  "url",
		CopilotModel: "model",
		RouterDir:    "router",
		PlaybooksDir: "playbooks",
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
		ListenAddr:   ":REDACTED_TEST_LISTEN",
		TrelloSecret: "REDACTED_TEST_SECRET",
		CallbackURL:  "REDACTED_TEST_URL",
		CopilotModel: "REDACTED_TEST_MODEL",
		RouterDir:    "REDACTED_TEST_ROUTER",
		PlaybooksDir: "REDACTED_TEST_PLAYBOOKS",
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
