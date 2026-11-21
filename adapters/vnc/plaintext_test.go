package vnc

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model/rfb"
)

// TestNegotiatedPlaintextDecision proves the adapter classifies the negotiated
// type independently of the connection, including an encrypted type this slice
// does not yet negotiate.
func TestNegotiatedPlaintextDecision(t *testing.T) {
	cases := []struct {
		name     string
		security rfb.SecurityType
		want     bool
	}{
		{"none", rfb.SecurityNone, true},
		{"vnc password", rfb.SecurityVNCAuth, true},
		{"apple dh", rfb.SecurityAppleDH, true},
		{"vencrypt", rfb.SecurityVeNCrypt, false},
		{"ra2", rfb.SecurityRA2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := negotiatedPlaintext(tc.security); got != tc.want {
				t.Fatalf("negotiatedPlaintext(%v) = %v, want %v", tc.security, got, tc.want)
			}
		})
	}
}
