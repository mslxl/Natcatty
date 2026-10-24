package app

import (
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// forwardTerminalCwd reports a shell's OSC 7 working directory to the SFTP
// surface. A local Session steers a local pane. A remote Session is attributed
// to its Host, and one whose Host cannot be resolved is ignored rather than
// steering an arbitrary remote pane. The surface decides whether follow-cwd is
// on, so forwarding costs nothing when the setting is off.
func forwardTerminalCwd(sessions *appsession.Service, transfer *transferstate.State, snapshot appsession.Snapshot) {
	if snapshot.CWD == "" || sessions == nil || transfer == nil {
		return
	}
	local := snapshot.Kind == appsession.KindLocal
	hostID := model.HostID("")
	if !local {
		id, _, _, ok := sessions.SessionInfo(snapshot.ID)
		if !ok {
			return
		}
		hostID = id
	}
	if local || hostID != "" {
		transfer.NoteTerminalCwd(snapshot.ID, hostID, local, snapshot.CWD)
	}
}
