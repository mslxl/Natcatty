package builtin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID:  "view.file",
		New: func() lens.Lens { return &viewFile{} },
	})
}

// viewFile is Terminal's Viewers family, `view.file` ("File contents"): the lens for the
// programs that print a file — `cat`, `bat`, `head`, `tail` (Terminal 0.9.14's lens
// catalog lists exactly those four programs). The view follows the file's kind, as
// Terminal does: Markdown renders, a JSON document becomes a tree ("JSON trees in
// command output (jq, curl, cat of a .json file)"), other text is a code view in the
// extension's syntax, and binary output says it is binary.
type viewFile struct {
	path   string
	cwd    string
	lines  []string
	binary bool
	hidden int

	// capture is the byte-stream path: when the command's file is an image
	// (by extension), the lens asks the engine for the raw bytes instead of
	// line-shaped text. bytes accumulates them; sniffed is the picture format
	// the first bytes identified, empty until the magic number is in.
	capture bool
	bytes   []byte
	sniffed string
	// sniffedDone reports that the sniff ran to a conclusion — positive or
	// negative — so Bytes does not run it again on every chunk. sniffed stays
	// empty when the conclusion was negative, which is how View tells the two
	// apart.
	sniffedDone bool
	// skip is how many leading bytes of bytes are shell noise rather than the
	// file's payload: the line editor's newline after accept-line, the newline
	// a shell prints between the command line and the program's first output.
	// sniffImageFormat scans for the magic number anywhere in the first few
	// bytes and reports the offset; Bytes records it here so View and the
	// trimmer can drop the prefix the file did not write.
	skip int
	// onlcr tracks whether the remote/local PTY line discipline translated
	// every 0x0a (\n) into 0x0d 0x0a (\r\n) (standard on SSH PTYs). Because
	// ONLCR is a lossless bijection, replacing \r\n with \n restores the exact
	// original image bytes.
	onlcr        onlcrState
	onlcrScanPos int
	lfTotal      int
	lfBare       int
	// done says the command printed everything: the picture's bytes are all in
	// and its view no longer grows. It is set as early as the file's own size
	// is known to have arrived (see Bytes), so a copy that ends exactly at the
	// last byte renders as complete before the shell's end mark is parsed.
	done bool
	// total is the file's size when it can be read from disk, for the progress
	// reading and to recognize the last byte. Zero when unknown (a remote
	// host, or a stream that is not the file).
	total int64
}

type onlcrState uint8

const (
	onlcrUnknown onlcrState = iota
	onlcrOff
	onlcrOn
)

// maxFileLines bounds the view like a block's output limit; the raw switch still
// holds every line.
const maxFileLines = 2000

// maxImageBytes bounds the picture a file lens reads from disk.
const maxImageBytes = 32 << 20

// imageExts are the formats Terminal's image surface and MyGo's ui.DecodeBitmap
// both read (GIF's first frame).
var imageExts = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true,
	"webp": true, "bmp": true,
}

func isImagePath(p string) bool {
	return imageExts[strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))]
}

// resolveFilePath resolves the command's file argument against its working
// directory.
func resolveFilePath(cwd, p string) string {
	if filepath.IsAbs(p) || cwd == "" {
		return p
	}
	return filepath.Join(cwd, p)
}

func (l *viewFile) ID() string { return "view.file" }

func (l *viewFile) Match(cmd model.Cmd) bool {
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(cmd.Program)), ".exe") {
	case "cat", "bat", "head":
		return true
	case "tail":
		// `tail -f` follows a log: that is the Viewers family's view.follow, not a
		// file snapshot.
		return !hasFollowFlag(cmd.Args)
	}
	return false
}

