//go:build !windows

package tunnel

import (
	"os"
	"syscall"
)

func terminateProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
