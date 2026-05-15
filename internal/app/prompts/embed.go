package prompts

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed MANAGER.md
var embeddedManager string

//go:embed WORKER.md
var embeddedWorker string

//go:embed BOOTSTRAP.md
var Bootstrap string

//go:embed IDENTITY.md
var Identity string

//go:embed TOOLS.md
var Tools string

//go:embed USER.md
var User string

// EmbeddedManager returns the MANAGER.md content baked into the binary.
func EmbeddedManager() string { return embeddedManager }

// EmbeddedWorker returns the WORKER.md content baked into the binary.
func EmbeddedWorker() string { return embeddedWorker }

// Defaults returns the embedded skeleton playbook contents keyed by
// their canonical bare basenames. The prompttmpl renderer materialises
// each entry into the per-process temp directory at startup as a
// fallback for operators who have not copied the skeleton files into
// their own --playbooks-dir; any user file with the same basename
// overrides the embedded copy.
func Defaults() map[string]string {
	return map[string]string{
		"BOOTSTRAP.md": Bootstrap,
		"IDENTITY.md":  Identity,
		"WORKER.md":    embeddedWorker,
		"TOOLS.md":     Tools,
		"USER.md":      User,
	}
}

// ResolveManager returns the MANAGER.md content. If a file named MANAGER.md
// exists next to the running executable, its content is used; otherwise the
// embedded copy is returned. The override path that was used (if any) is
// returned as the second value, empty string when the embedded copy is used.
func ResolveManager() (string, string) {
	return resolveOverride("MANAGER.md", embeddedManager)
}

// ResolveWorker is the WORKER.md analogue of ResolveManager.
func ResolveWorker() (string, string) {
	return resolveOverride("WORKER.md", embeddedWorker)
}

func resolveOverride(name, embedded string) (string, string) {
	exe, err := os.Executable()
	if err == nil {
		path := filepath.Join(filepath.Dir(exe), name)
		if data, err := os.ReadFile(path); err == nil {
			return string(data), path
		}
	}
	return embedded, ""
}