func (l *viewFile) Open(cmd model.Cmd, _ *lens.Ctx) (any, error) {
	path := lastPathArg(cmd.Args)
	st := &viewFile{path: path, cwd: cmd.Cwd}
	// The file's size tells the growing view when the last byte has arrived,
	// and gives the progress reading something to count against. It is only
	// available when the file is on this machine; a remote host's file, or an
	// interrupted copy, simply has no total.
	if path != "" && isImagePath(path) {
		if info, err := os.Stat(resolveFilePath(cmd.Cwd, path)); err == nil && info.Mode().IsRegular() {
			st.total = info.Size()
		}
	}
	return st, nil
}

// CaptureBytes implements lens.ByteSink: an image file's bytes already are the
// picture, so the lens captures them rather than re-reading the file from
// disk. This is what makes cat of an image work over SSH (the bytes reach the
// terminal regardless of where the file lives), and it is what keeps the
// terminal responsive at all — textifying a few megabytes of high bytes used
// to flood the capture screen with tens of thousands of garbage rows.
func (l *viewFile) CaptureBytes(state any) bool {
	s, ok := state.(*viewFile)
	if !ok {
		return false
	}
	s.capture = s.path != "" && isImagePath(s.path)
	return s.capture
}

// Bytes accumulates the raw byte stream. The sniff runs once the magic number
// of the format is in (12 bytes is enough for every format imageExts lists).
func (l *viewFile) Bytes(state any, b []byte) error {
	s, ok := state.(*viewFile)
	if !ok {
		return errBadState
	}
	if len(s.bytes) < maxImageBytes {
		keep := maxImageBytes - len(s.bytes)
		if keep > len(b) {
			keep = len(b)
		}
		s.bytes = append(s.bytes, b[:keep]...)
	}
	if !s.sniffedDone {
		format, skip, onlcr, more := sniffImageFormat(s.bytes)
		if more {
			return nil // wait for more bytes before deciding
		}
		s.sniffed = format // may be "": definitively not a picture
		s.skip = skip
		s.sniffedDone = true
		if format == "png" {
			// PNG's magic header contains two 0x0a bytes, so its signature
			// tells ONLCR on vs off deterministically from byte 0.
			if onlcr {
				s.onlcr = onlcrOn
			} else {
				s.onlcr = onlcrOff
			}
		}
	}
	if s.sniffed != "" && s.skip < len(s.bytes) {
		s.scanONLCR(s.bytes[s.skip:])
	}
	// The whole file has arrived: the picture is complete even though the
	// shell's end mark has not been parsed yet. That keeps the last frame from
	// being a row short while the mark is in flight.
	if s.total > 0 {
		received := int64(len(s.bytes)) - int64(s.skip)
		if s.onlcr == onlcrOn {
			received -= int64(s.lfTotal)
		}
		if received >= s.total {
			s.done = true
		}
	}
	return nil
}

// scanONLCR inspects newly arrived payload bytes to determine whether the PTY
// had ONLCR enabled (every 0x0a preceded by 0x0d). A single bare 0x0a proves
// ONLCR is off; seeing multiple 0x0a bytes all preceded by 0x0d proves ONLCR
// is on (false-positive probability < 1 in 16 million).
func (s *viewFile) scanONLCR(payload []byte) {
	for i := s.onlcrScanPos; i < len(payload); i++ {
		if payload[i] == 0x0a {
			s.lfTotal++
			if i == 0 || payload[i-1] != 0x0d {
				s.lfBare++
				if s.onlcr == onlcrUnknown {
					s.onlcr = onlcrOff
				}
			}
		}
	}
	s.onlcrScanPos = len(payload)
	if s.onlcr == onlcrUnknown && s.lfBare == 0 && s.lfTotal >= 3 {
		s.onlcr = onlcrOn
	}
}

