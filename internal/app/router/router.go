// Package router owns the HCL-driven event-routing engine that replaces
// the hard-coded switch in internal/app/routing.go's Route().
//
// At startup the gateway parses a list of `route {}` blocks out of
// router.hcl (the same file the kanban package reads). Each block has
// a `when` expression (kept as hcl.Expression so file/line/column
// diagnostics survive), a `do` action ("drop" | "dispatch" |
// "terminate" | "notify_departure") and a `reason` string. Match
// semantics are: top-down, first `when == true` wins — exactly the
// switch ordering routing.go encodes today.
//
// The engine only owns the `route {}` blocks. The sibling `kanban {}`
// block continues to live in internal/app/kanban; `rule {}` blocks
// will live in their own package once issue lonegunmanb/trello-copilot#7
// lands. The HCL decoder here uses PartialContent so the same
// router.hcl can carry all three block kinds.
//
// Failure modes:
//
//   - A `when` expression that errors at evaluation time is skipped
//     with a structured log line (`event=route_when_eval_error`) and
//     the engine continues to the next route. The dispatch is never
//     aborted because of one bad rule.
//
//   - If no route matches (the user dropped the catch-all
//     `route "unsupported_action_type" { when = true ... }` from their
//     config) the engine logs `event=router_no_route_matched` and
//     returns the drop decision.
//
// No hot reload: the engine is constructed once at startup.
package router

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// Config is the decoded shape of the route side of router.hcl. Other
// top-level blocks (kanban, rule, ...) are ignored at decode time so
// the same file can carry every router layer.
type Config struct {
	Routes []Route `hcl:"route,block"`
}

// Route is one declarative `route "<name>" {}` block. The `when`
// attribute is intentionally left as hcl.Expression — decoding to a
// string would drop the file/line/column diagnostics hcl carries, and
// the engine evaluates `when` against a per-event context anyway.
//
// `do` must be one of the four RouteAction tokens documented in
// internal/app/routing.go: "drop", "dispatch", "terminate",
// "notify_departure". The engine validates this at load time so a
// typo fails fast instead of at the first matching event.
//
// `reason` is logged verbatim in the gateway dispatch line and is
// useful for after-the-fact "why was this event dropped?" forensics.
type Route struct {
	Name   string         `hcl:"name,label"`
	When   hcl.Expression `hcl:"when"`
	Do     string         `hcl:"do"`
	Reason string         `hcl:"reason"`

	// DeclRange is the source range of the route block. Captured at
	// decode time so diagnostics can point back at the offending rule
	// even after the file has been closed.
	DeclRange hcl.Range
}

// Valid `do` tokens. Kept here (rather than importing routing.go's
// RouteAction) so the router package has no cyclic dependency on
// internal/app.
const (
	ActionDrop            = "drop"
	ActionDispatch        = "dispatch"
	ActionTerminate       = "terminate"
	ActionNotifyDeparture = "notify_departure"
)

func isValidAction(do string) bool {
	switch do {
	case ActionDrop, ActionDispatch, ActionTerminate, ActionNotifyDeparture:
		return true
	}
	return false
}

// LoadConfig reads router.hcl at path and decodes its `route {}`
// blocks. Other top-level blocks are tolerated. The empty-path and
// missing-file cases return a wrapped error suitable for the gateway
// startup log.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, errors.New("router: hcl path is empty")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("router: read %s: %w", path, err)
	}
	return DecodeConfig(src, filepath.Base(path))
}

// DecodeConfig is the bytes-in/Config-out core LoadConfig wraps. It is
// exported so tests can avoid touching the filesystem.
//
// Only the `route` block schema is required; every other top-level
// block (kanban, rule, ...) is ignored via PartialContent so the same
// router.hcl can carry all three layers.
func DecodeConfig(src []byte, filename string) (Config, error) {
	parser := hclparse.NewParser()
	f, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return Config{}, fmt.Errorf("router: parse %s: %s", filename, diags.Error())
	}
	schema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "route", LabelNames: []string{"name"}},
		},
	}
	content, _, diags := f.Body.PartialContent(schema)
	if diags.HasErrors() {
		return Config{}, fmt.Errorf("router: scan %s: %s", filename, diags.Error())
	}

	cfg := Config{}
	seen := map[string]hcl.Range{}
	for _, b := range content.Blocks {
		if b.Type != "route" {
			continue
		}
		var r Route
		if d := gohcl.DecodeBody(b.Body, nil, &r); d.HasErrors() {
			return Config{}, fmt.Errorf("router: decode route %q in %s: %s",
				b.Labels[0], filename, d.Error())
		}
		r.Name = b.Labels[0]
		r.DeclRange = b.DefRange
		if r.Name == "" {
			return Config{}, fmt.Errorf("router: route at %s has empty label", b.DefRange)
		}
		if prev, dup := seen[r.Name]; dup {
			return Config{}, fmt.Errorf("router: duplicate route %q at %s (first declared at %s)",
				r.Name, b.DefRange, prev)
		}
		seen[r.Name] = b.DefRange
		if !isValidAction(r.Do) {
			return Config{}, fmt.Errorf("router: route %q at %s has invalid do=%q (must be one of drop, dispatch, terminate, notify_departure)",
				r.Name, b.DefRange, r.Do)
		}
		cfg.Routes = append(cfg.Routes, r)
	}
	if len(cfg.Routes) == 0 {
		return Config{}, fmt.Errorf("router: %s contains no `route {}` blocks", filename)
	}
	return cfg, nil
}
