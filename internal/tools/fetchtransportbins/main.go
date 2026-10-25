// Command fetchtransportbins downloads the pinned MoshCatty and
// Netcatty-et-bin release archives, verifies their SHA-256 digests, extracts
// only the expected client binary, and verifies the binary digest and size
// before placing it under resources/<kind>/<target>/.
//
// The digests come from adapters/bundle, which also compiles them into the app
// for verification before every launch. A failed verification removes every
// partial file; no system binary is ever substituted.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mslxl/natcatty/adapters/bundle"
)

const (
	maxArchiveBytes = 200 << 20
	downloadTimeout = 10 * time.Minute
)

func main() {
	kind := flag.String("kind", "all", "which client to fetch: mosh, et, or all")
	targets := flag.String("target", "", "comma-separated targets, or all; empty uses the host target")
	out := flag.String("out", "resources", "resources directory")
	force := flag.Bool("force", false, "overwrite an existing binary")
	flag.Parse()

	if err := run(*kind, *targets, *out, *force, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "fetchtransportbins:", err)
		os.Exit(1)
	}
}

func run(kind, targetList, out string, force bool, output io.Writer) error {
	kinds, err := parseKinds(kind)
	if err != nil {
		return err
	}
	targets, err := parseTargets(targetList)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: downloadTimeout}
	for _, current := range kinds {
		repository, release, assets := manifest(current)
		for _, target := range targets {
			asset, ok := assets[target]
			if !ok {
				return fmt.Errorf("%s has no asset for %s", current, target)
			}
			destination := filepath.Join(out, string(current), target, asset.Binary)
			if _, err := os.Stat(destination); err == nil && !force {
				fmt.Fprintf(output, "%s: %s already exists (use -force to replace)\n", current, destination)
				continue
			}
			if err := fetchOne(client, current, repository, release, target, asset, out); err != nil {
				return fmt.Errorf("%s/%s: %w", current, target, err)
			}
			fmt.Fprintf(output, "%s: wrote %s\n", current, destination)
		}
	}
	return nil
}

func parseKinds(value string) ([]bundle.Kind, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all", "":
		return []bundle.Kind{bundle.Mosh, bundle.ET}, nil
	case string(bundle.Mosh):
		return []bundle.Kind{bundle.Mosh}, nil
	case string(bundle.ET):
		return []bundle.Kind{bundle.ET}, nil
	default:
		return nil, fmt.Errorf("unknown kind %q", value)
	}
}

func parseTargets(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		target, ok := bundle.HostTarget()
		if !ok {
			return nil, fmt.Errorf("this platform has no release target")
		}
		return []string{target}, nil
	}
	known := map[string]bool{
		"win32-x64": true, "linux-x64": true, "linux-arm64": true, "darwin-universal": true,
	}
	if strings.EqualFold(value, "all") {
		return []string{"win32-x64", "linux-x64", "linux-arm64", "darwin-universal"}, nil
	}
	var targets []string
	for _, target := range strings.Split(value, ",") {
		target = strings.TrimSpace(target)
		if !known[target] {
			return nil, fmt.Errorf("unknown target %q", target)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func manifest(kind bundle.Kind) (repository, release string, assets map[string]bundle.Asset) {
	if kind == bundle.Mosh {
		return bundle.MoshRepository, bundle.MoshRelease, bundle.MoshAssets()
	}
	return bundle.ETRepository, bundle.ETRelease, bundle.ETAssets()
}

func fetchOne(client *http.Client, kind bundle.Kind, repository, release, target string, asset bundle.Asset, out string) error {
	url := repository + "/releases/download/" + release + "/" + asset.Asset
	response, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", url, response.Status)
	}

	workDir, err := os.MkdirTemp("", "fetchtransportbins-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	archivePath := filepath.Join(workDir, asset.Asset)
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(archiveFile, digest), io.LimitReader(response.Body, maxArchiveBytes+1))
	closeErr := archiveFile.Close()
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxArchiveBytes {
		return fmt.Errorf("%s exceeds %d bytes", asset.Asset, maxArchiveBytes)
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(actual, asset.ArchiveSHA256) {
		return fmt.Errorf("archive SHA-256 mismatch: want %s, got %s", asset.ArchiveSHA256, actual)
	}

	binaryPath := filepath.Join(workDir, asset.Binary)
	if err := extractBinary(archivePath, asset.Binary, binaryPath); err != nil {
		return err
	}
	size, actual, err := fileDigest(binaryPath)
	if err != nil {
		return err
	}
	if size != asset.Size || !strings.EqualFold(actual, asset.BinarySHA256) {
		return fmt.Errorf("binary verification failed: want %d/%s, got %d/%s",
			asset.Size, asset.BinarySHA256, size, actual)
	}

	destination := filepath.Join(out, string(kind), target, asset.Binary)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Rename(binaryPath, destination); err != nil {
		// Rename fails across volumes; fall back to a copy.
		if copyErr := copyFile(binaryPath, destination); copyErr != nil {
			return copyErr
		}
		_ = os.Remove(binaryPath)
	}
	_ = os.Chmod(destination, 0o755)
	return nil
}

// extractBinary walks a gzipped tar and copies the entry whose base name is
// wanted. Absolute names, parent-relative names, and links are rejected.
func extractBinary(archivePath, wanted, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(archivePath), err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(archivePath), err)
		}
		name := path.Clean(strings.ReplaceAll(header.Name, "\\", "/"))
		if !safeArchiveName(name) {
			return fmt.Errorf("unsafe archive entry %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg || path.Base(name) != wanted {
			continue
		}
		out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		limit := int64(maxArchiveBytes)
		if header.Size > 0 && header.Size < limit {
			limit = header.Size
		}
		written, copyErr := io.Copy(out, io.LimitReader(tarReader, limit))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if header.Size > 0 && written != header.Size {
			return fmt.Errorf("short extraction of %s", wanted)
		}
		return nil
	}
	return fmt.Errorf("%s does not contain %s", filepath.Base(archivePath), wanted)
}

// safeArchiveName rejects absolute and traversal paths.
func safeArchiveName(name string) bool {
	if name == "" || name == "." || strings.HasPrefix(name, "/") {
		return false
	}
	if strings.Contains(name, ":") && len(name) > 1 {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

func fileDigest(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	return destination.Close()
}
