package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// setupConfigSrc creates an empty directory in a per-test temp dir and
// returns its path. Tests use this to provide a non-empty --config-src
// value; LoadConfig itself does not stat the path (resolution happens
// in ResolveConfigSrc at startup), so any non-empty string would work.
// Keeping the path real makes the intent clear and lets tests evolve
// to call ResolveConfigSrc directly without re-plumbing fixtures.
func setupConfigSrc(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "config-src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config-src: %v", err)
	}
	return dir
}

func setRequiredConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("JJC_CONFIG_SRC", setupConfigSrc(t))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")
}

func TestLoadConfigWorkDirBaseFromEnv(t *testing.T) {
	setRequiredConfigEnv(t)
	base := filepath.Join(t.TempDir(), "jjc-work")
	t.Setenv("JJC_WORK_DIR_BASE", base)

	cfg, err := LoadConfig([]string{"cmd"})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.WorkDirBase != base {
		t.Fatalf("expected work dir base from env %q, got %q", base, cfg.WorkDirBase)
	}
}

func TestLoadConfigWorkDirBaseDefault(t *testing.T) {
	setRequiredConfigEnv(t)

	cfg, err := LoadConfig([]string{"cmd"})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	want := defaultWorkDirBase(runtime.GOOS)
	if cfg.WorkDirBase != want {
		t.Fatalf("expected default work dir base %q, got %q", want, cfg.WorkDirBase)
	}
}

func TestLoadConfigWorkDirBaseRequiresAbsolutePath(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv("JJC_WORK_DIR_BASE", "relative-work")

	_, err := LoadConfig([]string{"cmd"})
	if err == nil {
		t.Fatal("expected relative work dir base to fail validation")
	}
	if !strings.Contains(err.Error(), "work dir base") {
		t.Fatalf("error should mention work dir base: %v", err)
	}
}

func TestLoadConfigWorkDirBaseRejectsEmptyFlag(t *testing.T) {
	setRequiredConfigEnv(t)

	_, err := LoadConfig([]string{"cmd", "--work-dir-base", ""})
	if err == nil {
		t.Fatal("expected empty work dir base flag to fail validation")
	}
	if !strings.Contains(err.Error(), "missing work dir base") {
		t.Fatalf("error should mention missing work dir base: %v", err)
	}
}

func TestEnsureWorkDirBaseCreatesMissingDirectory(t *testing.T) {
	base := filepath.Join(t.TempDir(), "nested", "work")

	if err := EnsureWorkDirBase(base); err != nil {
		t.Fatalf("ensure work dir base: %v", err)
	}
	if !dirExists(base) {
		t.Fatalf("work dir base %q was not created", base)
	}
}

func TestEnsureWorkDirBaseRejectsInvalidPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  string
		want string
	}{
		{name: "empty", dir: "", want: "empty"},
		{name: "relative", dir: "relative-work", want: "absolute path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := EnsureWorkDirBase(tc.dir)
			if err == nil {
				t.Fatal("expected invalid work dir base to fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error should mention %q, got %v", tc.want, err)
			}
		})
	}
}

