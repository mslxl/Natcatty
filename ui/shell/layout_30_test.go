package shell

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	transferui "github.com/mslxl/natcatty/ui/transfer"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const (
	sftpLayoutTimeout = 5 * time.Second
	// sftpSurfaceEdgeGutter is the least distance any SFTP control may keep
	// from the edge of the surface's box inside the shell's content column.
	// The surface itself insets by surfaceGutter; the tolerance below it
	// leaves room for a pane's border and a text element's own inset without
	// letting anything touch the edge. The shell's content column is only
	// 456 DIPs wide at a 720x480 window, so a control that reaches the edge
	// also reaches the rounded window frame: ticket 30's acceptance saw the
	// header's Connect button and the operation bar's Cancel there.
	sftpSurfaceEdgeGutter float32 = 12
)

// sftpLayoutFS is a local file system for the layout fixture. The real
// adapters are not importable from the UI layer, so the fixture serves a
// temporary directory through the same interface the SFTP surface uses.
type sftpLayoutFS struct {
	root    string
	backing string
}

func (f sftpLayoutFS) Root(context.Context) (string, error) { return f.root, nil }

func (f sftpLayoutFS) List(_ context.Context, dir string) ([]domainsftp.Entry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]domainsftp.Entry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			return nil, err
		}
		entry := domainsftp.Entry{
			Name: item.Name(), Path: filepath.Join(dir, item.Name()),
			Mode: uint32(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime(),
		}
		if item.IsDir() {
			entry.Kind = domainsftp.KindDir
		} else {
			entry.Kind = domainsftp.KindFile
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (f sftpLayoutFS) Stat(_ context.Context, path string) (domainsftp.Entry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return domainsftp.Entry{}, err
	}
	entry := domainsftp.Entry{
		Name: filepath.Base(path), Path: path, Mode: uint32(info.Mode().Perm()),
		Size: info.Size(), ModTime: info.ModTime(),
	}
	if info.IsDir() {
		entry.Kind = domainsftp.KindDir
	} else {
		entry.Kind = domainsftp.KindFile
	}
	return entry, nil
}

func (sftpLayoutFS) Mkdir(_ context.Context, path string) error { return os.Mkdir(path, 0o755) }
func (sftpLayoutFS) MkdirAll(_ context.Context, path string) error {
	return os.MkdirAll(path, 0o755)
}
func (sftpLayoutFS) Open(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }
func (sftpLayoutFS) Create(_ context.Context, path string) (io.WriteCloser, error) {
	return os.Create(path)
}
func (sftpLayoutFS) Remove(_ context.Context, path string) error    { return os.Remove(path) }
func (sftpLayoutFS) RemoveAll(_ context.Context, path string) error { return os.RemoveAll(path) }
func (sftpLayoutFS) Rename(_ context.Context, oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
func (sftpLayoutFS) Chmod(_ context.Context, path string, mode uint32) error {
	return os.Chmod(path, os.FileMode(mode))
}
func (f sftpLayoutFS) Join(parts ...string) string  { return filepath.Join(parts...) }
func (f sftpLayoutFS) Parent(path string) string    { return filepath.Dir(path) }
func (f sftpLayoutFS) Base(path string) string      { return filepath.Base(path) }
func (f sftpLayoutFS) Style() apptransfer.PathStyle { return sftpLayoutStyle() }
func (f sftpLayoutFS) Backing() string              { return f.backing }
func (f sftpLayoutFS) Close() error                 { return nil }
func (f sftpLayoutFS) Canonical(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute)
}

func sftpLayoutStyle() apptransfer.PathStyle {
	if runtime.GOOS == "windows" {
		return apptransfer.StyleWindows
	}
	return apptransfer.StylePOSIX
}

type sftpLayoutOpener struct {
	root    string
	backing string
}

func (o sftpLayoutOpener) OpenLocal(context.Context) (apptransfer.FileSystem, error) {
	return sftpLayoutFS{root: o.root, backing: o.backing}, nil
}
func (o sftpLayoutOpener) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	return sftpLayoutFS{root: o.root, backing: o.backing}, nil
}