// undoONLCR is the exact lossless inverse of PTY ONLCR: every \r\n (0x0d 0x0a)
// becomes \n (0x0a), leaving bare \r and all other bytes untouched.
func undoONLCR(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == 0x0d && i+1 < len(b) && b[i+1] == 0x0a {
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// sniffImageFormat identifies a picture in the captured stream. The magic
// number is searched anywhere in the first sniffMaxPrefix bytes after leading
// shell control sequences. skip is how many leading bytes precede the magic
// number; onlcr reports that a PNG signature carried ONLCR CRLF expansion.
func sniffImageFormat(b []byte) (format string, skip int, onlcr bool, more bool) {
	if len(b) < sniffMinBytes {
		return "", 0, false, true
	}
	// The noise ahead of the file is whatever the shell wrote before its first
	// byte, and it is not just a newline. A prompt framework puts window-title
	// and title-stack sequences there:
	//
	//	\x1b]2;cat 121061214_p0.png\x07\x1b]1;cat\x07
	//
	// Skipping complete leading control sequences and line breaks finds the
	// signature behind them.
	start := skipLeadingControls(b)
	if start >= len(b) {
		return "", 0, false, true
	}
	// If an escape sequence at `start` has not finished yet (chunk boundary),
	// wait for more bytes up to sniffMaxPrefix before giving up.
	if b[start] == 0x1b && len(b) < sniffMaxPrefix {
		return "", 0, false, true
	}
	// Wait until at least sniffMinBytes of payload after leading controls have
	// arrived so a signature split across TCP/PTY reads is never misclassified.
	if len(b)-start < sniffMinBytes {
		return "", 0, false, true
	}
	limit := min(len(b), start+sniffMaxPrefix)
	for j := start; j < limit; j++ {
		if f, crlf := magicAt(b[j:]); f != "" {
			return f, j, crlf, false
		}
	}
	return "", 0, false, false
}

// skipLeadingControls returns how many bytes at the front of b are complete
// escape sequences — OSC (`ESC ] … BEL` or `ESC ] … ST`), CSI (`ESC [ … final`)
// and the two-byte forms — or leading CR/LF line breaks, so the signature
// search starts at the first real output byte.
func skipLeadingControls(b []byte) int {
	i := 0
	for i < len(b) {
		if b[i] == '\r' || b[i] == '\n' {
			i++
			continue
		}
		if b[i] != 0x1b {
			return i
		}
		if i+1 >= len(b) {
			return i
		}
		switch b[i+1] {
		case ']': // OSC: runs to BEL or ESC \
			j := i + 2
			for j < len(b) {
				if b[j] == 0x07 {
					i = j + 1
					break
				}
				if b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\' {
					i = j + 2
					break
				}
				j++
			}
			if j >= len(b) {
				return i // the sequence has not arrived whole
			}
		case '[': // CSI: runs to a final byte in 0x40..0x7e
			j := i + 2
			for j < len(b) {
				if b[j] >= 0x40 && b[j] <= 0x7e {
					i = j + 1
					break
				}
				j++
			}
			if j >= len(b) {
				return i
			}
		default: // two-byte escape, and the character-set selections
			if i+2 > len(b) {
				return i
			}
			i += 2
		}
	}
	return i
}

// sniffScanBytes is how far past the prefix's newline sniffImageFormat keeps
// looking for a magic number: a command line that wrapped puts the signature on
// the next line.
const sniffScanBytes = 16

// sniffMaxPrefix bounds the whole scan. A shell's echo before the program's
// first byte is one command line; a prefix longer than this is output, not a
// picture this command printed.
const sniffMaxPrefix = 4096

// sniffMinBytes is how many bytes must be in before sniffImageFormat decides.
// It is the longest magic the surface decodes (WebP's twelve, ONLCR PNG's ten)
// rounded up, so every format can be confirmed or ruled out from one look.
const sniffMinBytes = 16

// magicAt reports the picture format whose magic number is at the start of b,
// and whether the signature shows PTY ONLCR (\n -> \r\n) expansion.
func magicAt(b []byte) (string, bool) {
	pngMagic := []byte("\x89PNG\r\n\x1a\n")
	pngMagicONLCR := []byte("\x89PNG\r\r\n\x1a\r\n")
	gif87 := []byte("GIF87a")
	gif89 := []byte("GIF89a")
	riff := []byte("RIFF")
	webpAt8 := []byte("WEBP")
	// JPEG: FF D8 FF (three bytes, no further constraint).
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return "jpg", false
	}
	if len(b) >= len(pngMagic) && bytes.Equal(b[:len(pngMagic)], pngMagic) {
		return "png", false
	}
	if len(b) >= len(pngMagicONLCR) && bytes.Equal(b[:len(pngMagicONLCR)], pngMagicONLCR) {
		return "png", true
	}
	if len(b) >= 6 && (bytes.Equal(b[:6], gif87) || bytes.Equal(b[:6], gif89)) {
		return "gif", false
	}
	// WebP needs RIFF....WEBP, twelve bytes minimum.
	if len(b) >= 12 && bytes.Equal(b[:4], riff) && bytes.Equal(b[8:12], webpAt8) {
		return "webp", false
	}
	// BMP: 'B' 'M'.
	if len(b) >= 2 && b[0] == 'B' && b[1] == 'M' {
		return "bmp", false
	}
	return "", false
}

