package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type buildOptions struct {
	OutDir       string
	CacheDir     string
	ManifestPath string
	Platforms    string
	ZigPath      string
	NDKPath      string
	ReleaseURL   string
	NoManifest   bool
}

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	opts := buildOptions{}
	fs.StringVar(&opts.OutDir, "out", "build/ghostty-vt", "directory for the built .a artifacts")
	fs.StringVar(&opts.CacheDir, "cache", "", "cache directory (default: user cache)")
	fs.StringVar(&opts.ManifestPath, "manifest", "ui/terminal/artifacts.json", "manifest to write")
	fs.StringVar(&opts.Platforms, "platforms", "all", "all, ios or android")
	fs.StringVar(&opts.ZigPath, "zig", "", "zig binary (default: $ZIG, PATH, or pinned download)")
	fs.StringVar(&opts.NDKPath, "ndk", "", "Android NDK root (default: $ANDROID_NDK_HOME)")
	fs.StringVar(&opts.ReleaseURL, "release", defaultReleaseURL, "release URL recorded in the manifest")
	fs.BoolVar(&opts.NoManifest, "no-manifest", false, "do not write the manifest")
	if err := fs.Parse(args); err != nil {
		return err
	}

	wantIOS, wantAndroid, err := parsePlatforms(opts.Platforms)
	if err != nil {
		return err
	}
	if wantIOS && runtime.GOOS != "darwin" {
		return fmt.Errorf("iOS builds need macOS and Xcode; run --platforms android on this host")
	}
	if opts.CacheDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		opts.CacheDir = filepath.Join(cache, "natcatty", "ghostty-vt")
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return err
	}

	log.Printf("pinned source: %s (%s)", upstreamURL, pinnedCommit)
	src, err := ensureSource(opts.CacheDir)
	if err != nil {
		return err
	}
	zig, err := ensureZig(opts.ZigPath, opts.CacheDir)
	if err != nil {
		return err
	}
	log.Printf("source %s, zig %s", src, zigVersion)

	ndk, ndkVersion := "", ""
	if wantAndroid {
		ndk, ndkVersion, err = requireNDK(opts.NDKPath)
		if err != nil {
			return err
		}
		log.Printf("android NDK %s at %s", ndkVersion, ndk)
	}

	work, err := os.MkdirTemp(opts.CacheDir, "work-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	var built []builtArtifact
	hostLib := ""
	if wantIOS {
		artifacts, host, err := buildIOS(zig, src, work, opts.OutDir)
		if err != nil {
			return err
		}
		built = append(built, artifacts...)
		hostLib = host
	}
	if wantAndroid {
		artifacts, err := buildAndroid(zig, src, work, opts.OutDir, ndk)
		if err != nil {
			return err
		}
		built = append(built, artifacts...)
	}

	// The version/ABI check runs against a host library built from the same
	// source. If it disagrees with the Go bindings, stop before writing a
	// manifest that claims the artifacts are usable.
	if err := checkABI(zig, src, hostLib, work); err != nil {
		return err
	}

	for i := range built {
		if err := inspectArtifact(&built[i], ndk, work); err != nil {
			return err
		}
	}

	m, err := loadOrInitManifest(opts.ManifestPath, opts.ReleaseURL)
	if err != nil {
		return err
	}
	if ndkVersion != "" {
		m.NDK = ndkVersion
	}
	for _, a := range built {
		sum, size, err := sha256File(a.Path)
		if err != nil {
			return err
		}
		m.Artifacts[a.Target.Key] = artifact{
			File:   assetName(a.Target),
			URL:    m.Release + "/" + assetName(a.Target),
			SHA256: sum,
			Size:   size,
			Commit: pinnedCommit,
		}
		log.Printf("%s  %s  %d bytes  %s", a.Target.Key, sum, size, a.Path)
	}
	if err := writeSHA256SUMS(opts.OutDir, m); err != nil {
		return err
	}
	if opts.NoManifest {
		log.Printf("manifest not written (--no-manifest)")
		return nil
	}
	if err := m.validate(); err != nil {
		if len(m.Artifacts) != len(targets) {
			log.Printf("manifest not written: %v", err)
			log.Printf("build every target (--platforms all) before recording the manifest")
			return nil
		}
		return err
	}
	if err := writeManifest(opts.ManifestPath, m); err != nil {
		return err
	}
	log.Printf("wrote %s", opts.ManifestPath)
	return nil
}

type builtArtifact struct {
	Target target
	Path   string
}

func parsePlatforms(value string) (ios, android bool, err error) {
	switch strings.ToLower(value) {
	case "all", "":
		return true, true, nil
	case "ios":
		return true, false, nil
	case "android":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("--platforms is %q, want all, ios or android", value)
	}
}

