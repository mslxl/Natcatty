// SPDX-License-Identifier: GPL-3.0-or-later

// Command moshparity runs the mosh parity gate: it starts real stock
// mosh-server cells from the pinned matrix, runs the required behavior suites
// against each cell, and writes one pass/fail report per cell. `verify` fails
// closed and is the signal ticket 34's default switch reads.
package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Cell kinds. Docker cells are started by the harness from adapters/mosh/lab.
// Native cells are provisioned by the platform runner (the macOS and FreeBSD
// CI jobs, or a developer's documented lab); the harness only waits for their
// SSH endpoint and runs the suites. The stock cell has no server: it is the
// stock mosh-client display differential.
const (
	kindDocker  = "docker"
	kindMacOS   = "macos"
	kindFreeBSD = "freebsd"
	kindStock   = "stock"
)

// Host operating systems a report can be produced on. Docker and stock cells
// exercise Linux container images; native cells must be produced on their own
// OS, which the harness asserts before running anything.
const (
	hostOSLinux   = "linux"
	hostOSDarwin  = "darwin"
	hostOSFreeBSD = "freebsd"
)

// Pinned stock versions. Every mosh-server cell must prove one of these exact
// versions; the release tarball SHA-256s are pinned in adapters/mosh/lab.
const (
	v132 = "1.3.2"
	v140 = "1.4.0"
)

// Cell is one matrix entry.
type Cell struct {
	// ID is the report key, e.g. "debian-12/1.3.2" or "stock-client/1.4.0".
	ID string `json:"id"`
	// Platform is the distribution or OS the server runs on.
	Platform string `json:"platform"`
	// Version is the stock mosh-server or mosh-client version the cell proves.
	Version string `json:"version"`
	// Kind selects how the cell is provisioned.
	Kind string `json:"kind"`
	// Image is the Docker image for docker/stock cells.
	Image string `json:"image,omitempty"`
	// Dockerfile and BaseImage are the build recipe for docker cells.
	Dockerfile string `json:"dockerfile,omitempty"`
	BaseImage  string `json:"baseImage,omitempty"`
	// MOSHSHA256 pins the release tarball compiled into the image.
	MOSHSHA256 string `json:"moshSha256,omitempty"`
	// SSHAddr is the host:port the tests dial for server cells.
	SSHAddr string `json:"sshAddr,omitempty"`
	// ServerPath is the wrapper binary on the remote host that execs the
	// pinned mosh-server with the cell's forced UDP port.
	ServerPath string `json:"serverPath,omitempty"`
	// UDPPort is the published/forced mosh-server port.
	UDPPort int `json:"udpPort,omitempty"`
}

// SSHHost returns the cell's SSH host without its port.
func (c Cell) SSHHost() string {
	host, _, err := net.SplitHostPort(c.SSHAddr)
	if err != nil {
		return c.SSHAddr
	}
	return host
}

// SSHHostPort returns the cell's SSH port, defaulting to 22.
func (c Cell) SSHHostPort() int {
	_, port, err := net.SplitHostPort(c.SSHAddr)
	if err != nil {
		return 22
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		return 22
	}
	return value
}

// IsNative reports whether the cell runs on a provisioned native host (macOS
// or FreeBSD) rather than a Linux container.
func (c Cell) IsNative() bool {
	return c.Kind == kindMacOS || c.Kind == kindFreeBSD
}

// ExpectedLabOS is the platform the exercised endpoint must run: Linux for
// Docker and stock cells, and the host OS for native cells. `verify` requires
// every report's recorded lab platform to match, so pointing -lab-addr at an
// unrelated host cannot produce a report that passes.
func (c Cell) ExpectedLabOS() string {
	switch c.Kind {
	case kindDocker, kindStock:
		return hostOSLinux
	case kindMacOS:
		return hostOSDarwin
	case kindFreeBSD:
		return hostOSFreeBSD
	}
	return ""
}

