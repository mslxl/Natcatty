package builtin

import (
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
}

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
	return &viewFile{path: lastPathArg(cmd.Args), cwd: cmd.Cwd}, nil
}

// lastPathArg is the file the command prints: the last argument that is not an
// option, so `head -n 5 file` and `bat --style plain file` both name file.
func lastPathArg(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		a := args[i]
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
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

func (l *viewFile) Finish(any, int) error { return nil }

func (l *viewFile) View(st any) (lens.Node, error) {
	s, ok := st.(*viewFile)
	if !ok {
		return nil, errBadState
	}
	// A picture is a file surface, not text: Terminal shows the image itself. Read it
	// from disk (the command's output is binary), bounded like a block's output.
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
