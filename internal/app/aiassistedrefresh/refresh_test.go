package aiassistedrefresh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedCmd is one captured invocation from the fake commandRunner.
type recordedCmd struct {
	name string
	args []string
}

// fakeRunner records every commandRunner invocation and lets the test
// programme outputs / errors per command-name prefix. It also lets a test
// observe whether the temp directory passed to `git clone` still exists
// before the per-call `defer os.RemoveAll` fires.
type fakeRunner struct {
	t        *testing.T
	calls    []recordedCmd
	branch   string
	cloneErr error
	// programmable per-call hook
	hook func(call recordedCmd) ([]byte, error)
}

func (f *fakeRunner) run() commandRunner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		call := recordedCmd{name: name, args: append([]string(nil), args...)}
		f.calls = append(f.calls, call)
		// Default replies that mirror the production interaction shape.
		switch {
		case name == "git" && len(args) >= 4 && args[0] == "-C" && args[2] == "rev-parse":
			return []byte(f.branch + "\n"), nil
		case name == "git" && len(args) >= 1 && args[0] == "clone":
			if f.cloneErr != nil {
				return []byte("clone failed"), f.cloneErr
			}
			// Materialise the destination path so the test can later
			// confirm that defer-cleanup removed it.
			if dest := args[len(args)-1]; dest != "" {
				if err := os.MkdirAll(filepath.Join(dest, installerSubdir), 0o755); err != nil {
					f.t.Fatalf("fakeRunner: prep clone destination %s: %v", dest, err)
				}
			}
			return nil, nil
		}
		if f.hook != nil {
			return f.hook(call)
		}
		return nil, nil
	}
}

func (f *fakeRunner) commandsByName(name string) []recordedCmd {
	var out []recordedCmd
	for _, c := range f.calls {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// stubHomeIn returns a UserHomeDir replacement that always points at dir.
func stubHomeIn(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func mustTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func TestRefreshValidatesRepoDirectory(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "empty",
			opts: Options{},
			want: "RepoDirectory is required",
		},
		{
			name: "missing",
			opts: Options{RepoDirectory: filepath.Join(t.TempDir(), "does-not-exist")},
			want: "stat RepoDirectory",
		},
	}
	svc := New(WithLogger(discardLogger()))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Refresh(context.Background(), tc.opts)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestRefreshRejectsRepoDirectoryThatIsAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	svc := New(WithLogger(discardLogger()))
	err := svc.Refresh(context.Background(), Options{RepoDirectory: file})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("expected not-a-directory error, got %v", err)
	}
}

func TestRefreshValidatesIssueShape(t *testing.T) {
	svc := New(WithLogger(discardLogger()))
	err := svc.Refresh(context.Background(), Options{
		RepoDirectory: mustTempRepo(t),
		Issue:         "12345; rm -rf /",
	})
	if err == nil || !strings.Contains(err.Error(), "Issue") {
		t.Fatalf("expected issue-validation error, got %v", err)
	}
}

