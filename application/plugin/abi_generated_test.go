package plugin_test

import (
	"errors"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// TestManifestABIsMatchTheGeneratedIDL keeps the host manifest allowlist and
// the IDL from drifting apart.
func TestManifestABIsMatchTheGeneratedIDL(t *testing.T) {
	found := false
	for _, abi := range domainplugin.SupportedABIs {
		if abi == domainpluginabi.Version {
			found = true
		}
	}
	if !found {
		t.Fatalf("SupportedABIs = %v, generated IDL version %d is missing",
			domainplugin.SupportedABIs, domainpluginabi.Version)
	}
}

func TestGeneratedABIVersionCheckIsTyped(t *testing.T) {
	if err := domainpluginabi.CheckABIVersion(domainpluginabi.Version); err != nil {
		t.Fatalf("CheckABIVersion(%d) = %v", domainpluginabi.Version, err)
	}
	err := domainpluginabi.CheckABIVersion(domainpluginabi.Version + 1)
	var mismatch *domainpluginabi.ABIMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("CheckABIVersion error = %v, want ABIMismatchError", err)
	}
	if mismatch.Want != domainpluginabi.Version || mismatch.Got != domainpluginabi.Version+1 {
		t.Fatalf("mismatch = want %d got %d", mismatch.Want, mismatch.Got)
	}
	// The application port keeps the stable alias the runtime returns.
	var alias *appplugin.ABIVersionError
	if !errors.As(err, &alias) {
		t.Fatal("the application ABIVersionError alias does not match")
	}
}

func TestGeneratedHostImportAllowlist(t *testing.T) {
	for _, name := range []string{
		domainpluginabi.HostFunctionABIVersion,
		domainpluginabi.HostFunctionLog,
		domainpluginabi.HostFunctionNowNs,
		domainpluginabi.HostFunctionRandomGet,
		domainpluginabi.HostFunctionOutputWrite,
		domainpluginabi.HostFunctionLanguageRespond,
		domainpluginabi.HostFunctionLanguageCancelled,
	} {
		if !domainpluginabi.AllowsHostImport(domainpluginabi.HostModuleName, name) {
			t.Errorf("AllowsHostImport(%q) = false", name)
		}
	}
	if domainpluginabi.AllowsHostImport(domainpluginabi.HostModuleName, "request") {
		t.Error("the v1 allowlist accepted a later-ticket import")
	}
	if domainpluginabi.AllowsHostImport("wasi_snapshot_preview1", "fd_write") {
		t.Error("the v1 allowlist accepted WASI")
	}
}

func TestGeneratedEnumAndHandle(t *testing.T) {
	if !appplugin.LogInfo.Valid() {
		t.Fatal("info is not a valid level")
	}
	if appplugin.LogLevel(99).Valid() {
		t.Fatal("99 is a valid level")
	}
	if got := appplugin.LogWarn.String(); got != "warn" {
		t.Fatalf("warn string = %q", got)
	}
	handle := appplugin.Handle(uint64(7)<<32 | 3)
	if handle.Slot() != 3 || handle.Generation() != 7 {
		t.Fatalf("handle = slot %d generation %d", handle.Slot(), handle.Generation())
	}
}
