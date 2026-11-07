// SPDX-License-Identifier: GPL-3.0-or-later

// Package legal names the license and notice documents every Natcatty
// distribution must carry, builds the per-release Corresponding Source offer,
// and checks a repository checkout or a packaged artifact for them.
//
// The checks are mechanical: they prove the documents are present and name the
// expected components. They are not a legal opinion and do not replace the
// independent provenance audit and human/legal review tracked by tickets 15
// and 19.
package legal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// RepositoryURL is the public source repository.
	RepositoryURL = "https://github.com/mslxl/natcatty"
	// LicenseFile is the full GPLv3 text carried at the repository root and in
	// every packaged artifact.
	LicenseFile = "LICENSE"
	// NoticeFile is the modification and copyright notice.
	NoticeFile = "NOTICE"
	// ThirdPartyNoticesFile is the complete third-party notice document.
	ThirdPartyNoticesFile = "THIRD-PARTY-NOTICES.md"
	// SourceOfferFile is the per-release Corresponding Source statement in a
	// packaged artifact.
	SourceOfferFile = "SOURCE-OFFER.md"
)

// SourceTagURL returns the repository URL for a release tag. A development
// build has no release tag, so it falls back to the repository root instead of
// pointing at a nonexistent vdev tag.
func SourceTagURL(version string) string {
	version = normalizeVersion(version)
	if version == "" || version == "dev" {
		return RepositoryURL
	}
	return RepositoryURL + "/tree/v" + version
}

// SourceOffer returns the Markdown Corresponding Source section carried by the
// standalone SOURCE-OFFER.md document.
func SourceOffer(version string) string {
	version = normalizeVersion(version)
	return fmt.Sprintf(`## Corresponding Source

Natcatty %[1]s is free software distributed under the GNU General Public
License, version 3 or later (GPL-3.0-or-later). The complete Corresponding
Source for this release is the repository at the release tag:

- %[2]s

Build instructions are in `+"`docs/packaging.md`"+`. The license text is in
`+"`LICENSE`"+`, the modification and copyright notice is in `+"`NOTICE`"+`, and the
third-party components with their sources are in
`+"`THIRD-PARTY-NOTICES.md`"+`.

In-process transport clients:

- The Mosh client is part of this repository's source under `+"`internal/mosh`"+` and
  `+"`adapters/mosh`"+`. Its terminal model is a Go port of stock mosh 1.4.0
  (<https://github.com/mobile-shell/mosh/releases/download/mosh-1.4.0/mosh-1.4.0.tar.gz>);
  its OCB3 implementation is adapted from
  <https://github.com/unixshells/mosh-go> at commit
  `+"`0284bd652aab3dfff5b0b74945c23901d7537b61`"+` (MIT; Copyright (c) 2026 Unix Shells).
  No separate `+"`mosh-client`"+` binary is bundled or launched.
- The EternalTerminal client is part of this repository's source under
  `+"`adapters/et`"+`. Its protocol core is vendored from
  <https://github.com/tphakala/et-go> at commit
  `+"`bc33dd76b558418bc1079ba27c8c477b8145eea5`"+` (Apache-2.0;
  Copyright 2026 Tomi P. Hakala).
  No separate `+"`et`"+` binary is bundled or launched.

This program is distributed in the hope that it will be useful, but WITHOUT
ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS
FOR A PARTICULAR PURPOSE. See the GNU General Public License for more details.
`, version, SourceTagURL(version))
}

// SourceOfferDocument returns the standalone SOURCE-OFFER.md document for a
// packaged artifact.
func SourceOfferDocument(version string) string {
	version = normalizeVersion(version)
	return "# Natcatty " + version + " — Corresponding Source\n\n" + SourceOffer(version)
}

func normalizeVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

type documentCheck struct {
	path    string
	markers []string
}

func licenseChecks() []documentCheck {
	return []documentCheck{
		{LicenseFile, []string{"GNU GENERAL PUBLIC LICENSE", "Version 3, 29 June 2007"}},
		{NoticeFile, []string{"GPL-3.0-or-later", "modified version"}},
		{filepath.Join("resources", ThirdPartyNoticesFile), []string{
			"MyGo", "libghostty-vt", "EternalTerminal", "Netcatty",
			"mosh-go", "0284bd652aab3dfff5b0b74945c23901d7537b61",
		}},
	}
}

// CheckRepository verifies the legal documents in a repository checkout and
// that mygo.json ships LICENSE and NOTICE with every artifact.
func CheckRepository(root string) error {
	for _, check := range licenseChecks() {
		if err := checkDocument(root, check); err != nil {
			return err
		}
	}
	return checkManifestResources(root)
}

// CheckPackaged verifies the legal documents in a packaged artifact. The
// artifact's mygo.json supplies the version the source offer must name.
func CheckPackaged(root string) error {
	for _, check := range licenseChecks() {
		path := check.path
		if path != filepath.Join("resources", ThirdPartyNoticesFile) {
			// Packaged artifacts place the notices next to the executable.
			path = filepath.Base(path)
		} else {
			path = ThirdPartyNoticesFile
		}
		check.path = path
		if err := checkDocument(root, check); err != nil {
			return err
		}
	}
	version, err := manifestVersion(filepath.Join(root, "mygo.json"))
	if err != nil {
		return err
	}
	if err := checkDocument(root, documentCheck{
		path:    SourceOfferFile,
		markers: []string{"Corresponding Source", SourceTagURL(version), "GPL-3.0-or-later"},
	}); err != nil {
		return err
	}
	return nil
}

func checkDocument(root string, check documentCheck) error {
	path := filepath.Join(root, check.path)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("legal document %s: %w", check.path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return fmt.Errorf("legal document %s is empty", check.path)
	}
	for _, marker := range check.markers {
		if !strings.Contains(string(data), marker) {
			return fmt.Errorf("legal document %s does not contain %q", check.path, marker)
		}
	}
	return nil
}

func checkManifestResources(root string) error {
	path := filepath.Join(root, "mygo.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read mygo.json: %w", err)
	}
	var manifest struct {
		Resources []string `json:"resources"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse mygo.json: %w", err)
	}
	for _, required := range []string{LicenseFile, NoticeFile} {
		found := false
		for _, resource := range manifest.Resources {
			if filepath.Clean(resource) == required {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("mygo.json does not ship %s in its resources", required)
		}
	}
	return nil
}

func manifestVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if manifest.Version == "" {
		return "", fmt.Errorf("%s has no version", path)
	}
	return manifest.Version, nil
}
