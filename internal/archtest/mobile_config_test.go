package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
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
