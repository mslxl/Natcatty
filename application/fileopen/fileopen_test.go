package fileopen

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

type fakePreferences struct {
	settings domainpreferences.Settings
}

func (f *fakePreferences) Settings() domainpreferences.Settings { return f.settings }

type launchCall struct {
	system      bool
	application string
	path        string
}

type fakeLauncher struct {
	calls []launchCall
	err   error
}

func (l *fakeLauncher) OpenSystem(_ context.Context, path string) error {
	l.calls = append(l.calls, launchCall{system: true, path: path})
	return l.err
}

func (l *fakeLauncher) OpenApplication(_ context.Context, application, path string) error {
	l.calls = append(l.calls, launchCall{application: application, path: path})
	return l.err
}

type fakeEditor struct {
	files []File
	err   error
}

func (e *fakeEditor) OpenFile(_ context.Context, file File) error {
	e.files = append(e.files, file)
	return e.err
}

func settingsWith(t *testing.T, mutate func(*domainpreferences.SFTPEditor)) *fakePreferences {
	t.Helper()
	settings := domainpreferences.Defaults()
	mutate(&settings.SFTPEditor)
	return &fakePreferences{settings: domainpreferences.Normalize(settings)}
}

func TestAssociationSelectsTheOpener(t *testing.T) {
	prefs := settingsWith(t, func(s *domainpreferences.SFTPEditor) {
		var ok bool
		*s, ok = s.SetAssociation(domainpreferences.FileAssociation{
			Extension: "txt", Kind: domainpreferences.OpenerInternal,
		})
		if !ok {
			t.Fatal("could not build the association")
		}
	})
	editor := &fakeEditor{}
	launcher := &fakeLauncher{}
	service := New(prefs, launcher, WithEditor(editor))

	result, err := service.Open(context.Background(), Request{
		Name: "notes.TXT", LocalPath: "/tmp/notes.TXT", Size: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != domainpreferences.OpenerInternal || len(editor.files) != 1 {
		t.Fatalf("result = %+v, editor calls = %+v", result, editor.files)
	}
	if got := editor.files[0]; got.Path != "/tmp/notes.TXT" || got.Name != "notes.TXT" || got.Size != 12 {
		t.Errorf("editor file = %+v", got)
	}
	if len(launcher.calls) != 0 {
		t.Errorf("the associated editor also launched %+v", launcher.calls)
	}

	// A file whose extension has no association goes to the default opener.
	if _, err := service.Open(context.Background(), Request{
		Name: "readme.md", LocalPath: "/tmp/readme.md",
	}); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || !launcher.calls[0].system {
		t.Fatalf("default opener calls = %+v, want the system opener", launcher.calls)
	}
	if len(editor.files) != 1 {
		t.Error("the default system opener also reached the editor")
	}
}

func TestExternalAssociationRunsTheChosenApplication(t *testing.T) {
	prefs := settingsWith(t, func(s *domainpreferences.SFTPEditor) {
		var ok bool
		*s, ok = s.SetAssociation(domainpreferences.FileAssociation{
			Extension: "png", Kind: domainpreferences.OpenerExternal,
			Application: domainpreferences.ExternalApplication{
				Path: `C:\Tools\viewer.exe`, Name: "Viewer",
			},
		})
		if !ok {
			t.Fatal("could not build the association")
		}
	})
	launcher := &fakeLauncher{}
	service := New(prefs, launcher)

	result, err := service.Open(context.Background(), Request{
		Name: "photo.png", LocalPath: `C:\work\photo.png`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != domainpreferences.OpenerExternal {
		t.Errorf("result kind = %q, want external", result.Kind)
	}
	want := launchCall{application: `C:\Tools\viewer.exe`, path: `C:\work\photo.png`}
	if len(launcher.calls) != 1 || launcher.calls[0] != want {
		t.Fatalf("launcher calls = %+v, want %+v", launcher.calls, want)
	}
}

func TestUnknownExtensionUsesTheDefaultOpener(t *testing.T) {
	prefs := settingsWith(t, func(s *domainpreferences.SFTPEditor) {
		s.DefaultOpener = domainpreferences.Opener{
			Kind: domainpreferences.OpenerExternal,
			Application: domainpreferences.ExternalApplication{
				Path: `/usr/bin/handoff`, Name: "Handoff",
			},
		}
	})
	launcher := &fakeLauncher{}
	service := New(prefs, launcher)

	for _, name := range []string{"data.unknown", "LICENSE", ".env"} {
		launcher.calls = nil
		if _, err := service.Open(context.Background(), Request{
			Name: name, LocalPath: "/tmp/" + name,
		}); err != nil {
			t.Fatalf("open %q: %v", name, err)
		}
		want := launchCall{application: "/usr/bin/handoff", path: "/tmp/" + name}
		if len(launcher.calls) != 1 || launcher.calls[0] != want {
			t.Errorf("%q used %+v, want the default application %+v", name, launcher.calls, want)
		}
	}
}

func TestRemoteFileIsMaterializedBeforeOpening(t *testing.T) {
	root := t.TempDir()
	launcher := &fakeLauncher{}
	service := New(settingsWith(t, func(*domainpreferences.SFTPEditor) {}), launcher,
		WithTempRoot(root))

	result, err := service.Open(context.Background(), Request{
		Name: "remote.txt", Remote: true, Size: 11,
		Source: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("hello remote")), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Remote || result.LocalPath == "" {
		t.Fatalf("result = %+v, want a materialized remote file", result)
	}
	if filepath.Dir(result.LocalPath) == root {
		t.Errorf("the remote file was written directly into the temp root: %s", result.LocalPath)
	}
	if !strings.HasPrefix(filepath.Clean(result.LocalPath), filepath.Clean(root)+string(filepath.Separator)) {
		t.Errorf("materialized path %q escapes %q", result.LocalPath, root)
	}
	data, err := os.ReadFile(result.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello remote" {
		t.Errorf("materialized content = %q", data)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].path != result.LocalPath {
		t.Fatalf("launcher calls = %+v, want %q", launcher.calls, result.LocalPath)
	}
}

func TestRemoteFileNameCannotEscapeTheTempDirectory(t *testing.T) {
	root := t.TempDir()
	service := New(settingsWith(t, func(*domainpreferences.SFTPEditor) {}), &fakeLauncher{},
		WithTempRoot(root))
	result, err := service.Open(context.Background(), Request{
		Name: "../../escape.txt", Remote: true,
		Source: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("x")), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(result.LocalPath) != "escape.txt" {
		t.Errorf("materialized name = %q, want the base name", filepath.Base(result.LocalPath))
	}
	if !strings.HasPrefix(filepath.Clean(result.LocalPath), filepath.Clean(root)+string(filepath.Separator)) {
		t.Errorf("materialized path %q escapes %q", result.LocalPath, root)
	}
}

func TestInternalOpenerWithoutAnEditorIsReportedBeforeDownloading(t *testing.T) {
	root := t.TempDir()
	prefs := settingsWith(t, func(s *domainpreferences.SFTPEditor) {
		s.DefaultOpener = domainpreferences.Opener{Kind: domainpreferences.OpenerInternal}
	})
	called := false
	service := New(prefs, &fakeLauncher{}, WithTempRoot(root))
	_, err := service.Open(context.Background(), Request{
		Name: "remote.txt", Remote: true,
		Source: func(context.Context) (io.ReadCloser, error) {
			called = true
			return io.NopCloser(strings.NewReader("x")), nil
		},
	})
	if kind, ok := KindOf(err); !ok || kind != ErrorEditorUnavailable {
		t.Fatalf("error = %v, kind = %q, %v; want the editor-unavailable kind", err, kind, ok)
	}
	if called {
		t.Error("the unavailable internal editor still downloaded the remote file")
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("temp root is not empty: %v", entries)
	}
}

func TestRemoteSourceAndLaunchFailuresAreClassified(t *testing.T) {
	root := t.TempDir()
	sourceErr := errors.New("connection reset")
	service := New(settingsWith(t, func(*domainpreferences.SFTPEditor) {}), &fakeLauncher{},
		WithTempRoot(root))
	_, err := service.Open(context.Background(), Request{
		Name: "remote.txt", Remote: true,
		Source: func(context.Context) (io.ReadCloser, error) { return nil, sourceErr },
	})
	if kind, ok := KindOf(err); !ok || kind != ErrorDownload {
		t.Fatalf("source error kind = %q, %v; want the download kind", kind, ok)
	}
	if !errors.Is(err, sourceErr) {
		t.Error("the source error was not wrapped")
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("failed download left %v behind", entries)
	}

	launchErr := errors.New("no such application")
	service = New(settingsWith(t, func(*domainpreferences.SFTPEditor) {}),
		&fakeLauncher{err: launchErr}, WithTempRoot(root))
	_, err = service.Open(context.Background(), Request{
		Name: "local.txt", LocalPath: "/tmp/local.txt",
	})
	if kind, ok := KindOf(err); !ok || kind != ErrorLaunch {
		t.Fatalf("launch error kind = %q, %v; want the launch kind", kind, ok)
	}
	if !errors.Is(err, launchErr) {
		t.Error("the launch error was not wrapped")
	}
}

func TestFailedLaunchOfAMaterializedFileRemovesTheTempDirectory(t *testing.T) {
	root := t.TempDir()
	service := New(settingsWith(t, func(*domainpreferences.SFTPEditor) {}),
		&fakeLauncher{err: errors.New("no such application")}, WithTempRoot(root))
	_, err := service.Open(context.Background(), Request{
		Name: "remote.txt", Remote: true,
		Source: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("x")), nil
		},
	})
	if kind, ok := KindOf(err); !ok || kind != ErrorLaunch {
		t.Fatalf("error = %v, kind = %q; want the launch kind", err, kind)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a failed launch left %v behind", entries)
	}
}

func TestCancellationAfterMaterializingRemovesTheTempDirectory(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	launcher := &fakeLauncher{}
	service := New(settingsWith(t, func(*domainpreferences.SFTPEditor) {}), launcher,
		WithTempRoot(root))
	_, err := service.Open(ctx, Request{
		Name: "remote.txt", Remote: true,
		Source: func(context.Context) (io.ReadCloser, error) {
			cancel()
			return io.NopCloser(strings.NewReader("x")), nil
		},
	})
	if kind, ok := KindOf(err); !ok || kind != ErrorCancelled {
		t.Fatalf("error = %v, kind = %q; want the cancelled kind", err, kind)
	}
	if len(launcher.calls) != 0 {
		t.Errorf("a cancelled open launched %+v", launcher.calls)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a cancelled open left %v behind", entries)
	}
}

func TestEditorFailureIsClassifiedSeparatelyFromALaunchFailure(t *testing.T) {
	editorErr := errors.New("the editor tab was closed")
	editor := &fakeEditor{err: editorErr}
	prefs := settingsWith(t, func(s *domainpreferences.SFTPEditor) {
		s.DefaultOpener = domainpreferences.Opener{Kind: domainpreferences.OpenerInternal}
	})
	service := New(prefs, &fakeLauncher{}, WithEditor(editor))
	_, err := service.Open(context.Background(), Request{
		Name: "notes.txt", LocalPath: "/tmp/notes.txt",
	})
	if kind, ok := KindOf(err); !ok || kind != ErrorEditorFailed {
		t.Fatalf("error = %v, kind = %q; want the editor-failed kind", err, kind)
	}
	if !errors.Is(err, editorErr) {
		t.Error("the editor error was not wrapped")
	}
}

func TestRemoteFileWithoutASourceIsRefused(t *testing.T) {
	service := New(settingsWith(t, func(*domainpreferences.SFTPEditor) {}), &fakeLauncher{})
	_, err := service.Open(context.Background(), Request{Name: "remote.txt", Remote: true})
	if kind, ok := KindOf(err); !ok || kind != ErrorSource {
		t.Fatalf("error = %v, kind = %q; want the source kind", err, kind)
	}
}

func TestCancelledOpenDoesNotLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	launcher := &fakeLauncher{}
	service := New(settingsWith(t, func(*domainpreferences.SFTPEditor) {}), launcher)
	_, err := service.Open(ctx, Request{Name: "local.txt", LocalPath: "/tmp/local.txt"})
	if kind, ok := KindOf(err); !ok || kind != ErrorCancelled {
		t.Fatalf("error = %v, kind = %q; want the cancelled kind", err, kind)
	}
	if len(launcher.calls) != 0 {
		t.Errorf("a cancelled open launched %+v", launcher.calls)
	}
}