// ensureSource returns a checkout of the pinned commit, downloading the
// pinned tarball and verifying its sha256 when the cache is cold.
func ensureSource(cache string) (string, error) {
	base := filepath.Join(cache, "ghostty-src", pinnedCommit)
	marker := filepath.Join(base, ".natcatty-source")
	wantMarker := pinnedCommit + "\n" + pinnedSourceSHA256 + "\n"
	if data, err := os.ReadFile(marker); err == nil && string(data) == wantMarker {
		return base, nil
	}

	downloads := filepath.Join(cache, "downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return "", err
	}
	tarball := filepath.Join(downloads, pinnedSourceSHA256+".tar.gz")
	if _, err := os.Stat(tarball); err != nil {
		url := "https://codeload.github.com/ghostty-org/ghostty/tar.gz/" + pinnedCommit
		log.Printf("downloading %s", url)
		if err := downloadFile(url, tarball); err != nil {
			return "", err
		}
	}
	sum, _, err := sha256File(tarball)
	if err != nil {
		return "", err
	}
	if sum != pinnedSourceSHA256 {
		return "", fmt.Errorf("pinned source archive %s has sha256 %s, want %s (delete it and retry)", tarball, sum, pinnedSourceSHA256)
	}

	tmp, err := os.MkdirTemp(cache, "extract-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := extractTarGz(tmp, tarball); err != nil {
		return "", err
	}
	top := filepath.Join(tmp, "ghostty-"+pinnedCommit)
	if fi, err := os.Stat(top); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("source archive did not contain ghostty-%s", pinnedCommit)
	}
	if err := os.RemoveAll(base); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(top, base); err != nil {
		return "", err
	}
	if err := os.WriteFile(marker, []byte(wantMarker), 0o644); err != nil {
		return "", err
	}
	return base, nil
}

func downloadFile(url, path string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, path)
}

// extractTarGz extracts a gzipped tarball, refusing names and symlinks that
// escape the destination. The archive is downloaded, so its contents are
// treated as untrusted even though its sha256 is pinned.
func extractTarGz(dest, archive string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Name == "" {
			continue
		}
		dst, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if filepath.IsAbs(hdr.Linkname) {
				return fmt.Errorf("symlink %s -> %s is absolute", hdr.Name, hdr.Linkname)
			}
			resolved := filepath.Join(filepath.Dir(dst), hdr.Linkname)
			rel, err := filepath.Rel(dest, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("symlink %s -> %s escapes the extraction directory", hdr.Name, hdr.Linkname)
			}
			if err := os.Symlink(hdr.Linkname, dst); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
			continue
		default:
			return fmt.Errorf("source archive contains unsupported entry %q (type %d)", hdr.Name, hdr.Typeflag)
		}
	}
}

func safeJoin(root, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("archive path %q is absolute", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive path %q escapes the destination", name)
	}
	dst := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, dst)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive path %q escapes the destination", name)
	}
	return dst, nil
}

var zigDownloads = map[string]struct {
	url, sha string
}{
	"darwin/arm64": {"https://ziglang.org/download/0.16.0/zig-aarch64-macos-0.16.0.tar.xz", "b23d70deaa879b5c2d486ed3316f7eaa53e84acf6fc9cc747de152450d401489"},
	"darwin/amd64": {"https://ziglang.org/download/0.16.0/zig-x86_64-macos-0.16.0.tar.xz", "0387557ed1877bc6a2e1802c8391953baddba76081876301c522f52977b52ba7"},
	"linux/amd64":  {"https://ziglang.org/download/0.16.0/zig-x86_64-linux-0.16.0.tar.xz", "70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00"},
	"linux/arm64":  {"https://ziglang.org/download/0.16.0/zig-aarch64-linux-0.16.0.tar.xz", "ea4b09bfb22ec6f6c6ceac57ab63efb6b46e17ab08d21f69f3a48b38e1534f17"},
}

