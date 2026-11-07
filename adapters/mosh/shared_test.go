// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"errors"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
)

// waitFor polls a real condition with a hard deadline; it never sleeps for a
// fixed synchronization interval.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCheckRoute(t *testing.T) {
	if err := CheckRoute(0, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckRoute(1, false); !errors.Is(err, moshmodel.ErrJumpUnsupported) {
		t.Fatalf("jump error = %v", err)
	}
	if err := CheckRoute(0, true); !errors.Is(err, moshmodel.ErrProxyUnsupported) {
		t.Fatalf("proxy error = %v", err)
	}
}

// TestConfigFromEnvironmentCarriesLocale pins the bootstrap environment merge
// independently of any client process: the SSH command and the announced
// locale must see the caller's UTF-8 locale, and unrelated variables must not
// leak into the mosh locale set.
func TestConfigFromEnvironmentCarriesLocale(t *testing.T) {
	spec := appsession.StartSpec{
		Cols: 100,
		Rows: 40,
		Mosh: &moshmodel.Config{Hostname: "example.test"},
	}
	cfg := configFromEnvironment(spec, []string{
		"LANG=de_DE.UTF-8",
		"LC_ALL=de_DE.UTF-8",
		"LC_TIME=C",
		"PATH=/bin",
	})
	if cfg.Lang != "de_DE.UTF-8" {
		t.Fatalf("bootstrap language = %q, want the LC_ALL locale", cfg.Lang)
	}
	if got := cfg.Locales["LANG"]; got != "de_DE.UTF-8" {
		t.Fatalf("LANG locale = %q", got)
	}
	if got := cfg.Locales["LC_TIME"]; got != "C" {
		t.Fatalf("LC_TIME locale = %q", got)
	}
	if _, ok := cfg.Locales["PATH"]; ok {
		t.Fatal("a non-locale variable entered the locale set")
	}
	if cfg.Cols != 100 || cfg.Rows != 40 {
		t.Fatalf("size = %dx%d, want 100x40", cfg.Cols, cfg.Rows)
	}
}

// TestConfigFromEnvironmentKeepsAnExplicitLanguage checks a Session that names
// its language does not have it replaced by the process locale.
func TestConfigFromEnvironmentKeepsAnExplicitLanguage(t *testing.T) {
	spec := appsession.StartSpec{
		Mosh: &moshmodel.Config{Hostname: "example.test", Lang: "fr_FR.UTF-8"},
	}
	cfg := configFromEnvironment(spec, []string{"LC_ALL=de_DE.UTF-8"})
	if cfg.Lang != "fr_FR.UTF-8" {
		t.Fatalf("explicit language = %q, want fr_FR.UTF-8", cfg.Lang)
	}
	if got := cfg.Locales["LC_ALL"]; got != "de_DE.UTF-8" {
		t.Fatalf("LC_ALL locale = %q", got)
	}
}

func TestMergeEnvOverridesWithoutReordering(t *testing.T) {
	got := mergeEnv(
		[]string{"LANG=en_US.UTF-8", "PATH=/bin"},
		[]string{"LANG=de_DE.UTF-8", "TERM=xterm-256color", "MALFORMED"},
	)
	if value := envValue(got, "LANG"); value != "de_DE.UTF-8" {
		t.Fatalf("LANG = %q, want the override", value)
	}
	if value := envValue(got, "TERM"); value != "xterm-256color" {
		t.Fatalf("TERM = %q, want the added value", value)
	}
	if len(got) != 3 || got[0] != "LANG=de_DE.UTF-8" || got[1] != "PATH=/bin" {
		t.Fatalf("environment = %#v, want the override in place and no malformed entry", got)
	}
}
