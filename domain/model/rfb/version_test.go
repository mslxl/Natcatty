package rfb

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		wire string
		want Version
	}{
		{"RFB 003.003\n", Version3_3},
		{"RFB 003.007\n", Version3_7},
		{"RFB 003.008\n", Version3_8},
	}
	for _, test := range cases {
		got, err := ParseVersion([]byte(test.wire))
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", test.wire, err)
		}
		if got != test.want {
			t.Fatalf("ParseVersion(%q) = %v, want %v", test.wire, got, test.want)
		}
		if test.want.Wire() != test.wire {
			t.Fatalf("%v.Wire() = %q, want %q", test.want, test.want.Wire(), test.wire)
		}
	}
}

func TestParseVersionRejectsMalformed(t *testing.T) {
	for _, wire := range []string{
		"",
		"RFB 003.008",
		"RFB 003.008\n\n",
		"rfb 003.008\n",
		"RFB 003x008\n",
		"RFB 00a.008\n",
		"RFB 003.00x\n",
		"RFB 003.008\r",
	} {
		if _, err := ParseVersion([]byte(wire)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("ParseVersion(%q) = %v, want ErrMalformed", wire, err)
		}
	}
}

func TestNegotiateVersionPair(t *testing.T) {
	cases := []struct {
		a, b Version
		want Version
	}{
		{Version3_8, Version3_7, Version3_7},
		{Version3_7, Version3_8, Version3_7},
		{Version3_8, Version3_3, Version3_3},
		{Version3_3, Version3_8, Version3_3},
	}
	for _, test := range cases {
		got, err := NegotiateVersionPair(test.a, test.b)
		if err != nil {
			t.Fatalf("NegotiateVersionPair(%v, %v): %v", test.a, test.b, err)
		}
		if got != test.want {
			t.Fatalf("NegotiateVersionPair(%v, %v) = %v, want %v", test.a, test.b, got, test.want)
		}
	}
	if _, err := NegotiateVersionPair(Version{Major: 4}, Version3_8); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("major mismatch = %v", err)
	}
}

func TestNegotiateVersion(t *testing.T) {
	cases := []struct {
		server Version
		want   Version
		ok     bool
	}{
		{Version{Major: 3, Minor: 3}, Version3_3, true},
		{Version{Major: 3, Minor: 5}, Version3_3, true},
		{Version{Major: 3, Minor: 7}, Version3_7, true},
		{Version{Major: 3, Minor: 8}, Version3_8, true},
		{Version{Major: 3, Minor: 889}, Version3_8, true},
		{Version{Major: 4, Minor: 1}, Version{}, false},
	}
	for _, test := range cases {
		got, err := NegotiateVersion(test.server)
		if !test.ok {
			if !errors.Is(err, ErrUnsupportedVersion) {
				t.Fatalf("NegotiateVersion(%v) = %v, want ErrUnsupportedVersion", test.server, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NegotiateVersion(%v): %v", test.server, err)
		}
		if got != test.want {
			t.Fatalf("NegotiateVersion(%v) = %v, want %v", test.server, got, test.want)
		}
	}
}
