// Package prompts holds the five skeleton playbook fragments that ship
// embedded inside the gateway binary. They serve as defaults the
// prompttmpl renderer materialises into its per-process temp directory
// at startup so the gateway runs even when --config-src ships no
// override; any same-name .md inside the operator's --config-src
// bundle overrides them.
//
// The package surface is intentionally tiny: Defaults() returns the
// basename → content map the renderer consumes, and that is it. The
// per-fragment package-level vars are exported so a test or a debug
// command can poke at one file in isolation.
package prompts

import (
	_ "embed"
)

//go:embed BOOTSTRAP.md
var Bootstrap string

//go:embed IDENTITY.md
var Identity string

//go:embed WORKER.md
var Worker string

//go:embed TOOLS.md
var Tools string

//go:embed USER.md
var User string

// Defaults returns the embedded skeleton playbook contents keyed by
// their canonical bare basenames. The prompttmpl renderer materialises
// each entry into the per-process temp directory at startup as a
// fallback for operators who have not copied the skeleton files into
// their own --config-src bundle; any user file with the same basename
// overrides the embedded copy.
func Defaults() map[string]string {
	return map[string]string{
		"BOOTSTRAP.md": Bootstrap,
		"IDENTITY.md":  Identity,
		"WORKER.md":    Worker,
		"TOOLS.md":     Tools,
		"USER.md":      User,
	}
}

// EmbeddedWorker returns the WORKER.md content baked into the binary.
// Kept as a separately exported accessor so the runner's skeleton-prompt
// fallback path can read it without copying the whole map.
func EmbeddedWorker() string { return Worker }
