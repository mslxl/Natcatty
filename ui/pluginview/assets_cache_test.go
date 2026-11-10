package pluginview

// The decoded asset caches are bounded per view. A guest can name many
// distinct assets, but retained decoded images and registered font families
// stay within the limits.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

func cachePNG(t *testing.T) []byte {
	t.Helper()
	return cachePNGSize(t, 2, 2)
}

func cachePNGSize(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buffer.Bytes()
}

func TestPackagedAssetCachesAreBounded(t *testing.T) {
	root := t.TempDir()
	data := cachePNG(t)
	font := goregular.TTF
	for i := 0; i < maxCachedImages+5; i++ {
		name := filepath.Join(root, fmt.Sprintf("image-%02d.png", i))
		if err := os.WriteFile(name, data, 0o644); err != nil {
			t.Fatalf("write image: %v", err)
		}
	}
	for i := 0; i < maxCachedFonts+1; i++ {
		name := filepath.Join(root, fmt.Sprintf("font-%02d.ttf", i))
		if err := os.WriteFile(name, font, 0o644); err != nil {
			t.Fatalf("write font: %v", err)
		}
	}

	assets := NewPackagedAssets(root)
	for i := 0; i < maxCachedImages+5; i++ {
		src := fmt.Sprintf("image-%02d.png", i)
		if _, ok := assets.Resolve(src); !ok {
			t.Fatalf("image %s did not resolve", src)
		}
	}
	assets.mu.Lock()
	images := len(assets.images)
	imageBytes := assets.imageBytes
	assets.mu.Unlock()
	if images > maxCachedImages {
		t.Fatalf("image cache holds %d entries, limit %d", images, maxCachedImages)
	}
	if imageBytes > maxCachedImageBytes {
		t.Fatalf("image cache holds %d bytes, limit %d", imageBytes, maxCachedImageBytes)
	}

	for i := 0; i < maxCachedFonts; i++ {
		src := fmt.Sprintf("font-%02d.ttf", i)
		if _, ok := assets.ResolveFont(src); !ok {
			t.Fatalf("font %s did not resolve", src)
		}
	}
	if _, ok := assets.ResolveFont(fmt.Sprintf("font-%02d.ttf", maxCachedFonts)); ok {
		t.Fatalf("font cache accepted entry %d past the limit", maxCachedFonts)
	}
	assets.mu.Lock()
	fonts := len(assets.fonts)
	assets.mu.Unlock()
	if fonts != maxCachedFonts {
		t.Fatalf("font cache holds %d entries, want %d", fonts, maxCachedFonts)
	}
}

func TestPackagedImageCacheIsByteBounded(t *testing.T) {
	root := t.TempDir()
	data := cachePNGSize(t, 1024, 1024)
	const count = 20
	for i := 0; i < count; i++ {
		name := filepath.Join(root, fmt.Sprintf("large-%02d.png", i))
		if err := os.WriteFile(name, data, 0o644); err != nil {
			t.Fatalf("write image: %v", err)
		}
	}

	assets := NewPackagedAssets(root)
	for i := 0; i < count; i++ {
		if _, ok := assets.Resolve(fmt.Sprintf("large-%02d.png", i)); !ok {
			t.Fatalf("image %d did not resolve", i)
		}
	}
	assets.mu.Lock()
	images := len(assets.images)
	imageBytes := assets.imageBytes
	assets.mu.Unlock()
	if images >= count {
		t.Fatalf("image cache retained all %d images past its byte budget", images)
	}
	if imageBytes > maxCachedImageBytes {
		t.Fatalf("image cache holds %d bytes, limit %d", imageBytes, maxCachedImageBytes)
	}
}

func TestPackagedAssetsWithoutAPackageDirectoryRejectsEveryID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), cachePNG(t), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "font.ttf"), goregular.TTF, 0o644); err != nil {
		t.Fatalf("write font: %v", err)
	}
	t.Chdir(dir)

	// An empty package directory must not fall back to the process working
	// directory, where these files do resolve.
	assets := NewPackagedAssets("")
	if source, ok := assets.Resolve("logo.png"); ok {
		t.Fatalf("empty root resolved logo.png from the working directory: %#v", source)
	}
	if family, ok := assets.ResolveFont("font.ttf"); ok {
		t.Fatalf("empty root resolved font.ttf from the working directory: %q", family)
	}
}
