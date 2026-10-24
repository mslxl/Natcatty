package state

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// sftpSettingsOption injects one SFTP settings snapshot into the surface.
func sftpSettingsOption(edit func(*domainpreferences.SFTPEditor)) Option {
	settings := domainpreferences.Defaults().SFTPEditor
	if edit != nil {
		edit(&settings)
	}
	return WithPreferences(func() domainpreferences.SFTPEditor { return settings })
}

func TestAutoSyncMirrorsANavigationOnOneBacking(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.AutoSync = true
	}))
	f.state.OpenLocal(Right)
	right := f.waitLoaded(t, Right)
	if right.Path != "/home/me" {
		t.Fatalf("right pane starts at %q, want /home/me", right.Path)
	}

	f.state.Navigate(Left, "/home/me/folder")
	settle(t, f.state, "both panes to enter folder", func() bool {
		return f.state.Tab(Left).Path == "/home/me/folder" && !f.state.Tab(Left).Loading &&
			right.Path == "/home/me/folder" && !right.Loading
	})
}

func TestAutoSyncFollowsOneStepBetweenDifferentTrees(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.AutoSync = true
	}))
	f.remote.mkdirs("/home", "/home/me", "/home/me/folder")
	f.state.OpenRemote(Right, "h1", "Test")
	right := f.waitLoaded(t, Right)
	f.state.Navigate(Right, "/home/me")
	settle(t, f.state, "the remote pane to enter its mirror", func() bool {
		return right.Path == "/home/me" && !right.Loading
	})

	f.state.Navigate(Left, "/home/me/folder")
	settle(t, f.state, "the left pane to enter folder", func() bool {
		return f.state.Tab(Left).Path == "/home/me/folder" && !f.state.Tab(Left).Loading
	})
	settle(t, f.state, "the remote pane to follow folder", func() bool {
		return right.Path == "/home/me/folder" && !right.Loading
	})

	f.state.GoParent(Left)
	settle(t, f.state, "the left pane to go up", func() bool {
		return f.state.Tab(Left).Path == "/home/me" && !f.state.Tab(Left).Loading
	})
	settle(t, f.state, "the remote pane to go up", func() bool {
		return right.Path == "/home/me" && !right.Loading
	})
}

func TestAutoSyncOffLeavesTheOtherPaneAlone(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.AutoSync = false
	}))
	f.state.OpenLocal(Right)
	right := f.waitLoaded(t, Right)

	f.state.Navigate(Left, "/home/me/folder")
	settle(t, f.state, "the left pane to enter folder", func() bool {
		return f.state.Tab(Left).Path == "/home/me/folder" && !f.state.Tab(Left).Loading
	})
	f.state.Flush()
	if right.Path != "/home/me" {
		t.Fatalf("auto-sync moved the other pane while off: %q", right.Path)
	}
}

func TestAutoSyncDoesNotMirrorAnUnalignedPane(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.AutoSync = true
	}))
	f.remote.mkdirs("/srv", "/srv/folder")
	f.state.OpenRemote(Right, "h1", "Test")
	right := f.waitLoaded(t, Right)
	f.state.Navigate(Right, "/srv")
	settle(t, f.state, "the remote pane to enter srv", func() bool {
		return right.Path == "/srv" && !right.Loading
	})

	f.state.Navigate(Left, "/home/me/folder")
	settle(t, f.state, "the left pane to enter folder", func() bool {
		return f.state.Tab(Left).Path == "/home/me/folder" && !f.state.Tab(Left).Loading
	})
	f.state.Flush()
	if right.Path != "/srv" {
		t.Fatalf("an unaligned pane followed to %q", right.Path)
	}
}

func TestFollowCwdNavigatesTheLocalPane(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.FollowTerminalCwd = true
	}))
	f.local.mkdirs("/home/me/sub", "/home/me/other")
	left := f.state.Tab(Left)

	f.state.NoteTerminalCwd("session-1", "", true, "/home/me/sub")
	settle(t, f.state, "the local pane to follow the shell", func() bool {
		return left.Path == "/home/me/sub" && !left.Loading
	})

	// The user moves away; the same report must not pull the pane back.
	f.state.Navigate(Left, "/home/me")
	settle(t, f.state, "the user to leave the followed directory", func() bool {
		return left.Path == "/home/me" && !left.Loading
	})
	f.state.NoteTerminalCwd("session-1", "", true, "/home/me/sub")
	f.state.Flush()
	if left.Path != "/home/me" {
		t.Fatalf("a repeated cwd overrode the user's navigation: %q", left.Path)
	}

	f.state.NoteTerminalCwd("session-1", "", true, "/home/me/other")
	settle(t, f.state, "the local pane to follow a new cwd", func() bool {
		return left.Path == "/home/me/other" && !left.Loading
	})
}