func ensureZig(flagPath, cache string) (string, error) {
	var candidates []string
	explicit := 0
	if flagPath != "" {
		candidates = append(candidates, flagPath)
		explicit++
	}
	if env := os.Getenv("ZIG"); env != "" {
		candidates = append(candidates, env)
		explicit++
	}
	if path, err := exec.LookPath("zig"); err == nil {
		candidates = append(candidates, path)
	}
	for i, candidate := range candidates {
		version, err := zigVersionOf(candidate)
		if err == nil && version == zigVersion {
			return candidate, nil
		}
		// An explicitly selected Zig must be the pinned one: silently
		// falling back would undermine reproducibility. A `zig` merely
		// found on PATH may be another version; keep looking.
		if i < explicit {
			return "", fmt.Errorf("%s is not zig %s (reports %q)", candidate, zigVersion, version)
		}
	}

	key := runtime.GOOS + "/" + runtime.GOARCH
	dl, ok := zigDownloads[key]
	if !ok {
		return "", fmt.Errorf("no pinned Zig %s download for %s", zigVersion, key)
	}
	dir := filepath.Join(cache, "zig", zigVersion)
	if found := findZigBinary(dir); found != "" {
		if version, err := zigVersionOf(found); err == nil && version == zigVersion {
			return found, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	archive := filepath.Join(cache, "downloads", "zig-"+zigVersion+"-"+strings.ReplaceAll(key, "/", "-")+".tar.xz")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(archive); err != nil {
		log.Printf("downloading %s", dl.url)
		if err := downloadFile(dl.url, archive); err != nil {
			return "", err
		}
	}
	sum, _, err := sha256File(archive)
	if err != nil {
		return "", err
	}
	if sum != dl.sha {
		return "", fmt.Errorf("Zig archive %s has sha256 %s, want %s", archive, sum, dl.sha)
	}
	// Go has no xz decoder; the archive is hash-pinned, and tar is the
	// system's own xz reader.
	cmd := exec.Command("tar", "-xJf", archive, "-C", dir)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("extract Zig: %w", err)
	}
	found := findZigBinary(dir)
	if found == "" {
		return "", fmt.Errorf("Zig archive did not contain a zig binary")
	}
	return found, nil
}

func findZigBinary(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			candidate := filepath.Join(dir, entry.Name(), "zig")
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

func zigVersionOf(path string) (string, error) {
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func requireNDK(explicit string) (root, version string, err error) {
	root = explicit
	if root == "" {
		root = os.Getenv("ANDROID_NDK_HOME")
	}
	if root == "" {
		return "", "", fmt.Errorf("ANDROID_NDK_HOME is not set; run inside `nix develop .#android` or pass --ndk")
	}
	data, err := os.ReadFile(filepath.Join(root, "source.properties"))
	if err != nil {
		return "", "", fmt.Errorf("NDK at %s has no source.properties: %w", root, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "Pkg.Revision" {
			version = strings.TrimSpace(value)
		}
	}
	if version == "" {
		return "", "", fmt.Errorf("NDK at %s does not report Pkg.Revision", root)
	}
	return root, version, nil
}

func ndkEnv(ndk string) []string {
	return append(os.Environ(), "ANDROID_NDK_HOME="+ndk, "ANDROID_NDK_ROOT="+ndk)
}

// appleHostEnv strips the Nix compiler wrapper from a build that links with
// Apple's own toolchain. Inside `nix develop .#android` on macOS, SDKROOT
// and the wrapped clang point at a macOS SDK and a multi-target compiler;
// Ghostty's final dylib link then fails. Android builds keep the Nix PATH
// (they need the NDK tools), but Apple host builds use Xcode directly.
func appleHostEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "SDKROOT", "MACOSX_DEPLOYMENT_TARGET", "CC", "CXX", "CPP", "LD", "AR",
			"NIX_CC", "NIX_CFLAGS_COMPILE", "NIX_LDFLAGS", "NIX_LDFLAGS_FOR_BUILD",
			"DEVELOPER_DIR", "CPATH", "LIBRARY_PATH", "PATH":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
}

func hostEnv() []string {
	if runtime.GOOS == "darwin" {
		return appleHostEnv()
	}
	return os.Environ()
}

func buildIOS(zig, src, work, out string) ([]builtArtifact, string, error) {
	prefix := filepath.Join(work, "ios")
	log.Printf("building iOS device and simulator slices")
	cmd := exec.Command(zig, "build", "-Demit-lib-vt", "-Demit-xcframework=true", "-Doptimize=ReleaseFast", "-Dstrip=true", "--prefix", prefix)
	cmd.Dir = src
	cmd.Env = hostEnv()
	if err := runCommand(cmd); err != nil {
		return nil, "", err
	}
	var artifacts []builtArtifact
	for _, t := range targets {
		if t.Platform != "ios" {
			continue
		}
		lib := filepath.Join(prefix, "lib", "ghostty-vt.xcframework", t.XCFrameworkSlice, "libghostty-vt-fat.a")
		if fi, err := os.Stat(lib); err != nil || fi.IsDir() {
			return nil, "", fmt.Errorf("iOS slice %s missing at %s", t.XCFrameworkSlice, lib)
		}
		dst := filepath.Join(out, assetName(t))
		if err := copyFile(lib, dst); err != nil {
			return nil, "", err
		}
		artifacts = append(artifacts, builtArtifact{Target: t, Path: dst})
	}
	hostLib := filepath.Join(prefix, "lib", "libghostty-vt.a")
	if _, err := os.Stat(hostLib); err != nil {
		hostLib = ""
	}
	return artifacts, hostLib, nil
}

func buildAndroid(zig, src, work, out, ndk string) ([]builtArtifact, error) {
	var artifacts []builtArtifact
	for _, t := range targets {
		if t.Platform != "android" {
			continue
		}
		prefix := filepath.Join(work, "android", t.Key)
		log.Printf("building %s (%s)", t.Key, t.ZigTriple)
		cmd := exec.Command(zig, "build",
			"-Demit-lib-vt",
			"-Dtarget="+t.ZigTriple,
			"-Dcpu=baseline",
			"-Doptimize=ReleaseFast",
			"-Dstrip=true",
			"--prefix", prefix,
		)
		cmd.Dir = src
		cmd.Env = ndkEnv(ndk)
		if err := runCommand(cmd); err != nil {
			return nil, err
		}
		lib := filepath.Join(prefix, "lib", "libghostty-vt.a")
		if fi, err := os.Stat(lib); err != nil || fi.IsDir() {
			return nil, fmt.Errorf("%s: libghostty-vt.a missing at %s", t.Key, lib)
		}
		dst := filepath.Join(out, assetName(t))
		if err := copyFile(lib, dst); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, builtArtifact{Target: t, Path: dst})
	}
	return artifacts, nil
}

// checkABI links a tiny host program against a library built from the same
// source and compares what it reports with the ABI the Go bindings mirror.
func checkABI(zig, src, hostLib, work string) error {
	if hostLib == "" {
		prefix := filepath.Join(work, "host")
		log.Printf("building host library for the ABI probe")
		cmd := exec.Command(zig, "build", "-Demit-lib-vt", "-Doptimize=ReleaseFast", "-Dstrip=true", "--prefix", prefix)
		cmd.Dir = src
		cmd.Env = hostEnv()
		if err := runCommand(cmd); err != nil {
			return err
		}
		hostLib = filepath.Join(prefix, "lib", "libghostty-vt.a")
		if _, err := os.Stat(hostLib); err != nil {
			return fmt.Errorf("host library for ABI probe missing at %s", hostLib)
		}
	}

	probeC := filepath.Join(work, "abi_probe.c")
	source := `#include <stdio.h>
extern const char *ghostty_type_json(void);
int main(void) {
    const char *manifest = ghostty_type_json();
    if (!manifest) return 1;
    fputs(manifest, stdout);
    return 0;
}
`
	if err := os.WriteFile(probeC, []byte(source), 0o644); err != nil {
		return err
	}
	probe := filepath.Join(work, "abi_probe")
	args := []string{"cc", "-O2", "-o", probe, probeC, hostLib}
	if runtime.GOOS != "darwin" {
		args = append(args, "-lm", "-lpthread", "-ldl")
	}
	cmd := exec.Command(zig, args...)
	cmd.Env = hostEnv()
	if err := runCommand(cmd); err != nil {
		return fmt.Errorf("link ABI probe: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, probe).Output()
	if err != nil {
		return fmt.Errorf("run ABI probe: %w", err)
	}
	if err := diffABI(expectedABI, out); err != nil {
		return fmt.Errorf("ABI check failed: %w", err)
	}
	log.Printf("ABI check passed: library reports %s and the layouts the Go bindings expect", libVersion)
	return nil
}

func inspectArtifact(a *builtArtifact, ndk, work string) error {
	log.Printf("verifying %s", a.Target.Key)
	nm, err := nmCommand(ndk, a.Target)
	if err != nil {
		return err
	}
	args := []string{"-g", "--defined-only", a.Path}
	if a.Target.Platform == "ios" {
		args = append([]string{"llvm-nm"}, args...)
	}
	cmd := exec.Command(nm, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: list symbols with %s: %w\n%s", a.Target.Key, nm, err, out)
	}
	got := parseNMSymbols(string(out))
	missing, duplicates := symbolProblems(got, expectedSymbols)
	if len(missing) > 0 {
		return fmt.Errorf("%s: artifact is missing %d symbols the Go bindings require (first: %s)", a.Target.Key, len(missing), strings.Join(firstN(missing, 5), ", "))
	}
	if len(duplicates) > 0 {
		return fmt.Errorf("%s: artifact defines %d symbols more than once, which would break the link (first: %s)", a.Target.Key, len(duplicates), strings.Join(firstN(duplicates, 5), ", "))
	}
	found, err := fileContains(a.Path, []byte(libVersion))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: artifact does not contain the version string %q the Go bindings expect", a.Target.Key, libVersion)
	}
	if a.Target.Platform == "android" {
		if err := checkAndroidPageAlignment(ndk, a.Target.ClangTriple, a.Path, work); err != nil {
			return fmt.Errorf("%s: %w", a.Target.Key, err)
		}
	}
	return nil
}

func nmCommand(ndk string, t target) (string, error) {
	if t.Platform == "android" {
		host, err := ndkPrebuiltHost(ndk)
		if err != nil {
			return "", err
		}
		return filepath.Join(ndk, "toolchains", "llvm", "prebuilt", host, "bin", "llvm-nm"), nil
	}
	return "/usr/bin/xcrun", nil
}

func ndkPrebuiltHost(ndk string) (string, error) {
	base := filepath.Join(ndk, "toolchains", "llvm", "prebuilt")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read NDK prebuilt directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(base, entry.Name(), "bin", "clang")); err == nil {
				return entry.Name(), nil
			}
		}
	}
	return "", fmt.Errorf("no NDK clang under %s", base)
}

