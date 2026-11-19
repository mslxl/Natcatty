//go:build darwin

package processcwd

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// lsofTimeout bounds one probe. lsof normally answers immediately; a wedged
// process table must not stall the caller.
const lsofTimeout = 2 * time.Second

// Of returns the working directory of pid. macOS has no procfs, so lsof is
// asked for the process's cwd descriptor. The system path is tried first
// because a shell's PATH need not include /usr/sbin.
func Of(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrInvalid
	}
	var lastErr error
	for _, binary := range []string{"/usr/sbin/lsof", "lsof"} {
		path, err := exec.LookPath(binary)
		if err != nil {
			lastErr = err
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), lsofTimeout)
		output, err := exec.CommandContext(ctx, path, "-a", "-p", fmt.Sprint(pid), "-d", "cwd", "-Fn").Output()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if cwd := parseLsofOutput(string(output)); cwd != "" {
			return cwd, nil
		}
		lastErr = fmt.Errorf("processcwd: lsof reported no cwd for %d", pid)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("processcwd: lsof is unavailable")
	}
	return "", lastErr
}
