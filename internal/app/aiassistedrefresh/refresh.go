// Package aiassistedrefresh replaces the legacy
// <router-dir>/scripts/refresh-copilot-setup.ps1 wrapper with a synchronous
// Go entry point. It clones the upstream
// WodansSon/terraform-azurerm-ai-assisted-development repository into a
// per-call temp directory and shells out to the installer it ships there.
//
// The upstream installer is offered in two equivalent flavours:
//
//   - install-copilot-setup.ps1 (PowerShell 7+, Windows + cross-platform)
//   - install-copilot-setup.sh  (Bash, macOS / Linux)
//
// This package picks the right one for the current GOOS at runtime so the
// gateway itself stays Go-native and does not impose a hard pwsh.exe
// dependency on Linux containers (and conversely does not require bash on
// a Windows operator host). The refresh hook in the gateway calls
// Service.Refresh directly — there is no Copilot session, no LLM turn, and
// no prompt involvement in the refresh path anymore.
package aiassistedrefresh

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// DefaultSourceRepoURL is the upstream that ships the AI-assisted
// development installer + payload (instructions / prompts / skills).
const DefaultSourceRepoURL = "https://github.com/WodansSon/terraform-azurerm-ai-assisted-development.git"

// installerSubdir is the path inside the cloned upstream repo where the
// platform-specific installer entry points live.
const installerSubdir = "installer"

// userProfileSubdir is the directory the installer copies itself into
// when invoked with -Bootstrap. Documented at
// https://github.com/WodansSon/terraform-azurerm-ai-assisted-development/blob/main/installer/README.md.
const userProfileSubdir = ".terraform-azurerm-ai-installer"

// issueArgPattern guards the -Issue argument: the value is interpolated
// into a `git checkout -b issue-<n>` invocation, so we restrict it to
// what GitHub actually issues for issue / PR numbers — a positive integer
// up to seven digits (well above today's max). This is tighter than the
// legacy PowerShell wrapper's `^[A-Za-z0-9._-]+$` (which would have
// accepted refs like `..`, `.lock`, or `-help`) but matches every value
// card rule-input parsing actually produces.
var issueArgPattern = regexp.MustCompile(`^[1-9][0-9]{0,6}$`)

// Options captures the inputs the legacy refresh-copilot-setup.ps1 script
// accepted on its CLI surface. All fields except RepoDirectory are
// optional.
type Options struct {
	// RepoDirectory is the absolute path of the target
	// terraform-provider-azurerm clone the AI-assisted files should be
	// installed into. Required; must already exist as a directory.
	RepoDirectory string

	// Issue, when non-empty, is the GitHub issue or PR number used to
	// create an `issue-<Issue>` working branch when the repository is
	// currently checked out on `main`. The value must match
	// ^[A-Za-z0-9._-]+$ so it cannot escape the git invocation.
	Issue string

	// Clean, when true, runs only the installer's clean phase against
	// RepoDirectory and skips the install phase. When false the
	// installer first cleans and then re-installs the latest payload —
	// matching the legacy script's default behaviour.
	Clean bool
}

// Refresher abstracts the refresh entry point so callers (and the
// gateway's WorkDirHook in particular) can substitute a stub in tests
// without standing up a real git clone or pwsh.exe / bash invocation.
type Refresher interface {
	Refresh(ctx context.Context, opts Options) error
}

// RefresherFunc is the function-shaped implementation of Refresher. It is
// the easiest way for tests to plug a one-liner stub into the hook.
type RefresherFunc func(ctx context.Context, opts Options) error

// Refresh implements Refresher.
func (f RefresherFunc) Refresh(ctx context.Context, opts Options) error {
	return f(ctx, opts)
}

