package builtin

import (
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// rust.tree is Terminal's "Cargo dependency trees" lens in the Builds family:
// `cargo tree`. The output is cargo's box-drawing tree, so the view is a
// collapsible tree node whose labels are the crate name, version and source.
func init() {
	lens.Register(lens.Definition{
		ID:  "rust.tree",
		New: func() lens.Lens { return &rustTree{} },
	})
}

type rustTree struct{ lines []string }

func (l *rustTree) ID() string { return "rust.tree" }

func (l *rustTree) Match(cmd model.Cmd) bool {
	if program(cmd) != "cargo" {
		return false
	}
	for _, a := range cmd.Args {
		if a == "tree" {
			return true
		}
		if !strings.HasPrefix(a, "-") {
			return false // the first positional is not `tree`
		}
	}
	return false
}

func (l *rustTree) Open(_ model.Cmd, _ *lens.Ctx) (any, error) { return &rustTree{}, nil }

func (l *rustTree) Line(st any, ln model.Line) error {
	s, ok := st.(*rustTree)
	if !ok {
		return errBadState
	}
	if len(s.lines) < maxFileLines {
		s.lines = append(s.lines, ln.Text)
	}
	return nil
}

func (l *rustTree) Finish(any, int) error { return nil }

func (l *rustTree) Event(any, lens.Event) error { return nil }

func (l *rustTree) View(st any) (lens.Node, error) {
	s, ok := st.(*rustTree)
	if !ok {
		return nil, errBadState
	}
	if len(s.lines) == 0 {
		return nil, nil
	}
	// Without a branch marker the output is an error or a single crate, not a
	// dependency tree; leave it raw rather than claiming prose as a tree.
	hasBranch := false
	for _, ln := range s.lines {
		if strings.Contains(ln, "├── ") || strings.Contains(ln, "└── ") {
			hasBranch = true
			break
		}
	}
	if !hasBranch {
		// A dependency-free crate prints one line, "name vX.Y.Z (source)"; an
		// error prints prose, which must stay raw.
		if len(s.lines) == 1 && looksLikeCargoRoot(s.lines[0]) {
			if node, ok := rustCargoTree(s.lines); ok {
				return node, nil
			}
		}
		return nil, nil
	}
	if node, ok := rustCargoTree(s.lines); ok {
		return node, nil
	}
	return nil, nil
}

// looksLikeCargoRoot reports cargo tree's package line ("name v1.2.3 (path)").
func looksLikeCargoRoot(s string) bool {
	i := strings.Index(s, " v")
	return i > 0 && i+2 < len(s) && s[i+2] >= '0' && s[i+2] <= '9'
}
