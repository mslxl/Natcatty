package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// testFetchManifest returns a valid manifest whose artifact URLs point at a
// local server, and the bytes the server publishes for each file.
func testFetchManifest(t *testing.T, baseURL string) (manifest, map[string][]byte) {
	t.Helper()
	m := newManifest(baseURL)
	m.NDK = "30.0.16248370"
	body := map[string][]byte{}
	for _, target := range targets {
		content := []byte("artifact:" + target.Key)
		sum := sha256.Sum256(content)
		m.Artifacts[target.Key] = artifact{
			File:   assetName(target),
			URL:    baseURL + "/" + assetName(target),
			SHA256: hex.EncodeToString(sum[:]),
			Size:   int64(len(content)),
			Commit: pinnedCommit,
		}
		body[assetName(target)] = content
	}
	if err := m.validate(); err != nil {
		t.Fatalf("test manifest is invalid: %v", err)
	}
	return m, body
}

func serveArtifacts(t *testing.T, body map[string][]byte) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, ok := body[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestFetchDownloadsOnlyTheRequestedPlatform(t *testing.T) {
	// The body map is shared with the server, so build it before the server
	// starts; the URL is only needed for the manifest.
	m, body := testFetchManifest(t, "https://example.invalid/release")
	srv, hits := serveArtifacts(t, body)
	for key, rec := range m.Artifacts {
		rec.URL = srv.URL + "/" + rec.File
		m.Artifacts[key] = rec
	}

	out := t.TempDir()
	if err := fetchManifest(m, out, "ios"); err != nil {
		t.Fatalf("fetch ios: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2 (the iOS artifacts)", got)
	}
	for _, target := range targets {
		path := filepath.Join(out, assetName(target))
		_, err := os.Stat(path)
		if target.Platform == "ios" {
			if err != nil {
				t.Errorf("%s: %v", target.Key, err)
			}
			continue
		}
		if !os.IsNotExist(err) {
			t.Errorf("%s was fetched though only ios was requested", target.Key)
		}
	}
}

func TestFetchSkipsVerifiedFilesAndRepairsCorruptOnes(t *testing.T) {
	m, body := testFetchManifest(t, "https://example.invalid/release")
	srv, hits := serveArtifacts(t, body)
	for key, rec := range m.Artifacts {
		rec.URL = srv.URL + "/" + rec.File
		m.Artifacts[key] = rec
	}
	out := t.TempDir()

	// A matching file is not downloaded again.
	ios := m.Artifacts["ios-arm64"]
	if err := os.WriteFile(filepath.Join(out, ios.File), body[ios.File], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fetchManifest(m, out, "ios"); err != nil {
		t.Fatalf("fetch ios: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1 (only the missing simulator slice)", got)
	}

	// A corrupt file is replaced with the verified artifact.
	sim := m.Artifacts["ios-arm64-simulator"]
	if err := os.WriteFile(filepath.Join(out, sim.File), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fetchManifest(m, out, "ios"); err != nil {
		t.Fatalf("fetch ios after corruption: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, sim.File))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body[sim.File]) {
		t.Errorf("%s was not repaired: %q", sim.File, got)
	}
}

func TestFetchRejectsChecksumMismatchNamingTheArtifact(t *testing.T) {
	m, body := testFetchManifest(t, "https://example.invalid/release")
	ios := m.Artifacts["ios-arm64"]
	body[ios.File] = []byte("tampered")
	srv, _ := serveArtifacts(t, body)
	for key, rec := range m.Artifacts {
		rec.URL = srv.URL + "/" + rec.File
		m.Artifacts[key] = rec
	}

	out := t.TempDir()
	err := fetchManifest(m, out, "ios")
	if err == nil {
		t.Fatal("a checksum mismatch was accepted")
	}
	for _, want := range []string{"ios-arm64", "sha256"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(out, ios.File)); !os.IsNotExist(statErr) {
		t.Errorf("a rejected artifact was left at %s", filepath.Join(out, ios.File))
	}
	matches, _ := filepath.Glob(filepath.Join(out, "*"))
	if len(matches) != 0 {
		t.Errorf("rejected fetch left files behind: %v", matches)
	}
}

func TestFetchRejectsSizeMismatchNamingTheArtifact(t *testing.T) {
	m, body := testFetchManifest(t, "https://example.invalid/release")
	ios := m.Artifacts["ios-arm64"]
	ios.Size++
	m.Artifacts["ios-arm64"] = ios
	srv, _ := serveArtifacts(t, body)
	for key, rec := range m.Artifacts {
		rec.URL = srv.URL + "/" + rec.File
		m.Artifacts[key] = rec
	}

	out := t.TempDir()
	err := fetchManifest(m, out, "ios")
	if err == nil {
		t.Fatal("a size mismatch was accepted")
	}
	for _, want := range []string{"ios-arm64", "bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestFetchRejectsUnknownPlatform(t *testing.T) {
	m, _ := testFetchManifest(t, "https://example.invalid/release")
	err := fetchManifest(m, t.TempDir(), "web")
	if err == nil || !strings.Contains(err.Error(), "unknown platform") {
		t.Fatalf("unknown platform error = %v", err)
	}
}

// unreachableRelease serves 404 for every artifact, the way the pinned release
// actually behaves when its assets have not been published.
func unreachableRelease(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// manifestAt writes a manifest whose URLs point at srv and returns its path.
func manifestAt(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	m, _ := testFetchManifest(t, srv.URL)
	path := filepath.Join(t.TempDir(), "artifacts.json")
	if err := writeManifest(path, m); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFetchFallsBackToThePinnedSource proves a fresh checkout still gets the
// native artifacts when the release is gone: the download failure must hand
// the same platform to the source build instead of failing the job.
func TestFetchFallsBackToThePinnedSource(t *testing.T) {
	srv := unreachableRelease(t)
	manifestPath := manifestAt(t, srv)

	orig := buildPinnedArtifacts
	t.Cleanup(func() { buildPinnedArtifacts = orig })
	var calls [][3]string
	buildPinnedArtifacts = func(outDir, manifest, platform string) error {
		calls = append(calls, [3]string{outDir, manifest, platform})
		return nil
	}

	out := t.TempDir()
	if err := cmdFetch([]string{
		"--manifest", manifestPath, "--out", out, "--platform", "ios",
	}); err != nil {
		t.Fatalf("fetch with source fallback: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("source build called %d times, want 1", len(calls))
	}
	if got := calls[0]; got != [3]string{out, manifestPath, "ios"} {
		t.Errorf("source build args = %v, want out=%s manifest=%s platform=ios", got, out, manifestPath)
	}
}

// TestFetchReportsTheReleaseFailureWhenBuildIsDisabled proves the fallback is
// not a silent success path: with it disabled the artifact is named and the
// command fails.
func TestFetchReportsTheReleaseFailureWhenBuildIsDisabled(t *testing.T) {
	srv := unreachableRelease(t)
	manifestPath := manifestAt(t, srv)

	orig := buildPinnedArtifacts
	t.Cleanup(func() { buildPinnedArtifacts = orig })
	buildPinnedArtifacts = func(outDir, manifest, platform string) error {
		t.Fatal("source build ran although --build-if-missing=false")
		return nil
	}

	err := cmdFetch([]string{
		"--manifest", manifestPath, "--out", t.TempDir(), "--platform", "ios",
		"--build-if-missing=false",
	})
	if err == nil {
		t.Fatal("a missing release was accepted with the fallback disabled")
	}
	for _, want := range []string{"ios-arm64", "404"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