// commandRunner is the indirection layer over os/exec used by Service.
// Tests substitute a recorder; production wiring uses execCommandRunner.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// Service implements Refresher with the production behaviour: clone the
// upstream repo into a per-call temp directory, bootstrap the installer
// into the user's profile, then run clean (and optionally install) against
// RepoDirectory. The platform-specific installer flavour (PowerShell vs.
// Bash) is picked at runtime based on the current GOOS.
//
// Construct via New; the zero value is not usable.
type Service struct {
	sourceRepoURL string
	runner        commandRunner
	osName        string
	home          func() (string, error)
	tempDirRoot   string // when "" os.MkdirTemp uses the OS default
	logger        *log.Logger

	// mu serialises Refresh calls. The bootstrap step copies the
	// installer into the shared per-user directory
	// ~/.terraform-azurerm-ai-installer/, which the subsequent clean /
	// install steps then read back; concurrent Refresh calls on different
	// cards would race on that shared path (partial writes, mismatched
	// installer / payload, or a clean targeting repo A using the
	// installer that was just bootstrapped by a Refresh for repo B).
	// Refreshes are infrequent (one per fresh azurerm work_dir), so a
	// process-wide mutex is the simplest correct answer.
	mu sync.Mutex
}

// Option tunes a Service constructed via New.
type Option func(*Service)

