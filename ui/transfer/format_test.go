package transfer

import (
	"errors"
	"testing"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// TestReservedFailureNoticeKeepsTheDedicatedString proves a refused concurrent
// Transfer says another transfer owns the destination instead of the generic
// operation failure.
func TestReservedFailureNoticeKeepsTheDedicatedString(t *testing.T) {
	m := locales.In("en")
	err := &apptransfer.Error{Kind: apptransfer.KindReserved}
	if got, want := noticeText(m, transferstate.FailureNotice(err)),
		"Another transfer is already writing to this destination."; got != want {
		t.Fatalf("reserved notice = %q, want %q", got, want)
	}
}

// TestFailureNoticeKeepsEachTypedMessage proves every classified failure keeps
// its specific message, and only an unclassified one falls back to the generic
// operation failure.
func TestFailureNoticeKeepsEachTypedMessage(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"reserved", &apptransfer.Error{Kind: apptransfer.KindReserved}, m.SftpErrorReserved()},
		{"exists", &apptransfer.Error{Kind: apptransfer.KindExists}, m.SftpErrorExists()},
		{"not found", &apptransfer.Error{Kind: apptransfer.KindNotFound}, m.SftpErrorNotFound()},
		{"permission", &apptransfer.Error{Kind: apptransfer.KindPermission}, m.SftpErrorPermission()},
		{"type mismatch", &apptransfer.Error{Kind: apptransfer.KindTypeMismatch}, m.SftpErrorTypeMismatch()},
		{"unclassified", errors.New("boom"), m.SftpOperationFailed()},
	}
	for _, tc := range cases {
		if got := noticeText(m, transferstate.FailureNotice(tc.err)); got != tc.want {
			t.Errorf("%s notice = %q, want %q", tc.name, got, tc.want)
		}
	}
}
