//go:build darwin

package xauth

import "os"

// xauthCommand prefers XQuartz's xauth, which is not on PATH by default.
func xauthCommand() string {
	if _, err := os.Stat("/opt/X11/bin/xauth"); err == nil {
		return "/opt/X11/bin/xauth"
	}
	return "xauth"
}