// checkAndroidPageAlignment links the static archive into a probe shared
// object with 16 KB max page size and checks every LOAD segment. A static
// archive has no program headers of its own, so the probe is what proves the
// artifact can be linked into an Android-15-compatible DSO.
func checkAndroidPageAlignment(ndk, clangTriple, artifact, work string) error {
	host, err := ndkPrebuiltHost(ndk)
	if err != nil {
		return err
	}
	bin := filepath.Join(ndk, "toolchains", "llvm", "prebuilt", host, "bin")
	clang := filepath.Join(bin, "clang")
	readelf := filepath.Join(bin, "llvm-readelf")

	probeC := filepath.Join(work, "page_probe.c")
	if err := os.WriteFile(probeC, []byte("int natcatty_page_probe;\n"), 0o644); err != nil {
		return err
	}
	so := filepath.Join(work, "page-probe-"+strings.NewReplacer("-", "_", ".", "_").Replace(clangTriple)+".so")
	cmd := exec.Command(clang,
		"--target="+clangTriple,
		"-fPIC", "-shared", "-O0",
		"-Wl,--whole-archive", artifact, "-Wl,--no-whole-archive",
		"-Wl,-z,max-page-size=16384",
		"-o", so,
	)
	if err := runCommand(cmd); err != nil {
		return fmt.Errorf("link 16 KB probe: %w", err)
	}
	out, err := exec.Command(readelf, "-l", "-W", so).CombinedOutput()
	if err != nil {
		return fmt.Errorf("read probe program headers: %w\n%s", err, out)
	}
	aligns, err := parseLoadAlignments(string(out))
	if err != nil {
		return err
	}
	if err := checkPageAlignment(aligns); err != nil {
		return err
	}
	log.Printf("%s: 16 KB page alignment verified (%d LOAD segments)", artifact, len(aligns))
	return nil
}

