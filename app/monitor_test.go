package app

import (
	"context"
	"errors"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appmonitor "github.com/mslxl/natcatty/application/monitor"
)

func TestMonitorFailureMapsConnectionFailures(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reason appmonitor.Reason
	}{
		{"host-key-sentinel", appconnection.ErrHostKeyRejected, appmonitor.ReasonHostKey},
		{"auth-sentinel", appconnection.ErrAuthentication, appmonitor.ReasonAuth},
		{"dial-timeout", appconnection.ErrDialTimeout, appmonitor.ReasonTimeout},
		{"auth-timeout", appconnection.ErrAuthTimeout, appmonitor.ReasonTimeout},
		{"unsupported", appconnection.ErrUnsupportedAlgorithm, appmonitor.ReasonUnsupported},
		{"cancelled", context.Canceled, appmonitor.ReasonCancelled},
		{"deadline", context.DeadlineExceeded, appmonitor.ReasonTimeout},
		{"typed-host-key", &appconnection.Error{
			Kind: appconnection.KindHostKey, Cause: errors.New("refused"),
		}, appmonitor.ReasonHostKey},
		{"typed-auth", &appconnection.Error{
			Kind: appconnection.KindAuth, Cause: errors.New("refused"),
		}, appmonitor.ReasonAuth},
		{"plain", errors.New("socket closed"), appmonitor.ReasonNetwork},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := monitorFailure(test.err)
			var typed *appmonitor.Error
			if !errors.As(err, &typed) {
				t.Fatalf("monitorFailure(%v) = %T, want *monitor.Error", test.err, err)
			}
			if typed.Reason != test.reason {
				t.Fatalf("reason = %q, want %q", typed.Reason, test.reason)
			}
		})
	}
}

func TestMonitorFailureKeepsAnAlreadyTypedError(t *testing.T) {
	original := &appmonitor.Error{Reason: appmonitor.ReasonOS, Cause: errors.New("windows")}
	if got := monitorFailure(original); got != original {
		t.Fatalf("monitorFailure rewrapped a typed error: %#v", got)
	}
	if got := monitorFailure(nil); got != nil {
		t.Fatalf("monitorFailure(nil) = %v", got)
	}
}