// Matrix is the complete required matrix. Adding a platform or version here
// makes `verify` demand a green report for it; removing one weakens the gate
// and requires an ADR update. Debian 12 and Ubuntu 22.04 are current LTS
// installations available to the container runner; Fedora 41 is the pinned
// container distribution; macOS is the Homebrew runner; FreeBSD 14 is the
// documented VM runner.
var Matrix = []Cell{
	{
		ID: "debian-12/" + v132, Platform: "debian-12", Version: v132, Kind: kindDocker,
		Image: "natcatty/mosh-lab-debian12:" + v132, Dockerfile: "Dockerfile.debian",
		BaseImage: "debian:12", MOSHSHA256: sha132,
		SSHAddr: "127.0.0.1:2451", ServerPath: "/usr/local/bin/mosh-lab-server", UDPPort: 24601,
	},
	{
		ID: "debian-12/" + v140, Platform: "debian-12", Version: v140, Kind: kindDocker,
		Image: "natcatty/mosh-lab-debian12:" + v140, Dockerfile: "Dockerfile.debian",
		BaseImage: "debian:12", MOSHSHA256: sha140,
		SSHAddr: "127.0.0.1:2452", ServerPath: "/usr/local/bin/mosh-lab-server", UDPPort: 24602,
	},
	{
		ID: "ubuntu-22.04/" + v132, Platform: "ubuntu-22.04", Version: v132, Kind: kindDocker,
		Image: "natcatty/mosh-lab-ubuntu2204:" + v132, Dockerfile: "Dockerfile.debian",
		BaseImage: "ubuntu:22.04", MOSHSHA256: sha132,
		SSHAddr: "127.0.0.1:2453", ServerPath: "/usr/local/bin/mosh-lab-server", UDPPort: 24603,
	},
	{
		ID: "ubuntu-22.04/" + v140, Platform: "ubuntu-22.04", Version: v140, Kind: kindDocker,
		Image: "natcatty/mosh-lab-ubuntu2204:" + v140, Dockerfile: "Dockerfile.debian",
		BaseImage: "ubuntu:22.04", MOSHSHA256: sha140,
		SSHAddr: "127.0.0.1:2454", ServerPath: "/usr/local/bin/mosh-lab-server", UDPPort: 24604,
	},
	{
		ID: "fedora-41/" + v132, Platform: "fedora-41", Version: v132, Kind: kindDocker,
		Image: "natcatty/mosh-lab-fedora41:" + v132, Dockerfile: "Dockerfile.fedora",
		BaseImage: "fedora:41", MOSHSHA256: sha132,
		SSHAddr: "127.0.0.1:2455", ServerPath: "/usr/local/bin/mosh-lab-server", UDPPort: 24605,
	},
	{
		ID: "fedora-41/" + v140, Platform: "fedora-41", Version: v140, Kind: kindDocker,
		Image: "natcatty/mosh-lab-fedora41:" + v140, Dockerfile: "Dockerfile.fedora",
		BaseImage: "fedora:41", MOSHSHA256: sha140,
		SSHAddr: "127.0.0.1:2456", ServerPath: "/usr/local/bin/mosh-lab-server", UDPPort: 24606,
	},
	// macOS: the runner installs Homebrew mosh (1.4.0) and builds the pinned
	// 1.3.2 release; one sshd serves both cells through per-version wrappers.
	{
		ID: "macos/" + v140, Platform: "macos", Version: v140, Kind: kindMacOS,
		SSHAddr: "127.0.0.1:2457", ServerPath: "/tmp/natcatty-mosh-lab/mosh-1.4.0", UDPPort: 24607,
	},
	{
		ID: "macos/" + v132, Platform: "macos", Version: v132, Kind: kindMacOS,
		SSHAddr: "127.0.0.1:2457", ServerPath: "/tmp/natcatty-mosh-lab/mosh-1.3.2", UDPPort: 24608,
	},
	// FreeBSD: the runner provisions both pinned releases behind wrappers.
	{
		ID: "freebsd-14/" + v132, Platform: "freebsd-14", Version: v132, Kind: kindFreeBSD,
		SSHAddr: "127.0.0.1:2458", ServerPath: "/usr/local/bin/mosh-1.3.2", UDPPort: 24609,
	},
	{
		ID: "freebsd-14/" + v140, Platform: "freebsd-14", Version: v140, Kind: kindFreeBSD,
		SSHAddr: "127.0.0.1:2458", ServerPath: "/usr/local/bin/mosh-1.4.0", UDPPort: 24610,
	},
	// Stock mosh-client display differential. It needs Docker (the pinned
	// parity image), so it is its own report entry instead of being repeated
	// inside every native cell.
	{
		ID: "stock-client/" + v140, Platform: "stock-client", Version: v140, Kind: kindStock,
		Image: "natcatty/mosh-parity:1.4.0",
	},
}

