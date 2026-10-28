package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Release artifacts are the files a release hands to users: the macOS disk
// image, the Windows NSIS Setup executable, the Linux tar.gz and deb packages,
// and the Linux install script. The raw executable and the .app bundle are
// carried with them but are not installers of their own.
func isReleaseArtifact(name string) bool {
	base := path.Base(name)
	switch {
	case strings.HasSuffix(base, ".dmg"),
		strings.HasSuffix(base, ".deb"),
		strings.HasSuffix(base, ".tar.gz"):
		return true
	case base == "install.sh":
		return true
	case strings.HasSuffix(base, ".exe") && strings.Contains(base, " Setup "):
		return true
	}
	return false
}

// releaseArtifacts lists the release artifacts under root as clean
// forward-slash paths relative to root, sorted.
func releaseArtifacts(root string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if isReleaseArtifact(relative) {
			names = append(names, relative)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

type sumEntry struct {
	Sum  string
	Name string
}

// writeSums writes a sha256sum-compatible SHA256SUMS for every release
// artifact under root. Names are relative to root, so the file verifies from
// any directory that holds the same artifact tree.
func writeSums(root, out string) ([]sumEntry, error) {
	names, err := releaseArtifacts(root)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s holds no release artifacts", root)
	}
	entries := make([]sumEntry, 0, len(names))
	for _, name := range names {
		sum, err := fileSHA256(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		entries = append(entries, sumEntry{Sum: sum, Name: name})
	}
	var text strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&text, "%s  %s\n", entry.Sum, entry.Name)
	}
	if dir := filepath.Dir(out); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(out, []byte(text.String()), 0o644); err != nil {
		return nil, err
	}
	return entries, nil
}

func fileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func readSums(filePath string) ([]sumEntry, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []sumEntry
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		index := strings.IndexAny(text, " \t")
		if index < 0 {
			return nil, fmt.Errorf("%s:%d: expected \"<sha256>  <name>\"", filePath, line)
		}
		sum := strings.ToLower(strings.TrimSpace(text[:index]))
		if len(sum) != sha256.Size*2 {
			return nil, fmt.Errorf("%s:%d: %q is not a SHA-256 digest", filePath, line, sum)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fmt.Errorf("%s:%d: %q is not hexadecimal", filePath, line, sum)
		}
		name := strings.TrimSpace(text[index:])
		name = strings.TrimPrefix(name, "*")
		if !cleanRelativeName(name) {
			return nil, fmt.Errorf("%s:%d: %q is not a clean relative path", filePath, line, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s:%d: %q is listed more than once", filePath, line, name)
		}
		seen[name] = true
		entries = append(entries, sumEntry{Sum: sum, Name: name})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s lists no checksums", filePath)
	}
	return entries, nil
}

func cleanRelativeName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	return path.Clean(name) == name && name != "." && !strings.HasPrefix(name, "../")
}

// verifySums checks every listed file and requires the list to cover exactly
// the release artifacts under root, so a new package cannot ship unchecksummed.
func verifySums(root, sumsPath string) ([]sumEntry, error) {
	entries, err := readSums(sumsPath)
	if err != nil {
		return nil, err
	}
	listed := make(map[string]bool, len(entries))
	for _, entry := range entries {
		listed[entry.Name] = true
		filePath := filepath.Join(root, filepath.FromSlash(entry.Name))
		info, err := os.Stat(filePath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", entry.Name)
		}
		actual, err := fileSHA256(filePath)
		if err != nil {
			return nil, err
		}
		if actual != entry.Sum {
			return nil, fmt.Errorf("%s: checksum is %s, want %s", entry.Name, actual, entry.Sum)
		}
	}
	artifacts, err := releaseArtifacts(root)
	if err != nil {
		return nil, err
	}
	for _, name := range artifacts {
		if !listed[name] {
			return nil, fmt.Errorf("%s is a release artifact but is not in %s", name, sumsPath)
		}
	}
	if len(artifacts) != len(entries) {
		return nil, fmt.Errorf("%s lists %d files, but %s holds %d release artifacts",
			sumsPath, len(entries), root, len(artifacts))
	}
	return entries, nil
}

// collect copies every release artifact under fromDir into toDir, flattened
// by base name, so the aggregate SHA256SUMS has stable relative names even
// though each platform uploaded its own directory tree.
//
// MyGo writes one install.sh beside each Linux archive; both copies are the
// same script. A repeated base name is allowed only when the files are
// byte-identical, so the aggregate lists install.sh once and a genuinely
// different artifact still fails the release.
func collect(fromDir, toDir string) ([]string, error) {
	names, err := releaseArtifacts(fromDir)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s holds no release artifacts", fromDir)
	}
	if err := os.MkdirAll(toDir, 0o755); err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]string{}
	for _, name := range names {
		base := path.Base(name)
		source := filepath.Join(fromDir, filepath.FromSlash(name))
		if previous, ok := seen[base]; ok {
			identical, err := sameContent(previous, source)
			if err != nil {
				return nil, err
			}
			if !identical {
				return nil, fmt.Errorf("%s: %q and %q are different artifacts with the same name",
					fromDir, previous, source)
			}
			continue
		}
		seen[base] = source
		destination := filepath.Join(toDir, base)
		if err := copyFile(source, destination); err != nil {
			return nil, err
		}
		out = append(out, base)
	}
	sort.Strings(out)
	return out, nil
}

// sameContent reports whether two regular files have the same bytes.
func sameContent(left, right string) (bool, error) {
	leftSum, err := fileSHA256(left)
	if err != nil {
		return false, err
	}
	rightSum, err := fileSHA256(right)
	if err != nil {
		return false, err
	}
	return leftSum == rightSum, nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(destination, info.Mode().Perm())
}
