package main

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// moduleImportPath is the Go module path for this checkout. Test references
// use full import paths; workflow fuzz packages use relative ones.
const moduleImportPath = "github.com/mslxl/natcatty"

// etPackage is the adapter package under test.
const etPackage = moduleImportPath + "/adapters/et"

// Pinned stock versions. The commit pins defend against a moved release tag.
const (
	v711  = "7.1.1"
	v6210 = "6.2.10"
)

// Cell is one matrix entry: a platform and the stock etserver version running
// there. Docker cells are started from adapters/et/lab/compose.yaml; native
// cells are provisioned by the runner (macOS) and must already be listening.
type Cell struct {
	// ID is the report key, e.g. "debian-12/7.1.1".
	ID string
	// Platform is the distribution or OS the server runs on.
	Platform string
	// Version is the stock etserver version the cell must prove it runs.
	Version string
	// Service is the compose service name, empty for a native cell.
	Service string
	// Native cells are not started by the harness; the runner provisions
	// them and the harness only runs the suites.
	Native bool
	// SSHAddr is the host:port the tests dial. Docker cells use the pinned
	// compose port; a native cell may be overridden with -lab-addr.
	SSHAddr string
	// GOOS and GOARCH are the expected platform of the test harness process
	// (runtime.GOOS/GOARCH), not of the stock server: the suites run on the
	// runner and dial the cell. Docker cells run on Linux amd64 runners;
	// macOS cells run natively on arm64 runners.
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	// BaseImageDigest pins the distribution base image digest used to build a
	// Docker cell. It is the full sha256 digest, not a prefix; compose.yaml
	// must reference the same digest.
	BaseImageDigest string `json:"baseImageDigest,omitempty"`
}

// Matrix is the complete required matrix. Adding a platform or version here
// requires a compose service (or a native runner) and makes `verify` demand a
// green report for it. Removing one weakens the gate and needs an ADR update.
var Matrix = []Cell{
	{
		ID: "debian-12/7.1.1", Platform: "debian-12", Version: v711,
		Service: "debian12-et711", SSHAddr: "127.0.0.1:2322",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3",
	},
	{
		ID: "debian-12/6.2.10", Platform: "debian-12", Version: v6210,
		Service: "debian12-et6210", SSHAddr: "127.0.0.1:2323",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3",
	},
	{
		ID: "ubuntu-24.04/7.1.1", Platform: "ubuntu-24.04", Version: v711,
		Service: "ubuntu2404-et711", SSHAddr: "127.0.0.1:2324",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852",
	},
	{
		ID: "ubuntu-24.04/6.2.10", Platform: "ubuntu-24.04", Version: v6210,
		Service: "ubuntu2404-et6210", SSHAddr: "127.0.0.1:2325",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852",
	},
	{
		ID: "fedora-41/7.1.1", Platform: "fedora-41", Version: v711,
		Service: "fedora41-et711", SSHAddr: "127.0.0.1:2326",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:f1a3fab47bcb3c3ddf3135d5ee7ba8b7b25f2e809a47440936212a3a50957f3d",
	},
	{
		ID: "fedora-41/6.2.10", Platform: "fedora-41", Version: v6210,
		Service: "fedora41-et6210", SSHAddr: "127.0.0.1:2327",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:f1a3fab47bcb3c3ddf3135d5ee7ba8b7b25f2e809a47440936212a3a50957f3d",
	},
	{
		ID: "arch-linux/7.1.1", Platform: "arch-linux", Version: v711,
		Service: "arch-et711", SSHAddr: "127.0.0.1:2328",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:767de6eaa40dcfeda5263a48737ea03a782caabdf3a403b6e6a203c7fd333681",
	},
	{
		ID: "arch-linux/6.2.10", Platform: "arch-linux", Version: v6210,
		Service: "arch-et6210", SSHAddr: "127.0.0.1:2329",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:767de6eaa40dcfeda5263a48737ea03a782caabdf3a403b6e6a203c7fd333681",
	},
	// macOS cells run natively: the runner builds stock etserver from the
	// pinned source and enables the system sshd. Both stock versions are
	// required, like every other platform.
	{
		ID: "macos/" + v711, Platform: "macos", Version: v711, Native: true,
		SSHAddr: "127.0.0.1:22", GOOS: "darwin", GOARCH: "arm64",
	},
	{
		ID: "macos/" + v6210, Platform: "macos", Version: v6210, Native: true,
		SSHAddr: "127.0.0.1:22", GOOS: "darwin", GOARCH: "arm64",
	},
}

