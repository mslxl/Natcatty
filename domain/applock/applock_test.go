package applock

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestDefaultSettings(t *testing.T) {
	settings := DefaultSettings()
	if settings.Enabled {
		t.Error("App Lock is enabled by default")
	}
	if settings.TimeoutMinutes != 15 {
		t.Errorf("TimeoutMinutes = %d, want 15", settings.TimeoutMinutes)
	}
	if settings.PasswordVerifier != nil {
		t.Error("the default has a password verifier")
	}
	if settings.CanLock() {
		t.Error("the default settings can lock")
	}
}

func TestNormalizeTimeoutMinutesAcceptsOnlyTheOfferedValues(t *testing.T) {
	for _, minutes := range TimeoutOptions {
		settings := NormalizeSettings(Settings{TimeoutMinutes: minutes})
		if settings.TimeoutMinutes != minutes {
			t.Errorf("timeout %d normalized to %d", minutes, settings.TimeoutMinutes)
		}
	}
	for _, minutes := range []int{-1, 2, 14, 61, 1000} {
		settings := NormalizeSettings(Settings{TimeoutMinutes: minutes})
		if settings.TimeoutMinutes != DefaultTimeoutMinutes {
			t.Errorf("timeout %d normalized to %d, want %d", minutes, settings.TimeoutMinutes, DefaultTimeoutMinutes)
		}
	}
}

func TestNormalizeSettingsRequiresAVerifierToBeEnabled(t *testing.T) {
	settings := NormalizeSettings(Settings{
		Enabled:                       true,
		TimeoutMinutes:                5,
		SystemUnlockEnabled:           true,
		SystemUnlockAutoPromptEnabled: true,
	})
	if settings.Enabled {
		t.Error("settings without a verifier are enabled")
	}
	if settings.SystemUnlockEnabled || settings.SystemUnlockAutoPromptEnabled {
		t.Error("system unlock flags survived without a verifier")
	}
}

func TestNewVerifierProducesAVersionedPBKDF2Record(t *testing.T) {
	random := bytes.NewReader(bytes.Repeat([]byte{7}, SaltBytes))
	verifier, err := NewVerifier("correct horse", random)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if verifier.Version != VerifierVersion {
		t.Errorf("Version = %d, want %d", verifier.Version, VerifierVersion)
	}
	if verifier.Algorithm != Algorithm {
		t.Errorf("Algorithm = %q, want %q", verifier.Algorithm, Algorithm)
	}
	if verifier.Iterations != Iterations {
		t.Errorf("Iterations = %d, want %d", verifier.Iterations, Iterations)
	}
	if len(verifier.Salt) != SaltBytes {
		t.Errorf("salt is %d bytes, want %d", len(verifier.Salt), SaltBytes)
	}
	if len(verifier.Hash) != HashBytes {
		t.Errorf("hash is %d bytes, want %d", len(verifier.Hash), HashBytes)
	}
}

func TestVerifierAcceptsThePasswordItWasBuiltFrom(t *testing.T) {
	verifier, err := NewVerifier("s3cret", bytes.NewReader(bytes.Repeat([]byte{1}, SaltBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if !verifier.Verify("s3cret") {
		t.Error("the correct password was rejected")
	}
	if verifier.Verify("s3creT") {
		t.Error("a wrong password was accepted")
	}
	if verifier.Verify("") {
		t.Error("an empty password was accepted")
	}
}

func TestNewVerifierRejectsAnEmptyPassword(t *testing.T) {
	_, err := NewVerifier("", bytes.NewReader(bytes.Repeat([]byte{1}, SaltBytes)))
	if !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("NewVerifier error = %v, want ErrPasswordRequired", err)
	}
}

func TestVerifierJSONRoundTrips(t *testing.T) {
	verifier, err := NewVerifier("s3cret", bytes.NewReader(bytes.Repeat([]byte{9}, SaltBytes)))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(verifier)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Verifier
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal verifier: %v", err)
	}
	if !decoded.Verify("s3cret") {
		t.Error("the decoded verifier rejected the password")
	}
}

func TestNormalizeVerifierRejectsMalformedRecords(t *testing.T) {
	valid, err := NewVerifier("s3cret", bytes.NewReader(bytes.Repeat([]byte{2}, SaltBytes)))
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*Verifier){
		"wrong version":       func(v *Verifier) { v.Version = 2 },
		"wrong algorithm":     func(v *Verifier) { v.Algorithm = "sha256" },
		"too few iterations":  func(v *Verifier) { v.Iterations = 999 },
		"short salt":          func(v *Verifier) { v.Salt = v.Salt[:8] },
		"long salt":           func(v *Verifier) { v.Salt = append(v.Salt, 0) },
		"short hash":          func(v *Verifier) { v.Hash = v.Hash[:16] },
		"long hash":           func(v *Verifier) { v.Hash = append(v.Hash, 0) },
		"zero iterations":     func(v *Verifier) { v.Iterations = 0 },
		"negative iterations": func(v *Verifier) { v.Iterations = -1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copy := *valid
			copy.Salt = append([]byte(nil), valid.Salt...)
			copy.Hash = append([]byte(nil), valid.Hash...)
			mutate(&copy)
			if got := NormalizeVerifier(&copy); got != nil {
				t.Errorf("NormalizeVerifier accepted a malformed verifier: %+v", got)
			}
		})
	}
}

func TestNormalizeVerifierKeepsAValidRecord(t *testing.T) {
	valid, err := NewVerifier("s3cret", bytes.NewReader(bytes.Repeat([]byte{3}, SaltBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if got := NormalizeVerifier(valid); got == nil {
		t.Fatal("NormalizeVerifier rejected a valid verifier")
	}
}

func TestShouldLockAfterIdle(t *testing.T) {
	verifier, err := NewVerifier("s3cret", bytes.NewReader(bytes.Repeat([]byte{4}, SaltBytes)))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		settings Settings
		elapsed  int64
		want     bool
	}{
		{"disabled", DefaultSettings(), 3600, false},
		{"never", Settings{Enabled: true, TimeoutMinutes: 0, PasswordVerifier: verifier}, 3600, false},
		{"before the timeout", Settings{Enabled: true, TimeoutMinutes: 1, PasswordVerifier: verifier}, 59, false},
		{"at the timeout", Settings{Enabled: true, TimeoutMinutes: 1, PasswordVerifier: verifier}, 60, true},
		{"after the timeout", Settings{Enabled: true, TimeoutMinutes: 15, PasswordVerifier: verifier}, 901, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.settings.ShouldLockAfterIdle(test.elapsed); got != test.want {
				t.Errorf("ShouldLockAfterIdle(%d) = %v, want %v", test.elapsed, got, test.want)
			}
		})
	}
}
