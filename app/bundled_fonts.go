package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

const bundledMapleMonoNFDir = "fonts/maple-mono-nf"

var bundledMapleMonoNFFiles = []string{
	"MapleMono-NF-Regular.ttf",
	"MapleMono-NF-Bold.ttf",
	"MapleMono-NF-Italic.ttf",
	"MapleMono-NF-BoldItalic.ttf",
}

var bundledFontsOnce sync.Once

// registerBundledFonts loads the unmodified Maple Mono NF font files shipped
// under resources/fonts/maple-mono-nf and registers them under MyGo's text
// system. Failure logs a warning instead of aborting startup because the
// terminal font stack always falls back to system monospace.
func registerBundledFonts() {
	bundledFontsOnce.Do(func() {
		resourceDir, _ := mygo.App.Path(mygo.PathResources)
		executable, _ := os.Executable()
		workingDir, _ := os.Getwd()
		roots := bundledFontRoots(os.Getenv("NATCATTY_RESOURCES"), resourceDir, executable, workingDir)
		if err := registerBundledFontsFromRoots(roots, runtime.GOOS, ui.RegisterFont); err != nil {
			log.Printf("natcatty: bundled terminal font (%s) not registered: %v",
				domainpreferences.BundledTerminalFontFamily, err)
		}
	})
}

// bundledFontRoots returns candidate directories that may hold the bundled
// fonts/maple-mono-nf tree across packaged desktop bundles, Nix store installs,
// mobile bundles, and local `go run` checkouts.
func bundledFontRoots(envOverride, resourceDir, executablePath, workingDir string) []string {
	var roots []string
	add := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			absolute = root
		}
		for _, existing := range roots {
			if strings.EqualFold(existing, absolute) {
				return
			}
		}
		roots = append(roots, absolute)
	}
	add(envOverride)
	add(resourceDir)
	if executablePath != "" {
		exeDir := filepath.Dir(executablePath)
		add(exeDir)
		// Nix packages copy the resources directory beside the executable at
		// $out/bin/resources rather than flattening its contents into $out/bin.
		add(filepath.Join(exeDir, "resources"))
		add(filepath.Join(exeDir, "..", "Resources"))
	}
	if workingDir != "" {
		add(filepath.Join(workingDir, "resources"))
	}
	return roots
}

// bundledFontFilesFor returns the subset of bundled Maple Mono NF faces to
// register on goos. MyGo's Android text engine stores a single Typeface per
// registered family name and synthesizes bold/italic from it, so registering
// additional faces after Regular would overwrite Regular with BoldItalic.
func bundledFontFilesFor(goos string) []string {
	if goos == "android" {
		return bundledMapleMonoNFFiles[:1]
	}
	return bundledMapleMonoNFFiles
}

func registerBundledFontsFromRoots(roots []string, goos string, register func(data []byte, family string) error) error {
	fontDir, err := locateBundledFontDir(roots)
	if err != nil {
		return err
	}
	family := domainpreferences.BundledTerminalFontFamily
	for _, name := range bundledFontFilesFor(goos) {
		path := filepath.Join(fontDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := register(data, family); err != nil {
			return fmt.Errorf("register %s as %q: %w", name, family, err)
		}
	}
	return nil
}

func locateBundledFontDir(roots []string) (string, error) {
	relDir := filepath.FromSlash(bundledMapleMonoNFDir)
	for _, root := range roots {
		candidate := filepath.Join(root, relDir)
		probe := filepath.Join(candidate, bundledMapleMonoNFFiles[0])
		if info, err := os.Stat(probe); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("fonts/maple-mono-nf not found in resource roots")
}