func TestEnsureWorkDirBaseRejectsExistingFile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "work-file")
	if err := os.WriteFile(base, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := EnsureWorkDirBase(base)
	if err == nil {
		t.Fatal("expected existing file to fail work dir base validation")
	}
	if !strings.Contains(err.Error(), "ensure work dir base") {
		t.Fatalf("error should mention work dir base: %v", err)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9090")
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_GATEWAY_TUNNEL", "none")
	t.Setenv("COPILOT_MODEL", "env-model")
	t.Setenv("JJC_CONFIG_SRC", setupConfigSrc(t))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")
	workDirBase := filepath.Join(t.TempDir(), "work-from-env")
	t.Setenv("JJC_WORK_DIR_BASE", workDirBase)
	t.Setenv("LOG_FILE", "env.log")

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
	if cfg.Tunnel != "none" {
		t.Fatalf("unexpected tunnel: %s", cfg.Tunnel)
	}
	if cfg.CopilotModel != "env-model" {
		t.Fatalf("unexpected copilot model: %s", cfg.CopilotModel)
	}
	if cfg.ConfigSrc == "" {
		t.Fatalf("expected config src from env, got empty")
	}
	if cfg.KanbanBoardID != "env-board" {
		t.Fatalf("unexpected kanban board id: %s", cfg.KanbanBoardID)
	}
	if cfg.WorkDirBase != workDirBase {
		t.Fatalf("unexpected work dir base: %s", cfg.WorkDirBase)
	}
	if cfg.LogFile != "env.log" {
		t.Fatalf("unexpected log file: %s", cfg.LogFile)
	}
}

func TestLoadConfigFlagOverridesEnv(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9090")
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_GATEWAY_TUNNEL", "none")
	t.Setenv("COPILOT_MODEL", "env-model")
	t.Setenv("JJC_CONFIG_SRC", setupConfigSrc(t))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")
	t.Setenv("JJC_WORK_DIR_BASE", filepath.Join(t.TempDir(), "work-from-env"))
	flagWorkDirBase := filepath.Join(t.TempDir(), "work-from-flag")

	cfg, err := LoadConfig([]string{"cmd", "--listen", ":8088", "--trello-api-secret", "flag-secret", "--copilot-model", "flag-model", "--config-src", "flag-config", "--work-dir-base", flagWorkDirBase, "--kanban-board-id", "flag-board", "--log-file", "flag.log"})
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
	if cfg.ConfigSrc != "flag-config" {
		t.Fatalf("expected config src from flag, got %s", cfg.ConfigSrc)
	}
	if cfg.KanbanBoardID != "flag-board" {
		t.Fatalf("expected kanban board id from flag, got %s", cfg.KanbanBoardID)
	}
	if cfg.WorkDirBase != flagWorkDirBase {
		t.Fatalf("expected work dir base from flag, got %s", cfg.WorkDirBase)
	}
	if cfg.LogFile != "flag.log" {
		t.Fatalf("expected log file from flag, got %s", cfg.LogFile)
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
	t.Setenv("JJC_CONFIG_SRC", setupConfigSrc(t))
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
	if cfg.Tunnel != "cloudflared" {
		t.Fatalf("expected default tunnel cloudflared, got %q", cfg.Tunnel)
	}
	if cfg.CallbackURL != "" {
		t.Fatalf("callback URL should be empty before auto-tunnel starts, got %q", cfg.CallbackURL)
	}
	if cfg.ConfigSrc == "" {
		t.Fatalf("expected config src from env, got empty")
	}
	if cfg.LogFile != "trellooperator.log" {
		t.Fatalf("expected default log file trellooperator.log, got %q", cfg.LogFile)
	}
}

// TestLoadConfigConfigSrcRequired pins the requirement that --config-src /
// JJC_CONFIG_SRC must be explicitly configured rather than silently falling
// back to an operator-local path.
func TestLoadConfigConfigSrcRequired(t *testing.T) {
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_GATEWAY_TUNNEL", "none")
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")
	// Explicitly empty JJC_CONFIG_SRC to prove there is no fallback default.
	t.Setenv("JJC_CONFIG_SRC", "")

	_, err := LoadConfig([]string{"cmd"})
	if err == nil {
		t.Fatal("expected error when config src is missing")
	}
	if !strings.Contains(err.Error(), "config src") {
		t.Fatalf("error should mention config src: %v", err)
	}
}

// TestLoadConfigKanbanBoardIDRequired pins the issue's requirement that
// startup fails if --kanban-board-id / TRELLO_KANBAN_BOARD_ID is empty.
func TestLoadConfigKanbanBoardIDRequired(t *testing.T) {
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("CALLBACK_URL", "https://env.example.com/trello")
	t.Setenv("TRELLO_GATEWAY_TUNNEL", "none")
	t.Setenv("JJC_CONFIG_SRC", setupConfigSrc(t))
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

func TestLoadConfigTunnelValidation(t *testing.T) {
	baseEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("TRELLO_API_SECRET", "env-secret")
		t.Setenv("TRELLO_API_KEY", "env-key")
		t.Setenv("TRELLO_API_TOKEN", "env-token")
		t.Setenv("JJC_CONFIG_SRC", setupConfigSrc(t))
		t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")
	}

	t.Run("none requires callback URL", func(t *testing.T) {
		baseEnv(t)
		_, err := LoadConfig([]string{"cmd", "--tunnel", "none"})
		if err == nil || !strings.Contains(err.Error(), "when --tunnel=none") {
			t.Fatalf("expected callback-url required error, got %v", err)
		}
	})

	t.Run("cloudflared rejects callback URL", func(t *testing.T) {
		baseEnv(t)
		_, err := LoadConfig([]string{"cmd", "--callback-url", "https://example.com/"})
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("expected mutually exclusive error, got %v", err)
		}
	})

	t.Run("unknown tunnel rejected", func(t *testing.T) {
		baseEnv(t)
		_, err := LoadConfig([]string{"cmd", "--tunnel", "ngrok"})
		if err == nil || !strings.Contains(err.Error(), "unknown tunnel provider") {
			t.Fatalf("expected unknown tunnel error, got %v", err)
		}
	})

	t.Run("empty tunnel uses default", func(t *testing.T) {
		baseEnv(t)
		cfg, err := LoadConfig([]string{"cmd", "--tunnel", ""})
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Tunnel != "cloudflared" {
			t.Fatalf("expected empty tunnel to default to cloudflared, got %q", cfg.Tunnel)
		}
	})
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
		Tunnel:         "none",
		CopilotModel:   "m",
		ConfigSrc:      "c",
		WorkDirBase:    "work",
		KanbanBoardID:  "b",
		LogFile:        "trellooperator.log",
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
		Tunnel:         "none",
		CopilotModel:   "model",
		ConfigSrc:      "config-src",
		WorkDirBase:    "work-dir-base",
		KanbanBoardID:  "board",
		LogFile:        "trellooperator.log",
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
		Tunnel:         "REDACTED_TEST_TUNNEL",
		CopilotModel:   "REDACTED_TEST_MODEL",
		ConfigSrc:      "REDACTED_TEST_CONFIG_SRC",
		WorkDirBase:    "REDACTED_TEST_WORK_DIR_BASE",
		KanbanBoardID:  "REDACTED_TEST_BOARD",
		LogFile:        "REDACTED_TEST_LOG_FILE",
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

// TestHelpDoesNotLeakSecretsFromEnv pins the load-bearing fix from
// the "-help echoed default values" security bug: when an operator
// runs the gateway with the three TRELLO_* env vars set and asks
// for --help, the rendered usage MUST NOT contain the env-derived
// secret values. Go's flag package renders the *current* variable
// contents as the (default ...) in usage, so the bug is reproduced
// by simply registering the flag with the env-seeded variable as
// the default. The fix registers them with an empty default and
// overlays the env value AFTER flag.Parse via
// overlaySecretFromEnv. This test runs --help and asserts none of
// the three secret values surface.
func TestHelpDoesNotLeakSecretsFromEnv(t *testing.T) {
	const (
		secret = "DO-NOT-LEAK-secret-9c0a4ff1d2b6"
		key    = "DO-NOT-LEAK-key-7b3e5d8a1f04"
		token  = "DO-NOT-LEAK-token-2e8a17c4b5f9"
	)
	t.Setenv("TRELLO_API_SECRET", secret)
	t.Setenv("TRELLO_API_KEY", key)
	t.Setenv("TRELLO_API_TOKEN", token)
	t.Setenv("JJC_CONFIG_SRC", setupConfigSrc(t))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")

	var buf bytes.Buffer
	_, err := loadConfigWithOutput([]string{"cmd", "-h"}, &buf)
	// flag.ContinueOnError surfaces flag.ErrHelp from a -h request;
	// LoadConfig wraps it as a plain error. Either way, we expect
	// loadConfigWithOutput to NOT return nil here (the parse
	// short-circuits before validation), but the help text must be
	// present in the buffer regardless.
	if err == nil {
		t.Fatal("expected error from -h parse (flag.ErrHelp); got nil")
	}
	help := buf.String()
	if help == "" {
		t.Fatal("--help produced empty output; cannot assert on its contents")
	}
	for name, leaked := range map[string]string{
		"TRELLO_API_SECRET": secret,
		"TRELLO_API_KEY":    key,
		"TRELLO_API_TOKEN":  token,
	} {
		if strings.Contains(help, leaked) {
			t.Errorf("--help leaks %s env value into usage output; the flag's default must not echo the env var.\nleaked value: %q\nfull help:\n%s",
				name, leaked, help)
		}
	}
}

// TestHelpAdvertisesEnvOverridesForSecrets makes the operator-facing
// affordance explicit: even though the secret flags register with an
// empty default (per the leak-prevention rule above), the usage text
// MUST still mention the matching env var name so an operator
// reading -h knows TRELLO_API_SECRET / TRELLO_API_KEY /
// TRELLO_API_TOKEN are alternative inputs.
func TestHelpAdvertisesEnvOverridesForSecrets(t *testing.T) {
	var buf bytes.Buffer
	// No env values needed: we are checking the usage TEXT, not what
	// the flag resolves to. The function will return flag.ErrHelp
	// before validation runs, so the missing env vars do not matter.
	_, _ = loadConfigWithOutput([]string{"cmd", "-h"}, &buf)
	help := buf.String()
	for _, envName := range []string{"TRELLO_API_SECRET", "TRELLO_API_KEY", "TRELLO_API_TOKEN"} {
		if !strings.Contains(help, envName) {
			t.Errorf("--help should mention env var %s next to its corresponding flag; full help:\n%s", envName, help)
		}
	}
}

// TestEnvOverlayHonoursCLIPrecedence verifies the post-Parse overlay
// preserves the documented CLI > env > default precedence for the
// three secret flags. Without this guard, the help-leak fix could
// accidentally clobber CLI-supplied values with env values (or vice
// versa).
func TestEnvOverlayHonoursCLIPrecedence(t *testing.T) {
	t.Setenv("TRELLO_API_SECRET", "env-secret")
	t.Setenv("TRELLO_API_KEY", "env-key")
	t.Setenv("TRELLO_API_TOKEN", "env-token")
	t.Setenv("JJC_CONFIG_SRC", setupConfigSrc(t))
	t.Setenv("TRELLO_KANBAN_BOARD_ID", "env-board")

	cfg, err := LoadConfig([]string{
		"cmd",
		"--trello-api-secret", "cli-secret",
		// --trello-api-key intentionally omitted: env should win.
		"--trello-api-token", "cli-token",
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.TrelloSecret != "cli-secret" {
		t.Errorf("CLI must win over env for --trello-api-secret; got %q", cfg.TrelloSecret)
	}
	if cfg.TrelloAPIKey != "env-key" {
		t.Errorf("env must be picked up when CLI flag is absent; got %q", cfg.TrelloAPIKey)
	}
	if cfg.TrelloAPIToken != "cli-token" {
		t.Errorf("CLI must win over env for --trello-api-token; got %q", cfg.TrelloAPIToken)
	}
}
