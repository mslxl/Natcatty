package state

import (
	"encoding/base32"
	"errors"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

var totpStateBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func totpStateSecret() string {
	return totpStateBase32.EncodeToString([]byte("12345678901234567890"))
}

type fakeTOTPQRPicker struct {
	payloads []string
	err      error
	calls    int
}

func (f *fakeTOTPQRPicker) PickTOTPQRPayloads() ([]string, error) {
	f.calls++
	return f.payloads, f.err
}

func newTOTPState(t *testing.T, picker *fakeTOTPQRPicker, hosts *fakeHosts) *State {
	t.Helper()
	state := NewState(hosts, WithTOTPQRPicker(picker))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state
}

func TestChooseTOTPQRCodeFillsTheCanonicalURI(t *testing.T) {
	secret := totpStateSecret()
	picker := &fakeTOTPQRPicker{payloads: []string{
		"https://example.test/not-a-code",
		"otpauth://totp/Example:alice?secret=" + secret + "&issuer=Example",
	}}
	state := newTOTPState(t, picker, &fakeHosts{})
	state.BeginCreate()

	if !state.ChooseTOTPQRCode() {
		t.Fatalf("ChooseTOTPQRCode: %v", state.Problem())
	}
	if picker.calls != 1 {
		t.Fatalf("picker calls = %d, want 1", picker.calls)
	}
	form := state.Form()
	if !strings.HasPrefix(form.TOTP, "otpauth://totp/") {
		t.Fatalf("TOTP = %q, want a canonical otpauth URI", form.TOTP)
	}
	if !strings.Contains(form.TOTP, "issuer=Example") || !strings.Contains(form.TOTP, "secret="+secret) {
		t.Fatalf("canonical URI lost parameters: %q", form.TOTP)
	}
	if form.ClearTOTP {
		t.Fatal("an import must not keep a pending removal")
	}
	if state.Problem() != ProblemNone {
		t.Fatalf("problem = %q", state.Problem())
	}
}

func TestChooseTOTPQRCodeReportsUnusablePayloads(t *testing.T) {
	secret := totpStateSecret()
	tests := []struct {
		name        string
		picker      *fakeTOTPQRPicker
		wantProblem Problem
		wantOK      bool
	}{
		{"no totp payload", &fakeTOTPQRPicker{payloads: []string{"hello"}}, ProblemTOTP, false},
		{"hotp payload", &fakeTOTPQRPicker{payloads: []string{"otpauth://hotp/A?secret=" + secret}}, ProblemTOTP, false},
		{"decode error", &fakeTOTPQRPicker{err: errors.New("unreadable image")}, ProblemTOTPImage, false},
		{"cancelled", &fakeTOTPQRPicker{}, ProblemNone, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newTOTPState(t, test.picker, &fakeHosts{})
			state.BeginCreate()
			state.Form().TOTP = ""
			if got := state.ChooseTOTPQRCode(); got != test.wantOK {
				t.Fatalf("ChooseTOTPQRCode = %v, want %v", got, test.wantOK)
			}
			if state.Problem() != test.wantProblem {
				t.Fatalf("problem = %q, want %q", state.Problem(), test.wantProblem)
			}
			if state.Form().TOTP != "" {
				t.Fatalf("a failed import wrote %q", state.Form().TOTP)
			}
		})
	}
}

func TestChooseTOTPQRCodeNeedsTheHostEditor(t *testing.T) {
	picker := &fakeTOTPQRPicker{payloads: []string{"otpauth://totp/A?secret=" + totpStateSecret()}}
	state := newTOTPState(t, picker, &fakeHosts{})
	if state.ChooseTOTPQRCode() {
		t.Fatal("an import ran with no Host editor open")
	}
	if picker.calls != 0 {
		t.Fatalf("picker calls = %d, want 0", picker.calls)
	}
}

func TestSaveSubmitsTheTOTPSecretAndRejectsAMalformedOne(t *testing.T) {
	hosts := &fakeHosts{}
	state := newTOTPState(t, &fakeTOTPQRPicker{}, hosts)
	state.BeginCreate()
	form := state.Form()
	form.Hostname = "example.test"
	form.TOTP = totpStateSecret()
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(hosts.created) != 1 {
		t.Fatalf("created drafts = %+v", hosts.created)
	}
	// The fake records the pre-normalization draft, so canonicalize it the way
	// the application service would before asserting the stored form.
	normalized, err := domainvault.NormalizeDraft(hosts.created[0])
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if !strings.HasPrefix(normalized.TOTP, "otpauth://totp/") {
		t.Fatalf("submitted TOTP = %q, want a canonical otpauth URI", normalized.TOTP)
	}

	hosts = &fakeHosts{}
	state = newTOTPState(t, &fakeTOTPQRPicker{}, hosts)
	state.BeginCreate()
	state.Form().Hostname = "example.test"
	state.Form().TOTP = "not a secret"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemTOTP {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemTOTP)
	}
	if len(hosts.created) != 0 {
		t.Fatalf("a malformed authenticator was saved: %+v", hosts.created)
	}
}

func TestBeginEditKeepsTheStoredAuthenticatorAndDuplicateDropsIt(t *testing.T) {
	hosts := &fakeHosts{items: []domainvault.Host{{
		ID: model.HostID("host-1"), Label: "web", Hostname: "example.test",
		Protocol: domainvault.ProtocolSSH, HasTOTP: true,
	}}}
	state := newTOTPState(t, &fakeTOTPQRPicker{}, hosts)
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit: host not found")
	}
	form := state.Form()
	if !form.HasTOTP || form.TOTP != "" || form.ClearTOTP {
		t.Fatalf("edit form = %+v", form)
	}

	if !state.BeginDuplicate("host-1", "copy") {
		t.Fatal("BeginDuplicate: host not found")
	}
	form = state.Form()
	if form.HasTOTP || form.TOTP != "" || form.ClearTOTP {
		t.Fatalf("duplicate form = %+v", form)
	}
}
