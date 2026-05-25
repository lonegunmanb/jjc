package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/lonegunmanb/jjc/internal/app/sysevent"
	"github.com/lonegunmanb/jjc/internal/app/tunnel"
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
	Tunnel         string
	CopilotModel   string
	// ConfigSrc is the source of the JJC configuration bundle that
	// holds both router.hcl and every playbook .md file at the top
	// level. It can be either a local directory path or any source
	// understood by hashicorp/go-getter v2 (git::https://...,
	// https://..., github.com/owner/repo, file://..., etc.).
	//
	// When ConfigSrc is not a local directory, ResolveConfigSrc
	// downloads it into a per-process temp directory at startup and
	// removes that directory at shutdown. Override via JJC_CONFIG_SRC
	// or --config-src.
	ConfigSrc string
	// WorkDirBase is the absolute parent directory under which each
	// Trello card gets its own local work_dir. Override via
	// JJC_WORK_DIR_BASE or --work-dir-base.
	WorkDirBase string
	// KanbanBoardID is the Trello board the gateway resolves list
	// names against at startup (see internal/app/kanban). Sourced from
	// --kanban-board-id / TRELLO_KANBAN_BOARD_ID. Required: an empty
	// value fails validation with a clear error so a misconfigured
	// deployment never silently routes events with an un-resolved
	// kanban view.
	KanbanBoardID string
	// LogFile is the operator log destination. Defaults to the historical
	// trellooperator.log name for backward compatibility.
	LogFile string
}

func LoadConfig(args []string) (Config, error) {
	return loadConfigWithOutput(args, nil)
}

