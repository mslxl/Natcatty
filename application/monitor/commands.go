package monitor

import "github.com/mslxl/natcatty/application/hoststats"

// The canonical Host Stats Sample command and separate Linux disk probe live
// in application/hoststats, shared with the System Tool. These wrappers keep
// the monitoring service's seam stable.

// statsCommand returns the remote command for one stats sample.
func statsCommand() string { return hoststats.StatsCommand() }

// diskCommand returns the separate Linux disk probe. macOS reports DISKS in
// the stats reply, so no second command is needed there.
func diskCommand() string { return hoststats.DiskCommand() }
