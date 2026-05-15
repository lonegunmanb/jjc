package aiassistedrefresh

import (
	"io"
	"log"
)

// discardLogger returns a *log.Logger that throws every line away. Tests
// that don't want the production logger spamming `go test` output use it.
// Lives in a _test.go file so it is excluded from the production binary.
func discardLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// withRunner injects a fake command runner. Test-only seam — kept under
// _test.go so only in-package tests can use it.
func withRunner(r commandRunner) Option {
	return func(s *Service) {
		if r != nil {
			s.runner = r
		}
	}
}

// withOS overrides runtime.GOOS for the OS-detection branch. Test-only.
func withOS(name string) Option {
	return func(s *Service) {
		if name != "" {
			s.osName = name
		}
	}
}

// withHome overrides os.UserHomeDir. Test-only — keeps the bootstrap step
// from writing into the developer's real ~/.terraform-azurerm-ai-installer.
func withHome(fn func() (string, error)) Option {
	return func(s *Service) {
		if fn != nil {
			s.home = fn
		}
	}
}

// withTempDirRoot overrides the directory under which the per-call temp
// clone is created. Test-only — lets tests assert that the directory is
// removed after the call returns.
func withTempDirRoot(root string) Option {
	return func(s *Service) {
		s.tempDirRoot = root
	}
}