// loadConfigWithOutput is the package-private implementation of
// LoadConfig that lets a test redirect the flag-package's help/error
// output (defaults to nil, meaning os.Stderr). Externalising this is
// the only way to assert that --help never echoes secret env values
// without spawning a subprocess.
func loadConfigWithOutput(args []string, helpOutput io.Writer) (Config, error) {
	// Secret-bearing fields are deliberately NOT seeded from the
	// environment before flag.Parse. Doing so would let the flag
	// package surface the real value in `-help` as the "default"
	// (Go's flag package literally renders the current variable
	// contents in usage). We register them with an empty string
	// default for help output, then overlay TRELLO_API_SECRET /
	// TRELLO_API_KEY / TRELLO_API_TOKEN below only when the operator
	// did not pass the matching --flag on the command line. Precedence
	// is unchanged: CLI > env > (required, no built-in default).
	cfg := Config{
		ListenAddr:    envOrDefault("LISTEN_ADDR", ":18790"),
		CallbackURL:   os.Getenv("CALLBACK_URL"),
		Tunnel:        envOrDefault("TRELLO_GATEWAY_TUNNEL", tunnel.Cloudflared),
		CopilotModel:  envOrDefault("COPILOT_MODEL", DefaultCopilotModel),
		ConfigSrc:     os.Getenv("JJC_CONFIG_SRC"),
		WorkDirBase:   envOrDefault("JJC_WORK_DIR_BASE", defaultWorkDirBase(runtime.GOOS)),
		KanbanBoardID: os.Getenv("TRELLO_KANBAN_BOARD_ID"),
		LogFile:       envOrDefault("LOG_FILE", sysevent.DefaultLogFileName),
	}

	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	if helpOutput != nil {
		fs.SetOutput(helpOutput)
	}
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "listen address")
	// Register the three secret flags with an empty default so the
	// help text never echoes the env-derived value (see the comment
	// above). The env overlay happens after Parse.
	fs.StringVar(&cfg.TrelloSecret, "trello-api-secret", "", "Trello API secret (also TRELLO_API_SECRET; never printed in --help)")
	fs.StringVar(&cfg.TrelloAPIKey, "trello-api-key", "", "Trello API key, used by the Go SDK to talk to api.trello.com (also TRELLO_API_KEY; never printed in --help)")
	fs.StringVar(&cfg.TrelloAPIToken, "trello-api-token", "", "Trello API token, used by the Go SDK to talk to api.trello.com (also TRELLO_API_TOKEN; never printed in --help)")
	fs.StringVar(&cfg.CallbackURL, "callback-url", cfg.CallbackURL, "webhook callback URL used for signature verification")
	fs.StringVar(&cfg.Tunnel, "tunnel", cfg.Tunnel, "tunnel provider: cloudflared or none")
	fs.StringVar(&cfg.CopilotModel, "copilot-model", cfg.CopilotModel, "Copilot model to use for the agent session")
	fs.StringVar(&cfg.ConfigSrc, "config-src", cfg.ConfigSrc, "local directory or hashicorp/go-getter v2 source containing router.hcl and every playbook .md file (also JJC_CONFIG_SRC); remote sources are downloaded to a per-process temp dir at startup and removed on shutdown")
	fs.StringVar(&cfg.WorkDirBase, "work-dir-base", cfg.WorkDirBase, "absolute parent directory for per-card work_dir directories (also JJC_WORK_DIR_BASE)")
	fs.StringVar(&cfg.KanbanBoardID, "kanban-board-id", cfg.KanbanBoardID, "Trello board id whose lists the kanban {} block in router.hcl is resolved against")
	fs.StringVar(&cfg.LogFile, "log-file", cfg.LogFile, "operator log file path")

	if err := fs.Parse(args[1:]); err != nil {
		return Config{}, err
	}

	// Overlay env values for the secret flags only when the operator
	// did not pass the matching CLI flag. Without this, env-only
	// invocations (the README's primary path) would fail validation
	// because the flags were registered with an empty default.
	overlaySecretFromEnv(fs, &cfg.TrelloSecret, "trello-api-secret", "TRELLO_API_SECRET")
	overlaySecretFromEnv(fs, &cfg.TrelloAPIKey, "trello-api-key", "TRELLO_API_KEY")
	overlaySecretFromEnv(fs, &cfg.TrelloAPIToken, "trello-api-token", "TRELLO_API_TOKEN")

	if cfg.Tunnel == "" {
		cfg.Tunnel = tunnel.Cloudflared
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// overlaySecretFromEnv copies the named env var into *dst when the
// flag was NOT explicitly set on the command line. This keeps the
// documented precedence (CLI > env > default) intact while letting
// the flag's registered default stay empty so --help never echoes
// the operator's real secret.
func overlaySecretFromEnv(fs *flag.FlagSet, dst *string, flagName, envName string) {
	setOnCLI := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == flagName {
			setOnCLI = true
		}
	})
	if !setOnCLI {
		if v := os.Getenv(envName); v != "" {
			*dst = v
		}
	}
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
	switch cfg.Tunnel {
	case tunnel.Cloudflared:
		if cfg.CallbackURL != "" {
			return errors.New("--callback-url/CALLBACK_URL is mutually exclusive with --tunnel=cloudflared; use --tunnel=none to manage the callback URL manually")
		}
	case tunnel.None:
		if cfg.CallbackURL == "" {
			return errors.New("missing callback URL, set --callback-url or CALLBACK_URL when --tunnel=none")
		}
	default:
		return fmt.Errorf("unknown tunnel provider %q (valid: %s, %s)", cfg.Tunnel, tunnel.Cloudflared, tunnel.None)
	}
	if cfg.CopilotModel == "" {
		return errors.New("missing copilot model, set --copilot-model or COPILOT_MODEL")
	}
	if cfg.ConfigSrc == "" {
		return errors.New("missing config src, set --config-src or JJC_CONFIG_SRC (a local directory or a hashicorp/go-getter v2 source containing router.hcl and playbook .md files)")
	}
	if cfg.WorkDirBase == "" {
		return errors.New("missing work dir base, set --work-dir-base or JJC_WORK_DIR_BASE")
	}
	if !filepath.IsAbs(cfg.WorkDirBase) {
		return fmt.Errorf("work dir base must be an absolute path: %s", cfg.WorkDirBase)
	}
	if cfg.KanbanBoardID == "" {
		return errors.New("missing kanban board id, set --kanban-board-id or TRELLO_KANBAN_BOARD_ID")
	}
	if cfg.LogFile == "" {
		return errors.New("missing log file, set --log-file or LOG_FILE")
	}
	// ConfigSrc may be a remote URL (git::https://..., https://...),
	// which obviously cannot be stat()ed here; validation that it
	// resolves to a usable directory happens in ResolveConfigSrc at
	// startup so the failure surfaces with a structured event.
	return nil
}

func envOrDefault(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

func EnsureWorkDirBase(dir string) error {
	if dir == "" {
		return errors.New("work dir base is empty")
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("work dir base must be an absolute path: %s", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure work dir base %s: %w", dir, err)
	}
	return nil
}

func (c Config) Redacted() string {
	return fmt.Sprintf("listen=%s callback_url=%s tunnel=%s copilot_model=%s config_src=%s work_dir_base=%s kanban_board_id=%s log_file=%s trello_api_secret=%s trello_api_key=%s trello_api_token=%s",
		c.ListenAddr, c.CallbackURL, c.Tunnel, c.CopilotModel, c.ConfigSrc, c.WorkDirBase, c.KanbanBoardID, c.LogFile,
		redact(c.TrelloSecret), redact(c.TrelloAPIKey), redact(c.TrelloAPIToken))
}

// redact replaces a sensitive value with a length-only fingerprint so
// boot-log lines never leak prefix bytes. Trello API keys are 32-char
// hex; even a 2-char prefix is enough to fingerprint a token across
// hosts. We deliberately surface only the rune count so operators can
// still tell "is the env var actually set" from "is it empty".
func redact(v string) string {
	if v == "" {
		return ""
	}
	return fmt.Sprintf("<redacted len=%d>", len(v))
}
