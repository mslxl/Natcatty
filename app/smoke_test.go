package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mslxl/natcatty/internal/testutil"
)

// The smoke build prints this once the main window exists. Keep it in sync
// with app/run_smoke.go.
const launchSmokeReadyMarker = "natcatty: launch smoke ready"

// TestLaunchSmoke builds the real app with the launchsmoke tag, starts it, and
// checks that it reaches window readiness and quits cleanly. It is opt-in
// because a test suite must not pop windows, and because a headless CI runner
// has no display to open one on. Run it with:
//
//	NATCATTY_LAUNCH_SMOKE=1 go test ./app -run TestLaunchSmoke -v
func TestLaunchSmoke(t *testing.T) {
	if os.Getenv("NATCATTY_LAUNCH_SMOKE") != "1" {
		t.Skip("set NATCATTY_LAUNCH_SMOKE=1 to launch the native window")
	}
	if headless() {
		t.Skip("no display available for a native window")
	}

	root := testutil.ModuleRoot(t)
	binary := filepath.Join(t.TempDir(), "launchsmoke")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	build := exec.Command("go", "build", "-tags", "launchsmoke", "-o", binary, "./cmd/launchsmoke")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	var buildOutput bytes.Buffer
	build.Stdout = &buildOutput
	build.Stderr = &buildOutput
	if err := build.Run(); err != nil {
		t.Fatalf("build the launch smoke binary: %v\n%s", err, buildOutput.String())
	}

	cmd := exec.Command(binary)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the app: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the app exited with an error: %v\n%s", err, output.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("the app did not quit within 30s\n%s", output.String())
	}

	if !bytes.Contains(output.Bytes(), []byte(launchSmokeReadyMarker)) {
		t.Fatalf("the app never reached window readiness:\n%s", output.String())
	}
}

func headless() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	return os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
}
