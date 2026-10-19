//go:build !darwin

package xauth

// xauthCommand returns the xauth executable found on PATH.
func xauthCommand() string { return "xauth" }
