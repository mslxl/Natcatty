package launch

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type call struct {
	name string
	args []string
}

func recordingRunner(calls *[]call, err error) Runner {
	return func(_ context.Context, name string, args ...string) error {
		*calls = append(*calls, call{name: name, args: append([]string(nil), args...)})
		return err
	}
}

func TestOpenSystemUsesThePlatformHandler(t *testing.T) {
	tests := []struct {
		goos string
		want call
	}{
		{"darwin", call{name: "open", args: []string{"/tmp/report.txt"}}},
		{"linux", call{name: "xdg-open", args: []string{"/tmp/report.txt"}}},
	}
	for _, test := range tests {
		var calls []call
		launcher := New(WithGOOS(test.goos), WithRunner(recordingRunner(&calls, nil)))
		if err := launcher.OpenSystem(context.Background(), "/tmp/report.txt"); err != nil {
			t.Fatalf("%s: %v", test.goos, err)
		}
		if len(calls) != 1 || !reflect.DeepEqual(calls[0], test.want) {
			t.Errorf("%s launched %+v, want %+v", test.goos, calls, test.want)
		}
	}
}

func TestWindowsSystemOpenUsesShellExecute(t *testing.T) {
	var system []string
	launcher := New(WithGOOS("windows"), WithShellOpen(func(path string) error {
		system = append(system, path)
		return nil
	}))
	if err := launcher.OpenSystem(context.Background(), `C:\work\report.txt`); err != nil {
		t.Fatal(err)
	}
	if want := []string{`C:\work\report.txt`}; !reflect.DeepEqual(system, want) {
		t.Errorf("ShellExecute paths = %+v, want %+v", system, want)
	}
}

func TestOpenApplicationUsesTheApplicationCommand(t *testing.T) {
	tests := []struct {
		goos        string
		application string
		path        string
		want        call
	}{
		{
			goos:        "linux",
			application: "/usr/bin/code",
			path:        "/tmp/report.txt",
			want:        call{name: "/usr/bin/code", args: []string{"/tmp/report.txt"}},
		},
		{
			goos:        "windows",
			application: `C:\Tools\viewer.exe`,
			path:        `C:\work\photo.png`,
			want:        call{name: `C:\Tools\viewer.exe`, args: []string{`C:\work\photo.png`}},
		},
		{
			goos:        "darwin",
			application: "Visual Studio Code",
			path:        "/tmp/report.txt",
			want: call{name: "open", args: []string{
				"-a", "Visual Studio Code", "/tmp/report.txt"}},
		},
	}
	for _, test := range tests {
		var calls []call
		launcher := New(WithGOOS(test.goos), WithRunner(recordingRunner(&calls, nil)))
		if err := launcher.OpenApplication(context.Background(), test.application, test.path); err != nil {
			t.Fatalf("%s: %v", test.goos, err)
		}
		if len(calls) != 1 || !reflect.DeepEqual(calls[0], test.want) {
			t.Errorf("%s launched %+v, want %+v", test.goos, calls, test.want)
		}
	}
}

func TestEmptyArgumentsAndCancellationDoNotLaunch(t *testing.T) {
	var calls []call
	launcher := New(WithGOOS("linux"), WithRunner(recordingRunner(&calls, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		run  func() error
	}{
		{"empty system path", func() error { return launcher.OpenSystem(context.Background(), " ") }},
		{"empty application", func() error {
			return launcher.OpenApplication(context.Background(), "", "/tmp/file")
		}},
		{"empty application path", func() error {
			return launcher.OpenApplication(context.Background(), "/usr/bin/code", "")
		}},
		{"cancelled system", func() error { return launcher.OpenSystem(ctx, "/tmp/file") }},
		{"cancelled application", func() error {
			return launcher.OpenApplication(ctx, "/usr/bin/code", "/tmp/file")
		}},
	}
	for _, test := range tests {
		if err := test.run(); err == nil {
			t.Errorf("%s did not fail", test.name)
		}
	}
	if len(calls) != 0 {
		t.Errorf("refused calls still launched %+v", calls)
	}
}

func TestRunnerFailureReachesTheCaller(t *testing.T) {
	launchErr := errors.New("not found")
	launcher := New(WithGOOS("linux"), WithRunner(recordingRunner(&[]call{}, launchErr)))
	if err := launcher.OpenSystem(context.Background(), "/tmp/file"); !errors.Is(err, launchErr) {
		t.Errorf("error = %v, want the runner failure", err)
	}
}

func TestWindowsShellOpenIsWiredByDefault(t *testing.T) {
	// ShellExecute itself cannot be invoked in a test without opening a real
	// file, so this only checks that the seam exists; the command shapes are
	// covered above with an injected seam.
	launcher := New(WithGOOS("windows"))
	if launcher.shellOpen == nil {
		t.Fatal("the Windows launcher has no ShellExecute seam")
	}
}