func TestRefreshOnMainCreatesIssueBranch(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	rec := &fakeRunner{t: t, branch: "main"}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(t.TempDir()),
	)

	if err := svc.Refresh(context.Background(), Options{
		RepoDirectory: repo,
		Issue:         "9999",
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Find the `git checkout` call and assert exact shape.
	var gotCheckout bool
	for _, c := range rec.commandsByName("git") {
		if len(c.args) >= 5 && c.args[0] == "-C" && c.args[2] == "checkout" && c.args[3] == "-b" && c.args[4] == "issue-9999" {
			gotCheckout = true
			break
		}
	}
	if !gotCheckout {
		t.Fatalf("expected `git -C <repo> checkout -b issue-9999`, got calls=%+v", rec.calls)
	}
}

func TestRefreshSkipsBranchCreationWhenNotOnMain(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	rec := &fakeRunner{t: t, branch: "issue-9999"}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(t.TempDir()),
	)

	if err := svc.Refresh(context.Background(), Options{
		RepoDirectory: repo,
		Issue:         "9999",
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	for _, c := range rec.commandsByName("git") {
		if len(c.args) >= 3 && c.args[2] == "checkout" {
			t.Fatalf("checkout must not run when not on main; got %+v", c)
		}
	}
}

func TestRefreshSkipsBranchCreationWhenNoIssue(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	rec := &fakeRunner{t: t, branch: "main"}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(t.TempDir()),
	)

	if err := svc.Refresh(context.Background(), Options{RepoDirectory: repo}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for _, c := range rec.commandsByName("git") {
		if len(c.args) >= 3 && c.args[2] == "checkout" {
			t.Fatalf("checkout must not run when Issue is empty; got %+v", c)
		}
		if len(c.args) >= 3 && c.args[2] == "rev-parse" {
			t.Fatalf("rev-parse must not run when Issue is empty; got %+v", c)
		}
	}
}

func TestRefreshLinuxRunsBootstrapCleanInstall(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	tempRoot := t.TempDir()
	rec := &fakeRunner{t: t, branch: "issue-9999"}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(tempRoot),
	)
	if err := svc.Refresh(context.Background(), Options{RepoDirectory: repo}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	bashCalls := rec.commandsByName("bash")
	if len(bashCalls) != 3 {
		t.Fatalf("expected 3 bash invocations (bootstrap, clean, install), got %d: %+v", len(bashCalls), bashCalls)
	}

	// Bootstrap: bash <tempDir>/installer/install-copilot-setup.sh -bootstrap
	if !strings.HasSuffix(bashCalls[0].args[0], filepath.Join(installerSubdir, "install-copilot-setup.sh")) {
		t.Errorf("bootstrap script name unexpected: %q", bashCalls[0].args[0])
	}
	if bashCalls[0].args[1] != "-bootstrap" {
		t.Errorf("bootstrap flag: got %q want %q", bashCalls[0].args[1], "-bootstrap")
	}

	// Clean: bash <home>/.terraform-azurerm-ai-installer/install-copilot-setup.sh -repo-directory <repo> -clean
	wantInstalled := filepath.Join(home, userProfileSubdir, "install-copilot-setup.sh")
	wantClean := []string{wantInstalled, "-repo-directory", repo, "-clean"}
	if !equalStringSlice(bashCalls[1].args, wantClean) {
		t.Errorf("clean args:\n  got  %#v\n  want %#v", bashCalls[1].args, wantClean)
	}

	// Install: same minus -clean.
	wantInstall := []string{wantInstalled, "-repo-directory", repo}
	if !equalStringSlice(bashCalls[2].args, wantInstall) {
		t.Errorf("install args:\n  got  %#v\n  want %#v", bashCalls[2].args, wantInstall)
	}
}

func TestRefreshWindowsUsesPwshAndPascalCaseFlags(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	tempRoot := t.TempDir()
	rec := &fakeRunner{t: t, branch: "issue-9999"}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("windows"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(tempRoot),
	)
	if err := svc.Refresh(context.Background(), Options{RepoDirectory: repo}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	pwshCalls := rec.commandsByName("pwsh")
	if len(pwshCalls) != 3 {
		t.Fatalf("expected 3 pwsh invocations, got %d: %+v", len(pwshCalls), pwshCalls)
	}

	// All pwsh invocations must start with -NoProfile -File <script> ...
	for i, call := range pwshCalls {
		if call.args[0] != "-NoProfile" || call.args[1] != "-File" {
			t.Errorf("pwsh call %d does not start with `-NoProfile -File`: %+v", i, call.args)
		}
	}

	// Bootstrap script lives inside the cloned repo.
	if !strings.HasSuffix(pwshCalls[0].args[2], filepath.Join(installerSubdir, "install-copilot-setup.ps1")) {
		t.Errorf("bootstrap script name unexpected: %q", pwshCalls[0].args[2])
	}
	if pwshCalls[0].args[3] != "-Bootstrap" {
		t.Errorf("bootstrap flag: got %q want %q", pwshCalls[0].args[3], "-Bootstrap")
	}

	wantInstalled := filepath.Join(home, userProfileSubdir, "install-copilot-setup.ps1")
	wantClean := []string{"-NoProfile", "-File", wantInstalled, "-RepoDirectory", repo, "-Clean"}
	if !equalStringSlice(pwshCalls[1].args, wantClean) {
		t.Errorf("clean args:\n  got  %#v\n  want %#v", pwshCalls[1].args, wantClean)
	}
	wantInstall := []string{"-NoProfile", "-File", wantInstalled, "-RepoDirectory", repo}
	if !equalStringSlice(pwshCalls[2].args, wantInstall) {
		t.Errorf("install args:\n  got  %#v\n  want %#v", pwshCalls[2].args, wantInstall)
	}
}

func TestRefreshCleanOnlySkipsInstallStep(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	rec := &fakeRunner{t: t, branch: "issue-9999"}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(t.TempDir()),
	)
	if err := svc.Refresh(context.Background(), Options{RepoDirectory: repo, Clean: true}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	bashCalls := rec.commandsByName("bash")
	if len(bashCalls) != 2 {
		t.Fatalf("expected 2 bash invocations (bootstrap, clean) when Clean=true, got %d: %+v",
			len(bashCalls), bashCalls)
	}
	for _, call := range bashCalls {
		for _, a := range call.args {
			if a == "-clean" {
				return // happy path: at least one of the two bashes carries -clean
			}
		}
	}
	t.Fatalf("expected at least one bash invocation to carry -clean; got %+v", bashCalls)
}

func TestRefreshRemovesTempDirOnSuccess(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	tempRoot := t.TempDir()
	rec := &fakeRunner{t: t, branch: "issue-9999"}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(tempRoot),
	)
	if err := svc.Refresh(context.Background(), Options{RepoDirectory: repo}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatalf("read tempRoot: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("tempRoot must be empty after success, got entries=%v", names(entries))
	}
}

func TestRefreshRemovesTempDirOnFailure(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	tempRoot := t.TempDir()
	rec := &fakeRunner{t: t, branch: "issue-9999"}
	rec.hook = func(call recordedCmd) ([]byte, error) {
		// Inject a failure on the bootstrap (first non-git, non-prep call).
		if call.name == "bash" {
			return []byte("bootstrap exploded"), errors.New("exit 1")
		}
		return nil, nil
	}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(tempRoot),
	)
	err := svc.Refresh(context.Background(), Options{RepoDirectory: repo})
	if err == nil {
		t.Fatal("expected error from bootstrap failure")
	}
	entries, rerr := os.ReadDir(tempRoot)
	if rerr != nil {
		t.Fatalf("read tempRoot: %v", rerr)
	}
	if len(entries) != 0 {
		t.Fatalf("tempRoot must be cleaned even on failure, got entries=%v", names(entries))
	}
}

func TestRefreshSurfacesCloneError(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	rec := &fakeRunner{t: t, branch: "issue-9999", cloneErr: errors.New("network down")}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(t.TempDir()),
	)
	err := svc.Refresh(context.Background(), Options{RepoDirectory: repo})
	if err == nil || !strings.Contains(err.Error(), "git clone") {
		t.Fatalf("expected git-clone error, got %v", err)
	}
	// The bootstrap step must not have run.
	if len(rec.commandsByName("bash")) != 0 {
		t.Fatalf("bash must not be invoked when clone fails; got %+v", rec.calls)
	}
}

func TestRefreshSurfacesCheckoutError(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	rec := &fakeRunner{t: t, branch: "main"}
	rec.hook = func(call recordedCmd) ([]byte, error) {
		if call.name == "git" && len(call.args) >= 3 && call.args[2] == "checkout" {
			return []byte("ref already exists"), errors.New("exit 128")
		}
		return nil, nil
	}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(t.TempDir()),
	)
	err := svc.Refresh(context.Background(), Options{RepoDirectory: repo, Issue: "1"})
	if err == nil || !strings.Contains(err.Error(), "checkout") {
		t.Fatalf("expected checkout error, got %v", err)
	}
}