// Pinned release tarball hashes. Keep in sync with adapters/mosh/lab/README.md.
const (
	sha132 = "da600573dfa827d88ce114e0fed30210689381bbdcff543c931e4d6a2e851216"
	sha140 = "872e4b134e5df29c8933dff12350785054d2fd2839b5ae6b5587b14db1465ddd"
)

// RequiredVersions is the stock version set every non-stock platform must
// cover.
var RequiredVersions = []string{v132, v140}

func findCell(id string) (Cell, bool) {
	for _, cell := range Matrix {
		if cell.ID == id {
			return cell, true
		}
	}
	return Cell{}, false
}

// validateMatrix checks the structural invariants the gate relies on.
func validateMatrix(cells []Cell) error {
	seenID := make(map[string]bool)
	seenAddr := make(map[string]string)
	for _, cell := range cells {
		if cell.ID == "" || cell.Platform == "" || cell.Version == "" {
			return fmt.Errorf("cell %+v has an empty ID, platform, or version", cell)
		}
		if cell.Version != v132 && cell.Version != v140 {
			return fmt.Errorf("cell %s has unpinned version %q", cell.ID, cell.Version)
		}
		if seenID[cell.ID] {
			return fmt.Errorf("duplicate cell ID %q", cell.ID)
		}
		seenID[cell.ID] = true
		switch cell.Kind {
		case kindDocker:
			if cell.Image == "" || cell.Dockerfile == "" || cell.BaseImage == "" || cell.MOSHSHA256 == "" {
				return fmt.Errorf("docker cell %s is missing its build recipe", cell.ID)
			}
			if cell.SSHAddr == "" || cell.ServerPath == "" || cell.UDPPort < 1 {
				return fmt.Errorf("docker cell %s is missing its lab endpoint", cell.ID)
			}
		case kindMacOS, kindFreeBSD:
			if cell.SSHAddr == "" || cell.ServerPath == "" || cell.UDPPort < 1 {
				return fmt.Errorf("native cell %s is missing its lab endpoint", cell.ID)
			}
		case kindStock:
			if cell.Image == "" {
				return fmt.Errorf("stock cell %s is missing its parity image", cell.ID)
			}
		default:
			return fmt.Errorf("cell %s has unknown kind %q", cell.ID, cell.Kind)
		}
		if cell.ExpectedLabOS() == "" {
			return fmt.Errorf("cell %s has no expected lab platform", cell.ID)
		}
		if cell.SSHAddr != "" {
			key := cell.SSHAddr
			if other, ok := seenAddr[key]; ok {
				// A shared sshd is legal only when the cells differ by server
				// wrapper (macOS and FreeBSD). Distinct UDP ports keep the
				// servers apart.
				if cell.Kind != kindMacOS && cell.Kind != kindFreeBSD {
					return fmt.Errorf("cells %s and %s share SSH address %s", other, cell.ID, key)
				}
			}
			seenAddr[key] = cell.ID
		}
	}
	platforms := make(map[string]bool)
	for _, cell := range cells {
		if cell.Kind != kindStock {
			platforms[cell.Platform] = true
		}
	}
	for platform := range platforms {
		var versions []string
		for _, cell := range cells {
			if cell.Platform == platform {
				versions = append(versions, cell.Version)
			}
		}
		if len(versions) != 2 || versions[0] == versions[1] {
			return fmt.Errorf("platform %s covers versions %v, want both pinned versions", platform, versions)
		}
		for _, want := range RequiredVersions {
			found := false
			for _, got := range versions {
				if got == want {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("platform %s does not cover mosh-server %s", platform, want)
			}
		}
	}
	return nil
}

// suitesForCell returns the suite names a cell must run.
func suitesForCell(cell Cell) []string {
	if cell.Kind == kindStock {
		return []string{suiteStock}
	}
	return []string{suiteUnit, suiteLab, suiteFuzz}
}

// cellIdentity matches a report to its matrix entry. Image and endpoint are
// included so a report from a differently provisioned cell cannot pass.
func cellIdentity(cell Cell) string {
	parts := []string{cell.ID, cell.Platform, cell.Version, cell.Kind, cell.Image, cell.SSHAddr,
		cell.ServerPath, strconv.Itoa(cell.UDPPort)}
	return strings.Join(parts, "|")
}