// trimImageBytes cuts trailing bytes that are not part of the picture. The
// capture runs until OSC 133;D, but a shell prompt escape, a trailing newline
// the program printed after the file, or a fragment of a terminal sequence
// the binary stream happened to decode as can land in the buffer after the
// picture's own end marker. JPEG and PNG both carry an explicit end inside
// their format, so the trailing garbage is unambiguous; the other formats
// (GIF, WebP, BMP) are returned whole because their lengths are encoded in
// headers this package does not parse, and a trailing newline does not affect
// their decode.
func trimImageBytes(b []byte, format string) []byte {
	switch format {
	case "jpg":
		return trimJPEG(b)
	case "png":
		// Walk the chunk structure instead of searching for the bytes "IEND":
		// a compressed IDAT stream contains any four bytes often enough that a
		// search finds a false match and cuts the picture in half. That is what
		// made a real screenshot ("cannot decode") while a 32-pixel probe was
		// fine — the probe's data happened to hold no such sequence.
		//
		// The structure after the 8-byte signature is a length, a 4-byte type,
		// that many data bytes, and a 4-byte CRC, repeated until IEND.
		pos := 8
		for pos+8 <= len(b) {
			length := int(uint32(b[pos])<<24 | uint32(b[pos+1])<<16 | uint32(b[pos+2])<<8 | uint32(b[pos+3]))
			chunkType := string(b[pos+4 : pos+8])
			end := pos + 8 + length + 4
			if length < 0 || end > len(b) {
				// The capture stopped inside the chunk: nothing after it can be
				// trusted, so the bytes so far are all there is.
				return b
			}
			if chunkType == "IEND" {
				return b[:end]
			}
			pos = end
		}
		return b
	}
	return b
}

// trimJPEG walks JPEG marker segments from SOI (FFD8) to the real EOI (FFD9)
// so embedded EXIF thumbnails (inside length-prefixed APP1 segments, each with
// their own FFD9) are skipped rather than truncating the photograph.
func trimJPEG(b []byte) []byte {
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return b
	}
	i := 2
	for i+1 < len(b) {
		if b[i] != 0xff {
			return b
		}
		for i < len(b) && b[i] == 0xff {
			i++
		}
		if i >= len(b) {
			return b
		}
		marker := b[i]
		i++
		switch {
		case marker == 0xd9: // EOI
			return b[:i]
		case marker == 0x00 || marker == 0x01 || (marker >= 0xd0 && marker <= 0xd8):
			continue
		case marker == 0xda: // SOS: header followed by entropy-coded scan data
			if i+2 > len(b) {
				return b
			}
			segLen := int(b[i])<<8 | int(b[i+1])
			if segLen < 2 || i+segLen > len(b) {
				return b
			}
			i += segLen
			for i < len(b) {
				if b[i] != 0xff {
					i++
					continue
				}
				j := i + 1
				for j < len(b) && b[j] == 0xff {
					j++
				}
				if j >= len(b) {
					return b
				}
				next := b[j]
				if next == 0x00 || (next >= 0xd0 && next <= 0xd7) {
					i = j + 1
					continue
				}
				i = j - 1
				break
			}
		default:
			if i+2 > len(b) {
				return b
			}
			segLen := int(b[i])<<8 | int(b[i+1])
			if segLen < 2 || i+segLen > len(b) {
				return b
			}
			i += segLen
		}
	}
	return b
}

