package terminal

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/mslxl/natcatty/ui/terminal/mobile"
)

// TestArtifactsMatchBindings keeps the committed artifact manifest and the
// version the phone backend asserts at startup from drifting apart: a phone
// stages an object built from this manifest, and Load fails when the library
// reports anything but mobile.Version.
func TestArtifactsMatchBindings(t *testing.T) {
	data, err := os.ReadFile("artifacts.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Commit     string `json:"commit"`
		LibVersion string `json:"libVersion"`
		Artifacts  map[string]struct {
			Commit string `json:"commit"`
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.LibVersion != mobile.Version {
		t.Errorf("artifacts.json libVersion = %q, mobile.Version = %q", m.LibVersion, mobile.Version)
	}
	want := []string{"ios-arm64", "ios-arm64-simulator", "android-arm64-v8a", "android-armeabi-v7a", "android-x86_64"}
	for _, key := range want {
		a, ok := m.Artifacts[key]
		if !ok {
			t.Errorf("artifacts.json has no %s", key)
			continue
		}
		if a.Commit != m.Commit {
			t.Errorf("%s was built from %s, manifest pins %s", key, a.Commit, m.Commit)
		}
		if a.SHA256 == "" {
			t.Errorf("%s has no sha256", key)
		}
	}
}
