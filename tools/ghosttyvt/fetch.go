package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// buildPinnedArtifacts builds the artifacts for platform from the pinned,
// sha256-verified Ghostty source. It is a variable so a test can prove the
// download fallback without compiling Ghostty.
var buildPinnedArtifacts = func(outDir, manifestPath, platform string) error {
	return cmdBuild([]string{
		"--out", outDir,
		"--manifest", manifestPath,
		"--platforms", platform,
		"--no-manifest",
	})
}

// cmdFetch puts the pinned native artifacts in a directory, verifying each
// one's sha256 and size against ui/terminal/artifacts.json. A matching file is
// left alone, so a warm cache costs no network.
//
// If the published release cannot be downloaded, it falls back to building the
// same pinned commit from source, whose tarball is itself sha256-verified. CI
// starts from a clean checkout, so this keeps every phone job runnable even
// when the release assets have not been published (or have gone away); the
// release stays the fast path when it exists. Pass --build-if-missing=false to
// require the release and fail instead.
func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	outDir := fs.String("out", "build/ghostty-vt", "directory to put the .a artifacts in")
	manifestPath := fs.String("manifest", "ui/terminal/artifacts.json", "manifest naming each artifact")
	platform := fs.String("platform", "all", "all, ios or android")
	buildIfMissing := fs.Bool("build-if-missing", true, "build from the pinned source when the release artifact cannot be downloaded")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	if err := m.validate(); err != nil {
		return fmt.Errorf("%s: %w", *manifestPath, err)
	}
	if err := fetchManifest(m, *outDir, *platform); err != nil {
		if !*buildIfMissing {
			return err
		}
		log.Printf("fetch: %v", err)
		log.Printf("fetch: the pinned release is unavailable; building the same pinned commit from source")
		if buildErr := buildPinnedArtifacts(*outDir, *manifestPath, *platform); buildErr != nil {
			return fmt.Errorf("fetch the pinned artifacts: %w; build from the pinned source: %v", err, buildErr)
		}
		log.Printf("fetch: built %s artifacts from the pinned source", *platform)
	}
	return nil
}

// fetchManifest downloads every target of platform (all, ios or android)
// whose file is missing or does not match the manifest. A matching file is
// left alone, so a warm cache costs no network; a corrupt one is replaced.
// Every failure names the artifact key, so a missing native input is a build
// error with a cause, never a link failure far from here.
func fetchManifest(m manifest, outDir, platform string) error {
	if platform == "" {
		platform = "all"
	}
	switch platform {
	case "all", "ios", "android":
	default:
		return fmt.Errorf("unknown platform %q: use all, ios or android", platform)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	fetched := 0
	for _, target := range targets {
		if platform != "all" && target.Platform != platform {
			continue
		}
		record := m.Artifacts[target.Key]
		dst := filepath.Join(outDir, record.File)
		if sum, size, err := sha256File(dst); err == nil && sum == record.SHA256 && size == record.Size {
			log.Printf("%s: %s matches the manifest", target.Key, dst)
			continue
		}

		tmp := dst + ".download"
		os.Remove(tmp)
		if err := downloadFile(record.URL, tmp); err != nil {
			return fmt.Errorf("%s: %w", target.Key, err)
		}
		sum, size, err := sha256File(tmp)
		if err != nil {
			os.Remove(tmp)
			return fmt.Errorf("%s: %w", target.Key, err)
		}
		if sum != record.SHA256 {
			os.Remove(tmp)
			return fmt.Errorf("%s: %s has sha256 %s, manifest records %s", target.Key, record.URL, sum, record.SHA256)
		}
		if size != record.Size {
			os.Remove(tmp)
			return fmt.Errorf("%s: %s is %d bytes, manifest records %d", target.Key, record.URL, size, record.Size)
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("%s: install %s: %w", target.Key, dst, err)
		}
		log.Printf("%s: fetched %s (%d bytes, sha256 verified)", target.Key, dst, size)
		fetched++
	}
	log.Printf("fetched %d artifact(s); every file matches the manifest", fetched)
	return nil
}
