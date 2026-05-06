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
	CallbackURL  string
	CopilotModel string
	// RouterDir is the directory containing the per-work_type entry
	// playbook markdown files (e.g. azurerm_provider_issue.md) and the
	// trello-get-card-info.ps1 helper script under scripts/. The gateway
	// reads the appropriate entry playbook into each worker's system
	// prompt at session creation time.
	RouterDir string
}

// DefaultRouterDir is the conventional location of the workspace-trello-router
// checkout on the operator's machine. Override via WORKSPACE_TRELLO_ROUTER_DIR
// or --router-dir.
const DefaultRouterDir = `C:\Users\zjhe\.openclaw\workspace-trello-router`

func LoadConfig(args []string) (Config, error) {
	cfg := Config{
		ListenAddr:   envOrDefault("LISTEN_ADDR", ":18790"),
		TrelloSecret: os.Getenv("TRELLO_API_SECRET"),
		CallbackURL:  os.Getenv("CALLBACK_URL"),
		CopilotModel: envOrDefault("COPILOT_MODEL", DefaultCopilotModel),
		RouterDir:    envOrDefault("WORKSPACE_TRELLO_ROUTER_DIR", DefaultRouterDir),
	}

	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "listen address")
	fs.StringVar(&cfg.TrelloSecret, "trello-api-secret", cfg.TrelloSecret, "Trello API secret")
	fs.StringVar(&cfg.CallbackURL, "callback-url", cfg.CallbackURL, "webhook callback URL used for signature verification")
	fs.StringVar(&cfg.CopilotModel, "copilot-model", cfg.CopilotModel, "Copilot model to use for the agent session")
	fs.StringVar(&cfg.RouterDir, "router-dir", cfg.RouterDir, "directory containing per-work_type entry playbooks and trello scripts")

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
	if cfg.CallbackURL == "" {
		return errors.New("missing callback URL, set --callback-url or CALLBACK_URL")
	}
	if cfg.CopilotModel == "" {
		return errors.New("missing copilot model, set --copilot-model or COPILOT_MODEL")
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

func (c Config) Redacted() string {
	return fmt.Sprintf("listen=%s callback_url=%s copilot_model=%s router_dir=%s trello_api_secret=%s", c.ListenAddr, c.CallbackURL, c.CopilotModel, c.RouterDir, redact(c.TrelloSecret))
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