func TestRefreshUnsupportedOSReturnsError(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()
	rec := &fakeRunner{t: t, branch: "issue-1"}
	svc := New(
		WithLogger(discardLogger()),
		withRunner(rec.run()),
		withOS("plan9"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(t.TempDir()),
	)
	err := svc.Refresh(context.Background(), Options{RepoDirectory: repo})
	if err == nil || !strings.Contains(err.Error(), "unsupported GOOS") {
		t.Fatalf("expected unsupported-OS error, got %v", err)
	}
}

func TestRefresherFuncSatisfiesInterface(t *testing.T) {
	called := false
	var r Refresher = RefresherFunc(func(_ context.Context, _ Options) error {
		called = true
		return nil
	})
	if err := r.Refresh(context.Background(), Options{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !called {
		t.Fatalf("RefresherFunc did not invoke wrapped function")
	}
}

func TestNewDefaultsAreSane(t *testing.T) {
	s := New()
	if s.sourceRepoURL != DefaultSourceRepoURL {
		t.Errorf("sourceRepoURL: got %q want %q", s.sourceRepoURL, DefaultSourceRepoURL)
	}
	if s.osName != runtime.GOOS {
		t.Errorf("osName: got %q want %q", s.osName, runtime.GOOS)
	}
	if s.runner == nil {
		t.Error("runner must default to execCommandRunner")
	}
	if s.home == nil {
		t.Error("home must default to os.UserHomeDir")
	}
	if s.logger == nil {
		t.Error("logger must default to sysevent.Default()")
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s(%v)", e.Name(), e.IsDir()))
	}
	return out
}

// TestRefreshSerialisesConcurrentCalls pins the contract that Service
// serialises Refresh invocations: the bootstrap step writes into the
// shared per-user installer directory, so two parallel Refresh calls
// must never overlap. We assert that by counting the maximum number of
// in-flight runner calls observed across two concurrent Refresh
// goroutines — with the mutex it must be 1.
func TestRefreshSerialisesConcurrentCalls(t *testing.T) {
	repo := mustTempRepo(t)
	home := t.TempDir()

	var (
		mu       sync.Mutex
		inFlight int
		maxSeen  int
	)
	tracker := func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		mu.Unlock()
		// Hold the call long enough that a parallel goroutine would
		// definitely overlap it if no mutex were in place.
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()

		// Mirror the production-shape replies the real fakeRunner gives.
		switch {
		case name == "git" && len(args) >= 4 && args[0] == "-C" && args[2] == "rev-parse":
			return []byte("issue-1\n"), nil
		case name == "git" && len(args) >= 1 && args[0] == "clone":
			dest := args[len(args)-1]
			if err := os.MkdirAll(filepath.Join(dest, installerSubdir), 0o755); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, nil
	}

	svc := New(
		WithLogger(discardLogger()),
		withRunner(tracker),
		withOS("linux"),
		withHome(stubHomeIn(home)),
		withTempDirRoot(t.TempDir()),
	)

	const goroutines = 4
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if err := svc.Refresh(context.Background(), Options{RepoDirectory: repo}); err != nil {
				t.Errorf("Refresh: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxSeen != 1 {
		t.Fatalf("Refresh must serialise concurrent calls; observed maxInFlight=%d (want 1)", maxSeen)
	}
}
