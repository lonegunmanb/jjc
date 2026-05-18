//go:build windows

package tunnel

import "os"

func terminateProcess(p *os.Process) error {
	return p.Kill()
}
