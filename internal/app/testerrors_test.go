package app

import "errors"

// errExec is used by tests to simulate a failed copilot invocation.
var errExec = errors.New("copilot exec failure")