// RequiredVersions is the stock version set every platform must cover.
var RequiredVersions = []string{v711, v6210}

// imageDigestPattern requires a complete sha256 digest: exactly 64 lowercase
// hex characters. Prefix matching is not enough because it cannot detect a
// changed or truncated pin.
var imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// findCell returns the matrix cell with id.
func findCell(id string) (Cell, bool) {
	for _, cell := range Matrix {
		if cell.ID == id {
			return cell, true
		}
	}
	return Cell{}, false
}

// cellsForPlatform returns the matrix cells for a platform.
func cellsForPlatform(platform string) []Cell {
	var cells []Cell
	for _, cell := range Matrix {
		if cell.Platform == platform {
			cells = append(cells, cell)
		}
	}
	return cells
}

// validateLoopbackAddress requires a usable loopback host:port. The gate must
// never run its suites against a remote endpoint: Docker cells are asserted to
// publish on the pinned loopback port and a native override may only point at
// the provisioned local sshd.
func validateLoopbackAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("address %q is not host:port: %w", address, err)
	}
	if host == "" {
		return fmt.Errorf("address %q has an empty host", address)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("address %q is not a loopback endpoint", address)
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("address %q has an invalid port", address)
	}
	return nil
}

// validateLabOverride enforces that -lab-addr is a native-cell-only, loopback
// override. Allowing it for a Docker cell would let the gate test an arbitrary
// endpoint while still labelling the report with the Docker cell.
func validateLabOverride(cell Cell, address string) error {
	if address == "" {
		return nil
	}
	if !cell.Native {
		return fmt.Errorf("-lab-addr is only valid for native cells; %s (%s) is a Docker cell",
			cell.ID, cell.Platform)
	}
	if err := validateLoopbackAddress(address); err != nil {
		return fmt.Errorf("-lab-addr %q: %w", address, err)
	}
	return nil
}

