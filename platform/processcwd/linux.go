//go:build linux

package processcwd

import (
	"fmt"
	"os"
)

// Of returns the working directory of pid. Linux exposes it as the target of
// /proc/<pid>/cwd, which follows the process even across a shell builtin cd.
func Of(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrInvalid
	}
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return "", err
	}
	if !valid(cwd) {
		return "", fmt.Errorf("processcwd: %q is not an absolute directory", cwd)
	}
	return cwd, nil
}
