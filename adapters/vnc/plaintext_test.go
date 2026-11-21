package vnc

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model/rfb"
)

// TestNegotiatedPlaintextDecision proves the adapter classifies the negotiated
// type independently of the connection, including VeNCrypt subtypes and the
// RA2 variants.
func TestNegotiatedPlaintextDecision(t *testing.T) {
	cases := []struct {
		name   string
		choice rfb.SecurityChoice
		want   bool
	}{
		{"none", rfb.SecurityChoice{Type: rfb.SecurityNone}, true},
		{"vnc password", rfb.SecurityChoice{Type: rfb.SecurityVNCAuth}, true},
		{"apple dh", rfb.SecurityChoice{Type: rfb.SecurityAppleDH}, true},
		{"vencrypt plain", rfb.SecurityChoice{Type: rfb.SecurityVeNCrypt, Subtype: rfb.VeNCryptPlain}, true},
		{"vencrypt tlsplain", rfb.SecurityChoice{Type: rfb.SecurityVeNCrypt, Subtype: rfb.VeNCryptTLSPlain}, false},
		{"vencrypt x509plain", rfb.SecurityChoice{Type: rfb.SecurityVeNCrypt, Subtype: rfb.VeNCryptX509Plain}, false},
		{"ra2ne", rfb.SecurityChoice{Type: rfb.SecurityRA2ne}, true},
		{"ra2ne-256", rfb.SecurityChoice{Type: rfb.SecurityRA2ne_256}, true},
		{"ra2", rfb.SecurityChoice{Type: rfb.SecurityRA2}, false},
		{"ra2-256", rfb.SecurityChoice{Type: rfb.SecurityRA2_256}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := negotiatedPlaintext(tc.choice); got != tc.want {
				t.Fatalf("negotiatedPlaintext(%v) = %v, want %v", tc.choice, got, tc.want)
			}
		})
	}
}
