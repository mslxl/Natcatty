package plugin

// supportedPlatforms is the v1 plugin runtime matrix from ADR-0018. Rosetta is
// darwin/amd64, not a separate platform. Windows arm64, Linux 386/armv7, and
// BSD are deliberately absent: there the app still runs, but plugins report
// unavailable instead of attempting a best-effort engine.
var supportedPlatforms = []string{
	"darwin/arm64",
	"darwin/amd64",
	"windows/amd64",
	"linux/amd64",
	"linux/arm64",
}

// SupportedPlatforms returns a copy of the goos/goarch plugin matrix.
func SupportedPlatforms() []string {
	return append([]string(nil), supportedPlatforms...)
}

// SupportsPlatform reports whether third-party plugins are supported on
// goos/goarch. An unsupported target reports plugins unavailable and leaves
// the rest of the app working.
func SupportsPlatform(goos, goarch string) bool {
	for _, platform := range supportedPlatforms {
		if platform == goos+"/"+goarch {
			return true
		}
	}
	return false
}
