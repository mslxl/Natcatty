package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
)

// TestMobileConfigurationDeclaresMinimums pins the phone deployment targets in
// the committed configuration. The minimums are product decisions: the iOS
// shell ticket fixed iOS 17.0 and this ticket fixes Android API 26. MyGo would
// silently fall back to those same defaults if the keys were removed, so the
// test reads the file itself and requires the declaration rather than trusting
// a default: lowering either one is then a visible edit to this test.
func TestMobileConfigurationDeclaresMinimums(t *testing.T) {
	root := testutil.ModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "mygo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		IOS struct {
			MinimumSystemVersion string `json:"minimumSystemVersion"`
		} `json:"ios"`
		Android struct {
			// A pointer so a missing declaration is not mistaken for 0,
			// which MyGo would replace with its default.
			MinSdk *int `json:"minSdk"`
		} `json:"android"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("mygo.json: %v", err)
	}

	if got := config.IOS.MinimumSystemVersion; got != "17.0" {
		t.Errorf("ios.minimumSystemVersion = %q, want \"17.0\"", got)
	}
	if config.Android.MinSdk == nil {
		t.Fatal("android.minSdk is not declared; MyGo's implicit default is not a committed decision")
	}
	if got := *config.Android.MinSdk; got != 26 {
		t.Errorf("android.minSdk = %d, want 26 (Android 8.0)", got)
	}
}

// TestMobileConfigurationRegistersSSHDeepLinks pins the committed registration
// of the ssh:// handler under the single application identifier. MyGo v0.10.0
// turns urlSchemes into the iOS bundle's CFBundleURLTypes (CFBundleURLName is
// the identifier) and into a VIEW intent-filter for the same scheme in the
// Android manifest, so these two committed values are the handler registration
// on both platforms. Removing either would make links open another app or no
// app; the test makes that a visible edit.
func TestMobileConfigurationRegistersSSHDeepLinks(t *testing.T) {
	root := testutil.ModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "mygo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Identifier string   `json:"identifier"`
		URLSchemes []string `json:"urlSchemes"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("mygo.json: %v", err)
	}

	if config.Identifier != "com.mslxl.natcatty" {
		t.Errorf("identifier = %q, want the single application identifier %q",
			config.Identifier, "com.mslxl.natcatty")
	}
	if !slices.Contains(config.URLSchemes, "ssh") {
		t.Errorf("urlSchemes = %v, want it to declare %q for ssh:// deep links",
			config.URLSchemes, "ssh")
	}
}