// validateMatrix checks the structural invariants the gate relies on: unique
// IDs, unique services and ports, pinned versions, both stock versions on
// every platform (including native ones), expected harness platforms, loopback
// endpoints, and full base-image digests.
func validateMatrix(cells []Cell) error {
	seenID := make(map[string]bool)
	seenService := make(map[string]bool)
	seenAddr := make(map[string]bool)
	for _, cell := range cells {
		if cell.ID == "" || cell.Platform == "" || cell.Version == "" {
			return fmt.Errorf("cell %+v has an empty ID, platform, or version", cell)
		}
		if cell.Version != v711 && cell.Version != v6210 {
			return fmt.Errorf("cell %s has unpinned version %q", cell.ID, cell.Version)
		}
		if seenID[cell.ID] {
			return fmt.Errorf("duplicate cell ID %q", cell.ID)
		}
		seenID[cell.ID] = true
		if cell.GOOS == "" || cell.GOARCH == "" {
			return fmt.Errorf("cell %s has no expected GOOS/GOARCH", cell.ID)
		}
		if err := validateLoopbackAddress(cell.SSHAddr); err != nil {
			return fmt.Errorf("cell %s SSH address: %w", cell.ID, err)
		}
		if cell.Native {
			if cell.Service != "" {
				return fmt.Errorf("native cell %s has a compose service", cell.ID)
			}
			if cell.GOOS != "darwin" || cell.GOARCH != "arm64" {
				return fmt.Errorf("native cell %s expects %s/%s, want darwin/arm64",
					cell.ID, cell.GOOS, cell.GOARCH)
			}
			if cell.BaseImageDigest != "" {
				return fmt.Errorf("native cell %s has a base image digest", cell.ID)
			}
			// Both macOS versions intentionally share the one local sshd
			// endpoint; only Docker cells must publish distinct ports.
			continue
		}
		if cell.Service == "" {
			return fmt.Errorf("docker cell %s has no compose service", cell.ID)
		}
		if cell.GOOS != "linux" || cell.GOARCH != "amd64" {
			return fmt.Errorf("docker cell %s expects %s/%s, want linux/amd64",
				cell.ID, cell.GOOS, cell.GOARCH)
		}
		if !imageDigestPattern.MatchString(cell.BaseImageDigest) {
			return fmt.Errorf("docker cell %s base image digest %q is not a full sha256 digest",
				cell.ID, cell.BaseImageDigest)
		}
		if seenService[cell.Service] {
			return fmt.Errorf("duplicate compose service %q", cell.Service)
		}
		seenService[cell.Service] = true
		if seenAddr[cell.SSHAddr] {
			return fmt.Errorf("duplicate lab address %q", cell.SSHAddr)
		}
		seenAddr[cell.SSHAddr] = true
	}
	platforms := make(map[string]bool)
	for _, cell := range cells {
		platforms[cell.Platform] = true
	}
	for platform := range platforms {
		var versions []string
		for _, cell := range cellsForPlatform(platform) {
			versions = append(versions, cell.Version)
		}
		slices.Sort(versions)
		want := slices.Clone(RequiredVersions)
		slices.Sort(want)
		if !slices.Equal(versions, want) {
			return fmt.Errorf("platform %s covers versions %v, want %v", platform, versions, want)
		}
	}
	return nil
}

// CellEnv returns the lab environment for a cell. It deliberately sets only
// the one version variable the adapter tests read, plus NATCATTY_ET_LAB_VERSION
// so the tests loop over exactly the cell's server.
func CellEnv(cell Cell, labAddr string) []string {
	env := []string{
		"NATCATTY_ET_LAB=1",
		"NATCATTY_ET_LAB_VERSION=" + cell.Version,
		"NATCATTY_ET_LAB_EXPECT_VERSION=" + cell.Version,
		"NATCATTY_ET_LAB_USER=tester",
		"NATCATTY_ET_LAB_PASSWORD=testpass",
		"NATCATTY_ET_LAB_JUMP=" + labAddr,
		"NATCATTY_ET_LAB_JUMP_TARGET=127.0.0.1:22",
		"NATCATTY_ET_LAB_JUMP_USER=tester",
		"NATCATTY_ET_LAB_JUMP_PASSWORD=testpass",
	}
	if cell.Version == v711 {
		env = append(env, "NATCATTY_ET_LAB_711="+labAddr)
	} else {
		env = append(env, "NATCATTY_ET_LAB_6210="+labAddr)
	}
	return env
}

// SkipForVersion lists lab tests that belong to the other stock version's
// behavior (for example, the reset attach flow only exists in v7). They are
// skipped, not waived: the other version's cell runs the corresponding
// behavior with its own test.
func SkipForVersion(version string) []string {
	switch version {
	case v711:
		return []string{
			"TestInProcessLegacyReattachFailsLikeStock6210",
		}
	case v6210:
		return []string{
			"TestInProcessAttachDisplacesFirstClientAgainstStock711",
			"TestInProcessOfflineVictimIsDisplacedAgainstStock711",
			"TestRetryLaterDrivesBackoffAgainstStock711",
		}
	default:
		return nil
	}
}

func skipPattern(version string) string {
	names := SkipForVersion(version)
	if len(names) == 0 {
		return ""
	}
	return "^(" + strings.Join(names, "|") + ")$"
}