func firstN(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

func runCommand(cmd *exec.Cmd) error {
	log.Printf("+ %s", strings.Join(cmd.Args, " "))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", cmd.Args[0], err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
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
	// Preserve the archive's timestamp so repeated builds of the same
	// source do not look new to make-like consumers.
	if fi, err := os.Stat(src); err == nil {
		os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	}
	return nil
}

func loadOrInitManifest(path, releaseURL string) (manifest, error) {
	m, err := readManifest(path)
	if err == nil {
		if m.Commit != pinnedCommit {
			return manifest{}, fmt.Errorf("%s records commit %s, not the pinned %s", path, m.Commit, pinnedCommit)
		}
		if m.LibVersion != libVersion {
			return manifest{}, fmt.Errorf("%s records libVersion %s, the Go bindings expect %s", path, m.LibVersion, libVersion)
		}
		if m.Artifacts == nil {
			m.Artifacts = map[string]artifact{}
		}
		if releaseURL != "" {
			m.Release = strings.TrimSuffix(releaseURL, "/")
		}
		return m, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return newManifest(releaseURL), nil
	}
	return manifest{}, err
}

func writeSHA256SUMS(outDir string, m manifest) error {
	var b strings.Builder
	for _, t := range targets {
		a, ok := m.Artifacts[t.Key]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s  %s\n", a.SHA256, a.File)
	}
	if b.Len() == 0 {
		return nil
	}
	return os.WriteFile(filepath.Join(outDir, "SHA256SUMS"), []byte(b.String()), 0o644)
}