func TestFollowCwdOffLeavesThePaneAlone(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.FollowTerminalCwd = false
	}))
	f.local.mkdirs("/home/me/sub")

	f.state.NoteTerminalCwd("session-1", "", true, "/home/me/sub")
	f.state.Flush()
	if f.state.Tab(Left).Path != "/home/me" {
		t.Fatalf("follow-cwd moved the pane while off: %q", f.state.Tab(Left).Path)
	}
}

// TestFollowCwdOffDoesNotQueueAReport proves a report costs no UI work while
// the setting is off. The app forwards every OSC 7, including when the SFTP
// view is not mounted to drain the queue, so queueing there would grow with
// every prompt.
func TestFollowCwdOffDoesNotQueueAReport(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(nil))
	f.state.Flush()
	f.state.NoteTerminalCwd("session-1", "", true, "/home/me/sub")
	f.state.queueMu.Lock()
	queued := len(f.state.queue)
	f.state.queueMu.Unlock()
	if queued != 0 {
		t.Fatalf("follow-cwd off queued %d UI closures", queued)
	}
}

func TestFollowCwdMatchesTheHostAndTheFocusedSession(t *testing.T) {
	f := newFixtureOptions(t, nil, nil,
		sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
			settings.FollowTerminalCwd = true
		}),
		WithFocusedSession(func() model.SessionID { return "focused" }),
	)
	f.remote.mkdirs("/srv")
	f.state.OpenRemote(Right, "h1", "Test")
	right := f.waitLoaded(t, Right)

	f.state.NoteTerminalCwd("focused", "h2", false, "/srv")
	f.state.Flush()
	if right.Path != "/" {
		t.Fatalf("a different Host steered the pane to %q", right.Path)
	}
	f.state.NoteTerminalCwd("background", "h1", false, "/srv")
	f.state.Flush()
	if right.Path != "/" {
		t.Fatalf("an unfocused Session steered the pane to %q", right.Path)
	}
	f.state.NoteTerminalCwd("focused", "h1", false, "/srv")
	settle(t, f.state, "the remote pane to follow its Host", func() bool {
		return right.Path == "/srv" && !right.Loading
	})
}

func TestFollowCwdIgnoresAMissingDirectory(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.FollowTerminalCwd = true
	}))
	left := f.state.Tab(Left)

	f.state.NoteTerminalCwd("session-1", "", true, "/not/a/directory")
	settle(t, f.state, "the follow check to reject the path", func() bool {
		return left.followMiss == "/not/a/directory"
	})
	if left.Path != "/home/me" {
		t.Fatalf("follow-cwd entered a missing directory: %q", left.Path)
	}
	if left.Problem != nil {
		t.Fatalf("follow-cwd reported a missing path as a pane problem: %v", left.Problem)
	}
}

func TestCompressedUploadSettingReachesTheCopyEngine(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.UseCompressedUpload = true
	}))
	f.state.OpenRemote(Right, "h1", "Test")
	right := f.waitLoaded(t, Right)

	selectOne(t, f.state, Left, "folder")
	f.state.Transfer(Left)
	waitIdle(t, f.state)

	dest, ok := right.FS.(*fakeFS)
	if !ok {
		t.Fatalf("the remote pane uses %T, want the fixture filesystem", right.FS)
	}
	if calls := dest.archiveCalls(); calls != 1 {
		t.Fatalf("compressed attempts = %d, want 1 when the setting is on", calls)
	}
	if !f.remote.has("/folder/inside.txt") {
		t.Fatal("the upload did not fall back to copy the folder after the extractor refused")
	}
}

func TestCompressedUploadOffCopiesFileByFile(t *testing.T) {
	f := newFixtureOptions(t, nil, nil, sftpSettingsOption(func(settings *domainpreferences.SFTPEditor) {
		settings.UseCompressedUpload = false
	}))
	f.state.OpenRemote(Right, "h1", "Test")
	right := f.waitLoaded(t, Right)

	selectOne(t, f.state, Left, "folder")
	f.state.Transfer(Left)
	waitIdle(t, f.state)

	dest := right.FS.(*fakeFS)
	if calls := dest.archiveCalls(); calls != 0 {
		t.Fatalf("compressed attempts = %d, want 0 when the setting is off", calls)
	}
	if !f.remote.has("/folder/inside.txt") {
		t.Fatal("the upload did not copy the folder")
	}
}
