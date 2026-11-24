package monitor

import "errors"

// ErrTooManyHosts reports a demand above the MaxHosts cap. The cap is a
// domain rule, so the sentinel lives with the snapshot model rather than with
// any one application context that enforces it.
var ErrTooManyHosts = errors.New("monitor: more than 256 Hosts")
