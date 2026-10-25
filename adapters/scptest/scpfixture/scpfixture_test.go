package scpfixture

import (
	"bufio"
	"context"
	"errors"
	"io"
	"testing"
)

// TestStreamCloseModelsPeerCloseTeardown locks the fixture behaviour that let
// ticket 35 miss 35-SCP-UPLOAD-CLOSE-EOF: once the command has finished, the
// SSH server has closed the channel and x/crypto's Session.Close reports
// io.EOF. A fixture that returned nil made a failing SCP teardown look green.
// Only the first Close observes the peer close, as the once-guarded exec
// channel does.
func TestStreamCloseModelsPeerCloseTeardown(t *testing.T) {
	stream := newFixtureStream()
	stream.run(context.Background(), func(*bufio.Reader, io.Writer, io.Writer) int { return 0 })
	if code, err := stream.ExitCode(); err != nil || code != 0 {
		t.Fatalf("ExitCode = %d, %v", code, err)
	}
	if err := stream.Close(); !errors.Is(err, io.EOF) {
		t.Fatalf("first Close after the peer finished = %v, want io.EOF", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

// TestStreamCloseDoesNotReportPeerCloseWhileTheHandlerRuns proves a Close that
// aborts a live handler is not mistaken for a clean peer close.
func TestStreamCloseDoesNotReportPeerCloseWhileTheHandlerRuns(t *testing.T) {
	stream := newFixtureStream()
	release := make(chan struct{})
	stream.run(context.Background(), func(*bufio.Reader, io.Writer, io.Writer) int {
		<-release
		return 0
	})
	if err := stream.Close(); err != nil {
		t.Fatalf("Close while the handler runs = %v, want nil", err)
	}
	close(release)
	if code, err := stream.ExitCode(); err != nil || code != 0 {
		t.Fatalf("ExitCode = %d, %v", code, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("repeated Close = %v, want nil", err)
	}
}

// TestLsModeRendersEveryPermissionBit covers the bit positions the upload
// transaction verifies: group and other write (a Windows source reports 0666)
// and the special execute markers. A fixture that only clears bits from a
// fixed literal would report 0666 as 0644 and fail a correct upload.
func TestLsModeRendersEveryPermissionBit(t *testing.T) {
	cases := []struct {
		name string
		node Node
		want string
	}{
		{"file 0644", Node{Type: File, Mode: 0o644}, "-rw-r--r--"},
		{"file 0666", Node{Type: File, Mode: 0o666}, "-rw-rw-rw-"},
		{"file 0755", Node{Type: File, Mode: 0o755}, "-rwxr-xr-x"},
		{"setuid executable", Node{Type: File, Mode: 0o4755}, "-rwsr-xr-x"},
		{"setgid without execute", Node{Type: File, Mode: 0o2644}, "-rw-r-Sr--"},
		{"sticky directory", Node{Type: Directory, Mode: 0o1777}, "drwxrwxrwt"},
		{"symlink", Node{Type: Symlink, Mode: 0o777}, "lrwxrwxrwx"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := lsMode(&testCase.node); got != testCase.want {
				t.Fatalf("lsMode(%04o) = %q, want %q", testCase.node.Mode, got, testCase.want)
			}
		})
	}
}
