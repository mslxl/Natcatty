package app

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/internal/testutil"
)

func TestBundledMapleMonoNFFilesMatchPinnedChecksums(t *testing.T) {
	dir := filepath.Join(testutil.ModuleRoot(t), "resources", filepath.FromSlash(bundledMapleMonoNFDir))
	want := map[string]string{
		"MapleMono-NF-Regular.ttf":    "dbf160ce96358056a47777e95a50af208b438312787cf20917ce2321066de8e5",
		"MapleMono-NF-Bold.ttf":       "cf6fb13160424257ea764a5deb1d55116c9ab5c347cb69c6be2576b44dc3d544",
		"MapleMono-NF-Italic.ttf":     "f20012da3a738ff070292aae453b5f7b47de3992451a581cd4ba6f7fed7b9785",
		"MapleMono-NF-BoldItalic.ttf": "97a40e0a25c8adb2c9e29d755a127b7fae989debf40e9f62ca3e393b1de2d429",
		"LICENSE.txt":                 "eb2d28d2e565a0757e3d64e34ebb452e75a0cad87c0ab3faf4e08ba7596de902",
	}
	for name, expected := range want {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read bundled font asset %s: %v", name, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != expected {
			t.Errorf("%s sha256 = %s, want %s", name, got, expected)
		}
	}
}

func TestRegisterBundledFontsRegistersExpectedFacesPerPlatform(t *testing.T) {
	resourcesDir := filepath.Join(testutil.ModuleRoot(t), "resources")

	for _, goos := range []string{"darwin", "windows", "linux", "ios"} {
		t.Run(goos, func(t *testing.T) {
			var families []string
			var sizes []int
			err := registerBundledFontsFromRoots([]string{resourcesDir}, goos, func(data []byte, family string) error {
				families = append(families, family)
				sizes = append(sizes, len(data))
				return nil
			})
			if err != nil {
				t.Fatalf("registerBundledFontsFromRoots(%s): %v", goos, err)
			}
			if len(families) != 4 {
				t.Fatalf("registered %d faces on %s, want 4", len(families), goos)
			}
			for i, family := range families {
				if family != domainpreferences.BundledTerminalFontFamily {
					t.Errorf("face %d family = %q, want %q", i, family, domainpreferences.BundledTerminalFontFamily)
				}
				if sizes[i] == 0 {
					t.Errorf("face %d has zero length", i)
				}
			}
		})
	}

	t.Run("android", func(t *testing.T) {
		var calls int
		var registeredData []byte
		err := registerBundledFontsFromRoots([]string{resourcesDir}, "android", func(data []byte, family string) error {
			calls++
			registeredData = data
			if family != domainpreferences.BundledTerminalFontFamily {
				t.Errorf("family = %q, want %q", family, domainpreferences.BundledTerminalFontFamily)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("registerBundledFontsFromRoots(android): %v", err)
		}
		if calls != 1 {
			t.Fatalf("registered %d faces on android, want only Regular (1)", calls)
		}
		regular, err := os.ReadFile(filepath.Join(resourcesDir, filepath.FromSlash(bundledMapleMonoNFDir), "MapleMono-NF-Regular.ttf"))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(registeredData, regular) {
			t.Error("android did not register MapleMono-NF-Regular.ttf")
		}
	})
}

func TestBundledFontRootsIncludeNixAndBundleLocations(t *testing.T) {
	roots := bundledFontRoots("/override", "/pkg/resources", "/nix/store/abc-natcatty/bin/natcatty", "/repo")
	joined := strings.Join(roots, "\n")
	for _, want := range []string{
		"/override",
		"/pkg/resources",
		filepath.Join("/nix/store/abc-natcatty/bin", "resources"),
		filepath.Join("/repo", "resources"),
	} {
		abs, _ := filepath.Abs(want)
		if !strings.Contains(joined, abs) {
			t.Errorf("bundledFontRoots missing %q in:\n%s", abs, joined)
		}
	}
}

func TestBundledMapleMonoNFRegistersInMyGoTextSystem(t *testing.T) {
	resourcesDir := filepath.Join(testutil.ModuleRoot(t), "resources")
	if err := registerBundledFontsFromRoots([]string{resourcesDir}, runtime.GOOS, ui.RegisterFont); err != nil {
		t.Fatalf("ui.RegisterFont failed for bundled Maple Mono NF: %v", err)
	}
	found := false
	for _, family := range ui.FontFamilies() {
		if family.Name == domainpreferences.BundledTerminalFontFamily && family.Registered {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%q not found among registered ui.FontFamilies()", domainpreferences.BundledTerminalFontFamily)
	}
}
