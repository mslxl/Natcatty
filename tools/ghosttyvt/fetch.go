package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// cmdFetch downloads the pinned release artifacts into a directory, verifying
// each one's sha256 and size against ui/terminal/artifacts.json. CI starts
// from a clean checkout, where build/ghostty-vt is empty; this is how a phone
// job gets the native library without rebuilding Ghostty.
func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	outDir := fs.String("out", "build/ghostty-vt", "directory to put the .a artifacts in")
	manifestPath := fs.String("manifest", "ui/terminal/artifacts.json", "manifest naming each artifact")
	platform := fs.String("platform", "all", "all, ios or android")
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
	return fetchManifest(m, *outDir, *platform)
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