// lastPathArg is the file the command prints: the last argument that is not an
// option, so `head -n 5 file` and `bat --style plain file` both name file.
func lastPathArg(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		a := unquoteArg(args[i])
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

// unquoteArg removes the shell quoting a command line carries. The line reaches
// the lens as the shell reported it, so a path with a space in it arrives
// wrapped in single or double quotes (`cat "my logo.png"`), or with
// backslash-escaped spaces (`cat my\ logo.png`). Without this the extension
// test saw `png"`, the file lens never captured the bytes, and the picture came
// out as a code block of mojibake.
func unquoteArg(a string) string {
	if len(a) >= 2 {
		if (a[0] == '"' && a[len(a)-1] == '"') || (a[0] == '\'' && a[len(a)-1] == '\'') {
			inner := a[1 : len(a)-1]
			if a[0] == '"' {
				// Only the escapes a double-quoted shell word keeps: \" \\ \$ \`
				var b strings.Builder
				for i := 0; i < len(inner); i++ {
					if inner[i] == '\\' && i+1 < len(inner) {
						switch inner[i+1] {
						case '"', '\\', '$', '`':
							b.WriteByte(inner[i+1])
							i++
							continue
						}
					}
					b.WriteByte(inner[i])
				}
				return b.String()
			}
			return inner
		}
	}
	if strings.Contains(a, `\ `) {
		return strings.ReplaceAll(a, `\ `, " ")
	}
	return a
}

func (l *viewFile) Line(st any, ln model.Line) error {
	s, ok := st.(*viewFile)
	if !ok {
		return errBadState
	}
	if strings.ContainsRune(ln.Text, 0) {
		s.binary = true
	}
	if len(s.lines) < maxFileLines {
		s.lines = append(s.lines, ln.Text)
		return nil
	}
	s.hidden++
	return nil
}

func (l *viewFile) Finish(st any, _ int) error {
	if s, ok := st.(*viewFile); ok {
		s.done = true
		// For small images over SSH that arrived in full with fewer than 3 LFs
		// (or fewer than sniffMinBytes total), finalize sniffing / ONLCR state.
		if s.capture && !s.sniffedDone && len(s.bytes) > 0 {
			start := skipLeadingControls(s.bytes)
			for j := start; j < len(s.bytes) && j < start+sniffMaxPrefix; j++ {
				if f, crlf := magicAt(s.bytes[j:]); f != "" {
					s.sniffed = f
					s.skip = j
					if f == "png" {
						if crlf {
							s.onlcr = onlcrOn
						} else {
							s.onlcr = onlcrOff
						}
					}
					break
				}
			}
			s.sniffedDone = true
		}
		if s.sniffed != "" && s.skip < len(s.bytes) {
			s.scanONLCR(s.bytes[s.skip:])
		}
		if s.onlcr == onlcrUnknown && s.lfBare == 0 && s.lfTotal > 0 {
			s.onlcr = onlcrOn
		}
	}
	return nil
}

// placeholderSize is the picture's pixel size, read from a prefix whose decoder
// refused it. GIF needs this: its header names the canvas, but the LZW stream
// cannot be drawn before the file is whole, so the block holds that shape with
// a stand-in. The other formats stream their rows and need no placeholder.
func placeholderSize(data []byte, format string) [2]int {
	if format != "gif" || len(data) < 10 {
		return [2]int{}
	}
	if string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a" {
		return [2]int{}
	}
	w := int(data[6]) | int(data[7])<<8
	h := int(data[8]) | int(data[9])<<8
	if w <= 0 || h <= 0 || w > 20000 || h > 20000 {
		return [2]int{}
	}
	return [2]int{w, h}
}

