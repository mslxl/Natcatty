package pluginview

// Packaged asset loading. The native UI ABI carries asset ids, never bytes or
// URLs: the host resolves an id inside the plugin's read-only package
// directory, enforces the type and size limits, and caches the decoded result
// for a bounded number of entries. A missing, escaping, remote, unsupported,
// or oversized asset renders the node's fallback instead of failing the view.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
)

const (
	// MaxPackagedImageBytes bounds one packaged image file.
	MaxPackagedImageBytes = 8 << 20
	// MaxPackagedFontBytes bounds one packaged font file.
	MaxPackagedFontBytes = 4 << 20
	// MaxPackagedImageDimension bounds the intrinsic width and height of a
	// decoded image, so a small compressed file cannot allocate a huge bitmap.
	MaxPackagedImageDimension = 8192
	// MaxPackagedImagePixels bounds the decoded pixel count of an image, so a
	// wide but short file cannot allocate a huge bitmap either.
	MaxPackagedImagePixels = 8 << 20
	// maxCachedImages, maxCachedImageBytes, and maxCachedFonts bound the
	// decoded assets retained by one view. Fonts stay registered with the text
	// system once loaded, so the font cap is a hard per-view limit.
	maxCachedImages     = 64
	maxCachedImageBytes = 64 << 20
	maxCachedFonts      = 16
)

// cachedImage is one decoded image and the bytes its pixels retain.
type cachedImage struct {
	source ui.ImageSource
	bytes  int64
}

var imageAssetExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".bmp": true, ".svg": true,
}

var fontAssetExtensions = map[string]bool{
	".ttf": true, ".otf": true, ".ttc": true,
}

// PackagedAssets resolves asset ids inside one plugin package directory. It
// implements both AssetResolver and FontResolver. It is safe for concurrent
// use; the UI renderer calls it during a frame.
type PackagedAssets struct {
	root string

	mu         sync.Mutex
	images     map[string]cachedImage
	imageBytes int64
	fonts      map[string]string
}

// NewPackagedAssets returns a resolver rooted at a plugin package directory.
// An empty, missing, or unresolvable directory still yields a resolver that
// rejects every asset; it never falls back to the process working directory.
func NewPackagedAssets(root string) *PackagedAssets {
	canonical := ""
	if root != "" {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			resolved, err = filepath.Abs(root)
			if err != nil {
				resolved = ""
			}
		}
		canonical = resolved
	}
	return &PackagedAssets{
		root:   canonical,
		images: make(map[string]cachedImage),
		fonts:  make(map[string]string),
	}
}

// Resolve loads a packaged raster or SVG image. A remote URL, an escaping
// path, an unsupported extension, an oversized file, or a decode failure
// reports false.
func (p *PackagedAssets) Resolve(src string) (ui.ImageSource, bool) {
	if p == nil || src == "" {
		return nil, false
	}
	p.mu.Lock()
	cached, ok := p.images[src]
	p.mu.Unlock()
	if ok {
		return cached.source, true
	}

	data, ext, ok := p.read(src, imageAssetExtensions, MaxPackagedImageBytes)
	if !ok {
		return nil, false
	}
	var source ui.ImageSource
	var retained int64
	switch ext {
	case ".svg":
		svg, err := ui.ParseSVG(data)
		if err != nil {
			return nil, false
		}
		width, height := svg.Size()
		if !validImageSize(float64(width), float64(height)) {
			return nil, false
		}
		source = svg
		retained = int64(len(data))
	default:
		bitmap, err := ui.DecodeBitmap(data)
		if err != nil {
			return nil, false
		}
		width, height := bitmap.Size()
		if !validImageSize(float64(width), float64(height)) {
			return nil, false
		}
		source = bitmap
		retained = int64(width) * int64(height) * 4
	}

	p.mu.Lock()
	if cached, exists := p.images[src]; exists {
		p.mu.Unlock()
		return cached.source, true
	}
	if retained <= maxCachedImageBytes {
		for len(p.images) >= maxCachedImages ||
			(len(p.images) > 0 && p.imageBytes+retained > maxCachedImageBytes) {
			for key, evicted := range p.images {
				delete(p.images, key)
				p.imageBytes -= evicted.bytes
				break
			}
		}
		p.images[src] = cachedImage{source: source, bytes: retained}
		p.imageBytes += retained
	}
	p.mu.Unlock()
	return source, true
}

// ResolveFont registers a packaged TrueType, OpenType, or collection font
// and returns the family the renderer uses. Loading the same id twice returns
// the same family. An unsupported, oversized, or invalid font reports false.
func (p *PackagedAssets) ResolveFont(src string) (string, bool) {
	if p == nil || src == "" {
		return "", false
	}
	p.mu.Lock()
	family, cached := p.fonts[src]
	full := len(p.fonts) >= maxCachedFonts
	p.mu.Unlock()
	if cached {
		return family, true
	}
	if full {
		return "", false
	}

	data, _, ok := p.read(src, fontAssetExtensions, MaxPackagedFontBytes)
	if !ok {
		return "", false
	}
	family = packagedFontFamily(p.root, src)
	if err := ui.RegisterFont(data, family); err != nil {
		// A concurrent lookup may have registered the same family first.
		p.mu.Lock()
		family, cached = p.fonts[src]
		p.mu.Unlock()
		if cached {
			return family, true
		}
		return "", false
	}
	p.mu.Lock()
	p.fonts[src] = family
	p.mu.Unlock()
	return family, true
}

// read validates the id and limits and returns the file bytes with the
// lower-case extension of the id. The asset must resolve to a regular file
// inside the package directory even after symbolic links are followed.
func (p *PackagedAssets) read(src string, extensions map[string]bool,
	limit int64) ([]byte, string, bool) {
	ext := strings.ToLower(filepath.Ext(src))
	if p.root == "" || !extensions[ext] || !domainnativeui.ValidAssetID(src) {
		return nil, "", false
	}
	full := filepath.Join(p.root, filepath.FromSlash(src))
	if !insideRoot(p.root, full) {
		return nil, "", false
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil || !insideRoot(p.root, real) {
		return nil, "", false
	}
	file, err := os.Open(real)
	if err != nil {
		return nil, "", false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, "", false
	}
	return data, ext, true
}

// insideRoot reports whether path is a proper descendant of root.
func insideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validImageSize(width, height float64) bool {
	if math.IsNaN(width) || math.IsNaN(height) || math.IsInf(width, 0) || math.IsInf(height, 0) {
		return false
	}
	if width <= 0 || height <= 0 {
		return false
	}
	return width <= MaxPackagedImageDimension && height <= MaxPackagedImageDimension &&
		width*height <= MaxPackagedImagePixels
}

// packagedFontFamily derives a stable family name that cannot collide with
// another package's asset of the same id.
func packagedFontFamily(root, src string) string {
	sum := sha256.Sum256([]byte(root + "\x00" + src))
	return "plugin-" + hex.EncodeToString(sum[:8])
}
