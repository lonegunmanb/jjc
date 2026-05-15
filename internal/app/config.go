package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

type Config struct {
	ListenAddr   string
	TrelloSecret string
	// TrelloAPIKey / TrelloAPIToken authenticate every outbound Trello
	// call the gateway makes (card lookups, comments, list moves) via
	// the Go SDK. They replace the previous PowerShell-script-based
	// scheme that read these same env vars per shell-out. Sourced from
	// --trello-api-key / TRELLO_API_KEY (and the matching token pair).
	TrelloAPIKey   string
	TrelloAPIToken string
	CallbackURL    string
	CopilotModel   string
	// RouterDir is the directory containing the trello helper scripts
	// under scripts/ (e.g. trello-get-card-info.ps1,
	// refresh-copilot-setup.ps1). Entry playbooks no longer live here —
	// they are loaded from PlaybooksDir and pre-rendered into a
	// process-temp directory at startup. RouterDir remains because the
	// scripts still ship alongside the operator's router checkout.
	RouterDir string
	// PlaybooksDir is the directory holding all .md playbook files
	// (skeleton prompts and entry playbooks). Every .md file under it is
	// copied into a process-level temp directory at startup, then each
	// rendered file has its `{{<basename>}}` references substituted with
	// the absolute temp-dir path of the named file. The directory MUST
	// exist; missing target referenced via `{{...}}` causes startup to
	// fail with event=playbook_render_failed. Override via
	// TRELLO_PLAYBOOKS_DIR or --playbooks-dir.
	PlaybooksDir string
}

// DefaultRouterDir is the conventional location of the workspace-trello-router
// checkout on the operator's machine. Override via WORKSPACE_TRELLO_ROUTER_DIR
// or --router-dir.
const DefaultRouterDir = `C:\Users\zjhe\.openclaw\workspace-trello-router`

// DefaultPlaybooksDirName is the conventional default basename of the
// playbooks source directory, looked up under the process's current
// working directory. Override via TRELLO_PLAYBOOKS_DIR or --playbooks-dir.
const DefaultPlaybooksDirName = ".playbooks"

func LoadConfig(args []string) (Config, error) {
	cfg := Config{
		ListenAddr:     envOrDefault("LISTEN_ADDR", ":18790"),
		TrelloSecret:   os.Getenv("TRELLO_API_SECRET"),
		TrelloAPIKey:   os.Getenv("TRELLO_API_KEY"),
		TrelloAPIToken: os.Getenv("TRELLO_API_TOKEN"),
		CallbackURL:    os.Getenv("CALLBACK_URL"),
		CopilotModel:   envOrDefault("COPILOT_MODEL", DefaultCopilotModel),
		RouterDir:      envOrDefault("WORKSPACE_TRELLO_ROUTER_DIR", DefaultRouterDir),
		PlaybooksDir:   envOrDefault("TRELLO_PLAYBOOKS_DIR", defaultPlaybooksDir()),
	}

	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "listen address")
	fs.StringVar(&cfg.TrelloSecret, "trello-api-secret", cfg.TrelloSecret, "Trello API secret")
	fs.StringVar(&cfg.TrelloAPIKey, "trello-api-key", cfg.TrelloAPIKey, "Trello API key (used by the Go SDK to talk to api.trello.com)")
	fs.StringVar(&cfg.TrelloAPIToken, "trello-api-token", cfg.TrelloAPIToken, "Trello API token (used by the Go SDK to talk to api.trello.com)")
	fs.StringVar(&cfg.CallbackURL, "callback-url", cfg.CallbackURL, "webhook callback URL used for signature verification")
	fs.StringVar(&cfg.CopilotModel, "copilot-model", cfg.CopilotModel, "Copilot model to use for the agent session")
	fs.StringVar(&cfg.RouterDir, "router-dir", cfg.RouterDir, "directory containing trello helper scripts under scripts/")
	fs.StringVar(&cfg.PlaybooksDir, "playbooks-dir", cfg.PlaybooksDir, "directory containing playbook .md files (default <cwd>/.playbooks)")

	if err := fs.Parse(args[1:]); err != nil {
		return Config{}, err
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func validateConfig(cfg Config) error {
	if cfg.TrelloSecret == "" {
		return errors.New("missing trello secret, set --trello-api-secret or TRELLO_API_SECRET")
	}
	if cfg.TrelloAPIKey == "" {
		return errors.New("missing trello api key, set --trello-api-key or TRELLO_API_KEY")
	}
	if cfg.TrelloAPIToken == "" {
		return errors.New("missing trello api token, set --trello-api-token or TRELLO_API_TOKEN")
	}
	if cfg.CallbackURL == "" {
		return errors.New("missing callback URL, set --callback-url or CALLBACK_URL")
	}
	if cfg.CopilotModel == "" {
		return errors.New("missing copilot model, set --copilot-model or COPILOT_MODEL")
	}
	if cfg.PlaybooksDir == "" {
		return errors.New("missing playbooks dir, set --playbooks-dir or TRELLO_PLAYBOOKS_DIR")
	}
	info, err := os.Stat(cfg.PlaybooksDir)
	if err != nil {
		return fmt.Errorf("playbooks-dir %q invalid: %w", cfg.PlaybooksDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("playbooks-dir %q is not a directory", cfg.PlaybooksDir)
	}
	return nil
}

func defaultPlaybooksDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return DefaultPlaybooksDirName
	}
	return wd + string(os.PathSeparator) + DefaultPlaybooksDirName
}

func envOrDefault(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

func (c Config) Redacted() string {
	return fmt.Sprintf("listen=%s callback_url=%s copilot_model=%s router_dir=%s playbooks_dir=%s trello_api_secret=%s trello_api_key=%s trello_api_token=%s",
		c.ListenAddr, c.CallbackURL, c.CopilotModel, c.RouterDir, c.PlaybooksDir,
		redact(c.TrelloSecret), redact(c.TrelloAPIKey), redact(c.TrelloAPIToken))
}

func redact(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return "****"
	}
	return v[:2] + "***" + v[len(v)-2:]
}
