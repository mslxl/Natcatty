package transfer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// osFS is a FileSystem over a real temporary directory. The view tests use it
// instead of the adapter package, which the UI layer may not import, while
// still exercising the listing, selection, and copy paths with real files.
type osFS struct {
	root string
}

func (f osFS) Root(context.Context) (string, error) { return f.root, nil }

func (f osFS) List(_ context.Context, dir string) ([]domainsftp.Entry, error) {
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
			Name:    item.Name(),
			Path:    filepath.Join(dir, item.Name()),
			Mode:    uint32(info.Mode().Perm()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
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

func (f osFS) Stat(_ context.Context, path string) (domainsftp.Entry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return domainsftp.Entry{}, err
	}
	entry := domainsftp.Entry{
		Name:    filepath.Base(path),
		Path:    path,
		Mode:    uint32(info.Mode().Perm()),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if info.IsDir() {
		entry.Kind = domainsftp.KindDir
	} else {
		entry.Kind = domainsftp.KindFile
	}
	return entry, nil
}

func (osFS) Mkdir(_ context.Context, path string) error { return os.Mkdir(path, 0o755) }
func (osFS) MkdirAll(_ context.Context, path string) error {
	return os.MkdirAll(path, 0o755)
}
func (osFS) Open(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }
func (osFS) Create(_ context.Context, path string) (io.WriteCloser, error) {
	return os.Create(path)
}
func (osFS) Remove(_ context.Context, path string) error    { return os.Remove(path) }
func (osFS) RemoveAll(_ context.Context, path string) error { return os.RemoveAll(path) }
func (osFS) Rename(_ context.Context, oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
func (osFS) Chmod(_ context.Context, path string, mode uint32) error {
	return os.Chmod(path, os.FileMode(mode))
}
func (f osFS) Join(parts ...string) string  { return filepath.Join(parts...) }
func (f osFS) Parent(path string) string    { return filepath.Dir(path) }
func (f osFS) Base(path string) string      { return filepath.Base(path) }
func (f osFS) Style() apptransfer.PathStyle { return style() }
func (f osFS) Close() error                 { return nil }

func style() apptransfer.PathStyle {
	if runtime.GOOS == "windows" {
		return apptransfer.StyleWindows
	}
	return apptransfer.StylePOSIX
}

type osOpener struct{ root string }

func (o osOpener) OpenLocal(context.Context) (apptransfer.FileSystem, error) {
	return osFS{root: o.root}, nil
}

func (o osOpener) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	return osFS{root: o.root}, nil
}

type viewHosts struct{}

func (viewHosts) Hosts(context.Context) ([]apptransfer.HostTarget, error) {
	return []apptransfer.HostTarget{{ID: "h1", Label: "Test", Hostname: "test.example", Username: "me", Port: 22}}, nil
}

type viewBookmarks struct{}

func (viewBookmarks) LoadBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	return nil, nil
}
func (viewBookmarks) SaveBookmarks(context.Context, []domainsftp.Bookmark) error { return nil }
func (viewBookmarks) LoadLocalBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	return nil, nil
}
func (viewBookmarks) SaveLocalBookmarks(context.Context, []domainsftp.Bookmark) error { return nil }

func newViewService(local, remote string) *apptransfer.Service {
	return apptransfer.NewService(osOpener{root: local}, osOpener{root: remote},
		apptransfer.WithHosts(viewHosts{}), apptransfer.WithBookmarks(viewBookmarks{}))
}

type viewFixture struct {
	state  *transferstate.State
	tester *ui.Tester
	local  string
	remote string
}

// newViewFixture builds the surface over two temporary directories and opens
// a remote Tab on the right. It returns after both listings are visible.
func newViewFixture(t *testing.T, width, height int, locale string) *viewFixture {
	t.Helper()
	local := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(local, "alpha.txt"), "alpha")
	writeFile(t, filepath.Join(local, "beta.bin"), strings.Repeat("b", 100))
	writeFile(t, filepath.Join(local, ".secret"), "hidden")
	if err := os.MkdirAll(filepath.Join(local, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(local, "folder", "inside.txt"), "inside")
	writeFile(t, filepath.Join(remote, "remote.txt"), "remote")

	service := newViewService(local, remote)
	state := transferstate.NewState(service)
	t.Cleanup(state.Shutdown)

	tester := ui.NewTester(View(state), width, height)
	tester.SetLocale(locale)
	tester.Frame()
	frameUntil(t, tester, "the local listing", func() bool {
		tab := state.Tab(transferstate.Left)
		return tab != nil && tab.FS != nil && tab.Path == local && !tab.Loading
	})
	state.OpenRemote(transferstate.Right, "h1", "Test")
	frameUntil(t, tester, "the remote listing", func() bool {
		tab := state.Tab(transferstate.Right)
		return tab != nil && tab.FS != nil && tab.Path == remote && !tab.Loading
	})
	state.Focus(transferstate.Left)
	tester.Frame()
	return &viewFixture{state: state, tester: tester, local: local, remote: remote}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func contentOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
