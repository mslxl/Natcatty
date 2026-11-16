//go:build !linux && !darwin && !windows

package processcwd

import "errors"

// ErrUnsupported reports a platform without a process working-directory
// lookup.
var ErrUnsupported = errors.New("processcwd: unsupported platform")

// Of reports that this platform has no lookup.
func Of(int) (string, error) { return "", ErrUnsupported }
