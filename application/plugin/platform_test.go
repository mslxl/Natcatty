package plugin_test

import (
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

func TestSupportedPlatformMatrix(t *testing.T) {
	supported := map[string]bool{
		"darwin/arm64":  true,
		"darwin/amd64":  true,
		"windows/amd64": true,
		"linux/amd64":   true,
		"linux/arm64":   true,
	}
	for _, platform := range appplugin.SupportedPlatforms() {
		if !supported[platform] {
			t.Errorf("SupportedPlatforms() contains unexpected %q", platform)
		}
		delete(supported, platform)
	}
	if len(supported) != 0 {
		t.Errorf("SupportedPlatforms() is missing %v", supported)
	}
}

func TestSupportsPlatform(t *testing.T) {
	for _, platform := range []struct {
		goos   string
		goarch string
		want   bool
	}{
		{"darwin", "arm64", true},
		{"darwin", "amd64", true},
		{"windows", "amd64", true},
		{"linux", "amd64", true},
		{"linux", "arm64", true},
		{"windows", "arm64", false},
		{"linux", "386", false},
		{"linux", "arm", false},
		{"freebsd", "amd64", false},
		{"js", "wasm", false},
		{"", "", false},
	} {
		if got := appplugin.SupportsPlatform(platform.goos, platform.goarch); got != platform.want {
			t.Errorf("SupportsPlatform(%q, %q) = %v, want %v",
				platform.goos, platform.goarch, got, platform.want)
		}
	}
}