type sftpLayoutHosts struct{}

func (sftpLayoutHosts) Hosts(context.Context) ([]apptransfer.HostTarget, error) {
	return []apptransfer.HostTarget{{ID: "h1", Label: "Test", Hostname: "test.example", Username: "me", Port: 22}}, nil
}

type sftpLayoutBookmarks struct{}

func (sftpLayoutBookmarks) LoadBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	return nil, nil
}
func (sftpLayoutBookmarks) SaveBookmarks(context.Context, []domainsftp.Bookmark) error { return nil }
func (sftpLayoutBookmarks) LoadLocalBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	return nil, nil
}
func (sftpLayoutBookmarks) SaveLocalBookmarks(context.Context, []domainsftp.Bookmark) error {
	return nil
}

// newSFTPLayoutShell renders the real shell with a real SFTP surface in both
// panes. The panes have different item counts so their footers can be told
// apart; every other label the test looks for is unique in the fixture.
func newSFTPLayoutShell(t *testing.T, width, height int, tag string) (*transferstate.State, *ui.Tester) {
	t.Helper()
	local := t.TempDir()
	remote := t.TempDir()
	for name, content := range map[string]string{
		"alpha.txt": "alpha", "beta.bin": "beta", "target.bin": "local target",
	} {
		if err := os.WriteFile(filepath.Join(local, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"remote.txt": "remote", "target.bin": "remote target",
	} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	service := apptransfer.NewService(
		sftpLayoutOpener{root: local, backing: "local"},
		sftpLayoutOpener{root: remote, backing: "remote:h1"},
		apptransfer.WithHosts(sftpLayoutHosts{}), apptransfer.WithBookmarks(sftpLayoutBookmarks{}),
	)
	ts := transferstate.NewState(service)
	t.Cleanup(ts.Shutdown)
	shellState := state.NewState(state.WithSFTP(transferui.View(ts)))
	shellState.ShowSFTP()
	tester := ui.NewTester(View(shellState), width, height)
	tester.SetLocale(tag)
	tester.Frame()
	waitForSFTPFrame(t, tester, "the local listing", func() bool {
		tab := ts.Tab(transferstate.Left)
		return tab != nil && tab.FS != nil && !tab.Loading
	})
	ts.OpenRemote(transferstate.Right, "h1", "Test")
	waitForSFTPFrame(t, tester, "the remote listing", func() bool {
		tab := ts.Tab(transferstate.Right)
		return tab != nil && tab.FS != nil && !tab.Loading
	})
	ts.Focus(transferstate.Left)
	tester.Frame()
	return ts, tester
}

// waitForSFTPFrame polls an observable condition with a hard deadline; the
// millisecond cadence only spaces the frames, it does not stand in for the
// condition being waited on.
func waitForSFTPFrame(t *testing.T, tester *ui.Tester, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(sftpLayoutTimeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s; texts: %v", sftpLayoutTimeout, what, tester.Texts())
		}
		tester.Frame()
		time.Sleep(time.Millisecond)
	}
}

// TestSFTPSurfaceStaysInsideTheShellContentColumn renders the SFTP surface
// where it actually lives: inside the shell's content column, which is 456
// DIPs wide at a 720x480 window. The surface's older layout checks rendered it
// at the full window width, so they could not see a control reaching the
// column's right edge. Every control the surface shows - both panes' tabs,
// breadcrumbs, icon toolbars, list headers, footers, the operation bar, and the
// conflict dialog - must stay inside the column and keep
// sftpSurfaceEdgeGutter from its edges, at both supported sizes and in all
// five shipping locales. English and Simplified Chinese are the acceptance
// languages; the other three are checked because a longer translation wraps
// differently in the same column.
func TestSFTPSurfaceStaysInsideTheShellContentColumn(t *testing.T) {
	for _, tag := range locales.Tags() {
		m := locales.In(tag)
		for _, size := range [][2]int{{720, 480}, {1100, 720}} {
			width, height := float32(size[0]), float32(size[1])
			t.Run(layoutName(size[0], size[1], "sftp-"+tag), func(t *testing.T) {
				ts, tester := newSFTPLayoutShell(t, size[0], size[1], tag)
				surface := wideContent(width, height)
				surfaceRight := surface.X + surface.W

				// The surface title is skipped: the sidebar's own SFTP entry
				// carries the same label, so a lookup by text cannot say which
				// one it found, and the sidebar entry is deliberately outside
				// the content column.
				// The Tab bar, the toolbars, and the rows' highlight run edge to
				// edge, as the original's, so they only have to stay inside the
				// surface; the footers' text keeps the gutter from its sides.
				// The pane's "+" is skipped for the title's reason: some
				// locales give it the same words as the shell's New Tab.
				chrome := []string{
					m.SftpLocal(), "Test", m.SftpGoHome(),
					m.SftpBookmarks(), m.SftpCopyPath(), m.SftpCompactList(), m.SftpFilter(),
					m.SftpNewFolder(), m.SftpNewFile(), m.SftpShowHiddenFiles(), m.SftpRefresh(),
					m.SftpMore(), m.SftpColumnName(), m.SftpColumnSize(),
					"alpha.txt", "beta.bin", "remote.txt",
				}
				content := []string{m.SftpItemCount(3), m.SftpItemCount(2)}
				for _, group := range []struct {
					labels []string
					gutter float32
				}{{chrome, 0}, {content, sftpSurfaceEdgeGutter}} {
					for _, label := range group.labels {
						rect, ok := tester.Find(label)
						if !ok {
							t.Fatalf("%dx%d %s does not show %q; texts: %v",
								size[0], size[1], tag, label, tester.Texts())
						}
						assertInsideSFTPFrame(t, size, surface, label, rect, group.gutter)
					}
				}

				// The icon toolbar is one row; its buttons must never share a
				// cell.
				toolbar := []string{
					m.SftpBookmarks(), m.SftpCopyPath(), m.SftpCompactList(), m.SftpFilter(),
					m.SftpNewFolder(), m.SftpNewFile(), m.SftpShowHiddenFiles(), m.SftpRefresh(),
					m.SftpMore(),
				}
				for i, first := range toolbar {
					a, _ := tester.Find(first)
					for _, second := range toolbar[i+1:] {
						b, _ := tester.Find(second)
						if overlaps(a, b) {
							t.Fatalf("%dx%d %s toolbar buttons overlap: %q %+v and %q %+v",
								size[0], size[1], tag, first, a, second, b)
						}
					}
				}

				// The panes split the surface: side by side, equal width, and
				// each footer inside its own pane.
				left, ok := tester.Find(m.SftpItemCount(3))
				if !ok {
					t.Fatalf("%dx%d %s does not show the left footer", size[0], size[1], tag)
				}
				right, ok := tester.Find(m.SftpItemCount(2))
				if !ok {
					t.Fatalf("%dx%d %s does not show the right footer", size[0], size[1], tag)
				}
				if right.X <= left.X || overlaps(left, right) {
					t.Fatalf("%dx%d %s panes are not side by side: left footer %+v right footer %+v",
						size[0], size[1], tag, left, right)
				}
				if left.W-right.W > 2 || right.W-left.W > 2 {
					t.Fatalf("%dx%d %s panes have different widths: left footer %+v right footer %+v",
						size[0], size[1], tag, left, right)
				}
				// This is the regression the acceptance screenshot showed: the
				// right pane's content column used to end 9 DIPs from the
				// surface's edge, so it read as clipped by the window frame.
				if right.X+right.W > surfaceRight-sftpSurfaceEdgeGutter {
					t.Fatalf("%dx%d %s right pane reaches the surface edge: footer %+v ends at %.1f, surface ends at %.1f",
						size[0], size[1], tag, right, right.X+right.W, surfaceRight)
				}

				// The operation bar and a conflict dialog are the surface's
				// densest overlay: a running transfer plus five actions, at the
				// size the acceptance exercised.
				if err := tester.Click("target.bin"); err != nil {
					t.Fatalf("%dx%d %s select the transferring file: %v", size[0], size[1], tag, err)
				}
				tester.Frame()
				ts.Transfer(transferstate.Left)
				waitForSFTPFrame(t, tester, "the conflict dialog", func() bool {
					dialog := ts.Dialog()
					return dialog != nil && dialog.Kind == transferstate.DialogConflict
				})
				tester.Frame()
				if ts.Snapshot() == nil {
					t.Fatalf("%dx%d %s shows a conflict but no operation bar", size[0], size[1], tag)
				}
				cancel, ok := tester.Find(m.SftpCancel())
				if !ok {
					t.Fatalf("%dx%d %s operation bar has no Cancel: %v", size[0], size[1], tag, tester.Texts())
				}
				assertInsideSFTPFrame(t, size, surface, m.SftpCancel(), cancel, sftpSurfaceEdgeGutter)

				dialogActions := []string{
					m.SftpConflictTitle(), m.SftpConflictStop(), m.SftpConflictSkip(),
					m.SftpConflictDuplicate(), m.SftpConflictReplace(),
				}
				for _, label := range dialogActions {
					rect, ok := tester.Find(label)
					if !ok {
						t.Fatalf("%dx%d %s conflict dialog does not show %q; texts: %v",
							size[0], size[1], tag, label, tester.Texts())
					}
					// The dialog is centred in the window rather than in the
					// surface, so it keeps the window margin, not the gutter.
					assertInsideSFTPFrame(t, size, ui.Rect{X: 0, Y: 0, W: width, H: height},
						label, rect, shellPadding)
				}
				if overlaps(mustFind(t, tester, m.SftpConflictStop()), mustFind(t, tester, m.SftpConflictReplace())) {
					t.Fatalf("%dx%d %s conflict actions overlap", size[0], size[1], tag)
				}
			})
		}
	}
}

func mustFind(t *testing.T, tester *ui.Tester, label string) ui.Rect {
	t.Helper()
	rect, ok := tester.Find(label)
	if !ok {
		t.Fatalf("does not show %q; texts: %v", label, tester.Texts())
	}
	return rect
}

// assertInsideSFTPFrame asserts rect lies inside frame and keeps gutter DIPs
// from the frame's left and right edges, and that rect lies inside the window.
// The gutter is horizontal: the surface stacks its rows from the top of the
// content column, so a control's text box sits a few DIPs below the frame's
// top edge by design, while a control reaching the frame's left or right edge
// was the defect.
func assertInsideSFTPFrame(t *testing.T, size [2]int, frame ui.Rect, label string, rect ui.Rect, gutter float32) {
	t.Helper()
	const epsilon = 0.5
	if rect.X < frame.X+gutter-epsilon || rect.Y < frame.Y-epsilon ||
		rect.X+rect.W > frame.X+frame.W-gutter+epsilon ||
		rect.Y+rect.H > frame.Y+frame.H+epsilon {
		t.Fatalf("%dx%d %q at %+v is closer than %.0f DIPs to the side of frame %+v",
			size[0], size[1], label, rect, gutter, frame)
	}
	if rect.X < -epsilon || rect.Y < -epsilon ||
		rect.X+rect.W > float32(size[0])+epsilon || rect.Y+rect.H > float32(size[1])+epsilon {
		t.Fatalf("%dx%d %q is outside the window: %+v", size[0], size[1], label, rect)
	}
}
