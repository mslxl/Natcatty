package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// manifest is the checked-in record of what was built: the source commit,
// the toolchain, and each artifact's release URL, size and sha256. It holds
// no binary; the artifacts are release assets.
type manifest struct {
	Schema     int                 `json:"schema"`
	Name       string              `json:"name"`
	Upstream   string              `json:"upstream"`
	Commit     string              `json:"commit"`
	LibVersion string              `json:"libVersion"`
	Zig        string              `json:"zig"`
	NDK        string              `json:"ndk,omitempty"`
	Optimize   string              `json:"optimize"`
	Strip      bool                `json:"strip"`
	Release    string              `json:"release"`
	Artifacts  map[string]artifact `json:"artifacts"`
}

type artifact struct {
	File   string `json:"file"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Commit string `json:"commit"`
}

func newManifest(releaseURL string) manifest {
	return manifest{
		Schema:     manifestSchema,
		Name:       "libghostty-vt",
		Upstream:   upstreamURL,
		Commit:     pinnedCommit,
		LibVersion: libVersion,
		Zig:        zigVersion,
		Optimize:   "ReleaseFast",
		Strip:      true,
		Release:    strings.TrimSuffix(releaseURL, "/"),
		Artifacts:  map[string]artifact{},
	}
}

func (m manifest) clone() manifest {
	c := m
	c.Artifacts = make(map[string]artifact, len(m.Artifacts))
	for key, value := range m.Artifacts {
		c.Artifacts[key] = value
	}
	return c
}

// validate enforces the contract the phone build relies on. A manifest that
// names a different commit, a different library version or a non-sha256
// digest is rejected before any artifact is downloaded or linked.
func (m manifest) validate() error {
	if m.Schema != manifestSchema {
		return fmt.Errorf("manifest schema is %d, want %d", m.Schema, manifestSchema)
	}
	if m.Name != "libghostty-vt" {
		return fmt.Errorf("manifest name is %q, want libghostty-vt", m.Name)
	}
	if m.Upstream != upstreamURL {
		return fmt.Errorf("manifest upstream is %q, want %q", m.Upstream, upstreamURL)
	}
	if m.Commit != pinnedCommit {
		return fmt.Errorf("manifest commit is %q, not the pinned commit %s", m.Commit, pinnedCommit)
	}
	if m.LibVersion != libVersion {
		return fmt.Errorf("manifest libVersion is %q, the Go bindings expect %q", m.LibVersion, libVersion)
	}
	if m.Zig != zigVersion {
		return fmt.Errorf("manifest zig is %q, want %q", m.Zig, zigVersion)
	}
	if m.Optimize != "ReleaseFast" || !m.Strip {
		return fmt.Errorf("manifest build is optimize=%q strip=%v, want ReleaseFast and stripped", m.Optimize, m.Strip)
	}
	for _, target := range targets {
		a, ok := m.Artifacts[target.Key]
		if !ok {
			return fmt.Errorf("manifest is missing artifact %q", target.Key)
		}
		if a.Commit != pinnedCommit {
			return fmt.Errorf("artifact %q records commit %q, not the pinned commit", target.Key, a.Commit)
		}
		if a.File != assetName(target) {
			return fmt.Errorf("artifact %q file is %q, want %q", target.Key, a.File, assetName(target))
		}
		if len(a.SHA256) != 64 {
			return fmt.Errorf("artifact %q sha256 is %q, want 64 hex characters", target.Key, a.SHA256)
		}
		if _, err := hex.DecodeString(a.SHA256); err != nil {
			return fmt.Errorf("artifact %q sha256 is not hexadecimal: %w", target.Key, err)
		}
		if strings.ToLower(a.SHA256) != a.SHA256 {
			return fmt.Errorf("artifact %q sha256 is not lowercase", target.Key)
		}
		if a.Size <= 0 {
			return fmt.Errorf("artifact %q size is %d", target.Key, a.Size)
		}
		if a.URL == "" || !strings.HasSuffix(a.URL, "/"+a.File) {
			return fmt.Errorf("artifact %q url %q does not name %s", target.Key, a.URL, a.File)
		}
		if target.Platform == "android" && m.NDK == "" {
			return fmt.Errorf("manifest builds Android artifacts but records no NDK version")
		}
	}
	if len(m.Artifacts) != len(targets) {
		return fmt.Errorf("manifest has %d artifacts, want %d", len(m.Artifacts), len(targets))
	}
	return nil
}

func (m manifest) marshal() ([]byte, error) {
	// encoding/json sorts map keys, so the artifact table is stable in
	// review diffs.
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func readManifest(path string) (manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func writeManifest(path string, m manifest) error {
	data, err := m.marshal()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}