func (l *viewFile) View(st any) (lens.Node, error) {
	s, ok := st.(*viewFile)
	if !ok {
		return nil, errBadState
	}
	// A picture is a file surface, not text: Terminal shows the image itself. Read it
	// from disk (the command's output is binary), bounded like a block's output.
	// The captured stream wins over the disk read: it is what the command
	// actually printed, so it cannot disagree with the file the user sees, and
	// it is the only source that exists on a remote host. The disk read stays
	// as the fallback for a partial capture (a cat interrupted in the middle)
	// or a stream the magic sniff could not identify.
	if s.capture {
		if s.sniffed != "" && len(s.bytes) > 0 {
			payload := s.bytes
			if s.skip > 0 && s.skip < len(payload) {
				payload = payload[s.skip:]
			}
			var picture []byte
			if s.onlcr == onlcrOn {
				// undoONLCR allocates a fresh slice, so it also isolates the
				// view from concurrent Bytes() appends on s.bytes.
				picture = trimImageBytes(undoONLCR(payload), s.sniffed)
			} else {
				trimmed := trimImageBytes(payload, s.sniffed)
				// Snapshot the bytes so concurrent Bytes() appends on the
				// session read loop cannot race with UI rendering.
				picture = append([]byte(nil), trimmed...)
			}
			// Complete is the picture's expected file size (without shell noise
			// prefix s.skip), so the renderer can tell a prefix from the whole
			// file.
			complete := len(picture)
			if s.total > 0 && int(s.total) > complete {
				complete = int(s.total)
			}
			return lens.Image{
				Data:            picture,
				Path:            s.path,
				Alt:             filepath.Base(s.path),
				Growing:         !s.done,
				Total:           s.total,
				Complete:        complete,
				PlaceholderSize: placeholderSize(picture, s.sniffed),
			}, nil
		}
		// capture mode but the bytes do not look like a picture: show what
		// the bytes are rather than pretend the file is one.
		if len(s.bytes) > 0 {
			return lens.Cols(lens.Spans(lens.Span{Text: "Binary file", Style: "muted"})), nil
		}
	}
	if s.path != "" && isImagePath(s.path) {
		if data, err := os.ReadFile(resolveFilePath(s.cwd, s.path)); err == nil && len(data) <= maxImageBytes {
			return lens.Image{Data: data, Path: s.path, Alt: filepath.Base(s.path)}, nil
		}
	}
	if len(s.lines) == 0 {
		return nil, nil // nothing printed: leave the raw body
	}
	text := strings.Join(s.lines, "\n")
	if strings.TrimSpace(text) == "" && !s.binary {
		return nil, nil
	}
	var node lens.Node
	switch {
	case s.binary:
		// Terminal's own words for a file it cannot show (strings near the file view).
		node = lens.Cols(lens.Spans(lens.Span{Text: "Binary file", Style: "muted"}))
	default:
		node = fileNode(s.path, text)
	}
	if s.hidden > 0 {
		node = lens.Cols(node, lens.Overflow{Hidden: s.hidden})
	}
	return node, nil
}

// fileNode picks the view for a text file by its extension.
func fileNode(path, text string) lens.Node {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case "md", "markdown", "mdown", "mkd":
		return lens.Markdown{Text: text}
	case "diff", "patch":
		add, del := diffCounts(text)
		return lens.Diff{Text: text, Path: diffTargetPath(text), Added: add, Removed: del}
	case "json", "jsonl", "ndjson":
		if n, ok := lens.JSONTree(text); ok {
			return n
		}
	case "yaml", "yml":
		if n, ok := lens.JSONTree(text); ok {
			return n
		}
	}
	return lens.Code{Text: text, Lang: ext, Start: 1}
}

func (l *viewFile) Event(any, lens.Event) error { return nil }