// WithLogger overrides the default log.Default() destination.
func WithLogger(l *log.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithSourceRepoURL overrides DefaultSourceRepoURL. Useful when an
// operator wants to mirror the upstream installer on an internal git
// server.
func WithSourceRepoURL(url string) Option {
	return func(s *Service) {
		if url != "" {
			s.sourceRepoURL = url
		}
	}
}

// New constructs a Service ready to run the production refresh path.
func New(opts ...Option) *Service {
	s := &Service{
		sourceRepoURL: DefaultSourceRepoURL,
		runner:        execCommandRunner,
		osName:        runtime.GOOS,
		home:          os.UserHomeDir,
		logger:        log.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Refresh re-implements the historic refresh-copilot-setup.ps1 flow as a
// Go-native sequence (the .ps1 wrapper is deprecated and is no longer
// invoked by the gateway). It:
//
//  1. Validates inputs.
//  2. If on `main` and Issue is non-empty, creates the `issue-<Issue>` branch.
//  3. Clones the upstream installer repo into a per-call temp directory.
//  4. Picks the platform-appropriate installer (.ps1 on Windows via pwsh,
//     .sh on macOS/Linux via bash) and runs -Bootstrap to copy it into
//     the user profile.
//  5. Runs the bootstrapped installer with -Clean against RepoDirectory.
//  6. Unless Options.Clean was set, runs the installer again without
//     -Clean to install the latest payload.
//  7. Best-effort RemoveAll of the temp directory.
//
// Refresh serialises concurrent calls via Service.mu — see the field
// comment for why. Errors are wrapped with the failing step so callers
// can log a useful reason without inspecting exec output blobs.
func (s *Service) Refresh(ctx context.Context, opts Options) error {
	if s == nil {
		return errors.New("aiassistedrefresh: nil Service")
	}
	if err := validateOptions(opts); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	logger := s.logger

	// Step 2: branch creation when on main and an issue number was supplied.
	if opts.Issue != "" {
		branch, err := s.currentBranch(ctx, opts.RepoDirectory)
		if err != nil {
			return fmt.Errorf("aiassistedrefresh: detect branch in %s: %w", opts.RepoDirectory, err)
		}
		if branch == "main" {
			target := "issue-" + opts.Issue
			logger.Printf("event=aiassistedrefresh_branch_create repo=%s branch=%s",
				opts.RepoDirectory, target)
			if out, err := s.runner(ctx, "git", "-C", opts.RepoDirectory, "checkout", "-b", target); err != nil {
				return fmt.Errorf("aiassistedrefresh: git checkout -b %s in %s: %w (output: %s)",
					target, opts.RepoDirectory, err, strings.TrimSpace(string(out)))
			}
		} else {
			logger.Printf("event=aiassistedrefresh_branch_skip repo=%s current=%s reason=not_on_main",
				opts.RepoDirectory, branch)
		}
	}

	// Step 3: clone upstream into a per-call temp dir.
	tempDir, err := os.MkdirTemp(s.tempDirRoot, "aiassistedrefresh-*")
	if err != nil {
		return fmt.Errorf("aiassistedrefresh: create temp dir: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(tempDir); rmErr != nil {
			logger.Printf("event=aiassistedrefresh_tempdir_cleanup_failed dir=%s err=%v",
				tempDir, rmErr)
		}
	}()
	logger.Printf("event=aiassistedrefresh_clone_started src=%s dir=%s",
		s.sourceRepoURL, tempDir)
	if out, err := s.runner(ctx, "git", "clone", "--depth", "1", s.sourceRepoURL, tempDir); err != nil {
		return fmt.Errorf("aiassistedrefresh: git clone %s: %w (output: %s)",
			s.sourceRepoURL, err, strings.TrimSpace(string(out)))
	}

	// Steps 4–6: pick the installer flavour and run bootstrap → clean →
	// (optional) install.
	dispatcher, err := s.installerForOS(tempDir)
	if err != nil {
		return err
	}

	logger.Printf("event=aiassistedrefresh_bootstrap_started os=%s installer=%s",
		s.osName, dispatcher.bootstrapScript)
	if out, err := s.runner(ctx, dispatcher.command, dispatcher.bootstrapArgs()...); err != nil {
		return fmt.Errorf("aiassistedrefresh: bootstrap installer (%s): %w (output: %s)",
			dispatcher.bootstrapScript, err, strings.TrimSpace(string(out)))
	}

	homeDir, err := s.home()
	if err != nil {
		return fmt.Errorf("aiassistedrefresh: resolve user home: %w", err)
	}
	bootstrapped := dispatcher.bootstrappedScript(homeDir)

	logger.Printf("event=aiassistedrefresh_clean_started repo=%s installer=%s",
		opts.RepoDirectory, bootstrapped)
	if out, err := s.runner(ctx, dispatcher.command, dispatcher.cleanArgs(bootstrapped, opts.RepoDirectory)...); err != nil {
		return fmt.Errorf("aiassistedrefresh: clean repo %s: %w (output: %s)",
			opts.RepoDirectory, err, strings.TrimSpace(string(out)))
	}

	if opts.Clean {
		logger.Printf("event=aiassistedrefresh_install_skipped reason=clean_only_requested repo=%s",
			opts.RepoDirectory)
		return nil
	}

	logger.Printf("event=aiassistedrefresh_install_started repo=%s installer=%s",
		opts.RepoDirectory, bootstrapped)
	if out, err := s.runner(ctx, dispatcher.command, dispatcher.installArgs(bootstrapped, opts.RepoDirectory)...); err != nil {
		return fmt.Errorf("aiassistedrefresh: install into %s: %w (output: %s)",
			opts.RepoDirectory, err, strings.TrimSpace(string(out)))
	}
	logger.Printf("event=aiassistedrefresh_done repo=%s clean_only=%t",
		opts.RepoDirectory, opts.Clean)
	return nil
}

// currentBranch returns the abbreviated HEAD ref of the repo at dir,
// trimmed of trailing whitespace. A "HEAD" return value means a detached
// HEAD — caller treats that the same as "not on main".
func (s *Service) currentBranch(ctx context.Context, dir string) (string, error) {
	out, err := s.runner(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w (output: %s)",
			err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// installerDispatcher captures the platform-specific shape of the
// installer invocation: which interpreter to call, where the script lives
// in the cloned repo, where it ends up after -Bootstrap, and what
// parameter casing the script expects.
type installerDispatcher struct {
	// command is the executable name handed to the runner ("pwsh" on
	// Windows, "bash" on Unix).
	command string

	// bootstrapScript is the absolute path of the installer entry point
	// inside the freshly-cloned upstream repo.
	bootstrapScript string

	// scriptName is the basename of the installer ("install-copilot-setup.ps1"
	// or "install-copilot-setup.sh"). Used to build the post-bootstrap
	// path under <home>/.terraform-azurerm-ai-installer/.
	scriptName string

	// repoDirFlag is the case-and-spelling-correct flag the installer
	// expects for the target repo argument. The PowerShell installer uses
	// `-RepoDirectory` (PascalCase); the Bash installer uses
	// `-repo-directory` (kebab-case lowercase).
	repoDirFlag string

	// cleanFlag is the case-correct flag for the clean operation
	// (`-Clean` vs `-clean`).
	cleanFlag string

	// bootstrapFlag is the case-correct flag for the bootstrap operation
	// (`-Bootstrap` vs `-bootstrap`).
	bootstrapFlag string
}

// bootstrapArgs is the argv passed to `command` to invoke the installer
// with -Bootstrap. PowerShell needs explicit `-NoProfile -File` plumbing
// so it doesn't load the operator's profile; bash gets the script path
// as its first positional argument.
func (d installerDispatcher) bootstrapArgs() []string {
	if d.command == "pwsh" {
		return []string{"-NoProfile", "-File", d.bootstrapScript, d.bootstrapFlag}
	}
	return []string{d.bootstrapScript, d.bootstrapFlag}
}

func (d installerDispatcher) cleanArgs(installedScript, repoDir string) []string {
	if d.command == "pwsh" {
		return []string{"-NoProfile", "-File", installedScript, d.repoDirFlag, repoDir, d.cleanFlag}
	}
	return []string{installedScript, d.repoDirFlag, repoDir, d.cleanFlag}
}

func (d installerDispatcher) installArgs(installedScript, repoDir string) []string {
	if d.command == "pwsh" {
		return []string{"-NoProfile", "-File", installedScript, d.repoDirFlag, repoDir}
	}
	return []string{installedScript, d.repoDirFlag, repoDir}
}

// bootstrappedScript returns the absolute path of the installer after it
// was copied into the user's profile by `-Bootstrap`.
func (d installerDispatcher) bootstrappedScript(home string) string {
	return filepath.Join(home, userProfileSubdir, d.scriptName)
}

// installerForOS picks the right installer flavour for the configured
// GOOS. The selection mirrors the upstream README: pwsh on windows, bash
// on everything else.
func (s *Service) installerForOS(repoRoot string) (installerDispatcher, error) {
	switch s.osName {
	case "windows":
		script := filepath.Join(repoRoot, installerSubdir, "install-copilot-setup.ps1")
		return installerDispatcher{
			command:         "pwsh",
			bootstrapScript: script,
			scriptName:      "install-copilot-setup.ps1",
			repoDirFlag:     "-RepoDirectory",
			cleanFlag:       "-Clean",
			bootstrapFlag:   "-Bootstrap",
		}, nil
	case "linux", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly":
		script := filepath.Join(repoRoot, installerSubdir, "install-copilot-setup.sh")
		return installerDispatcher{
			command:         "bash",
			bootstrapScript: script,
			scriptName:      "install-copilot-setup.sh",
			repoDirFlag:     "-repo-directory",
			cleanFlag:       "-clean",
			bootstrapFlag:   "-bootstrap",
		}, nil
	default:
		return installerDispatcher{}, fmt.Errorf("aiassistedrefresh: unsupported GOOS %q", s.osName)
	}
}

func validateOptions(opts Options) error {
	if strings.TrimSpace(opts.RepoDirectory) == "" {
		return errors.New("aiassistedrefresh: RepoDirectory is required")
	}
	st, err := os.Stat(opts.RepoDirectory)
	if err != nil {
		return fmt.Errorf("aiassistedrefresh: stat RepoDirectory %s: %w", opts.RepoDirectory, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("aiassistedrefresh: RepoDirectory %s is not a directory", opts.RepoDirectory)
	}
	if opts.Issue != "" && !issueArgPattern.MatchString(opts.Issue) {
		return fmt.Errorf("aiassistedrefresh: Issue %q does not match %s",
			opts.Issue, issueArgPattern.String())
	}
	return nil
}
