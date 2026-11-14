package builtin

import (
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID: "files.pwd",
		// pwdLens.Match accepts everything, so the globs are the gate; keep them
		// exact (the old `pwd*` also claimed `pwdfoo`).
		Match: []string{"pwd", "Get-Location", "get-location"},
		New:   func() lens.Lens { return &pwdLens{} },
	})
}

// pwdLens shows the working directory as a path component: the directory with
// a `cwd` badge, and a clickable path. It covers `pwd` in every shell and
// PowerShell's `Get-Location` (whose output is a `Path`/`----`/value block).
type pwdLens struct {
	path string
	got  bool
}

func (l *pwdLens) ID() string { return "files.pwd" }

func (l *pwdLens) Match(model.Cmd) bool { return true }

func (l *pwdLens) Open(model.Cmd, *lens.Ctx) (any, error) { return &pwdLens{}, nil }

func (l *pwdLens) Line(st any, ln model.Line) error {
	s, ok := st.(*pwdLens)
	if !ok {
		return errBadState
	}
	t := strings.TrimSpace(ln.Text)
	switch {
	case t == "", strings.EqualFold(t, "Path"), strings.HasPrefix(t, "----"):
		return nil
	}
	if !pwdLooksLikePath(t) {
		return nil
	}
	s.path = t
	s.got = true
	return nil
}

// pwdLooksLikePath keeps the lens from claiming any stray line as a directory.
func pwdLooksLikePath(t string) bool {
	if strings.ContainsAny(t, `/\`) {
		return true
	}
	return len(t) >= 2 && t[1] == ':' // C: on Windows
}

func (l *pwdLens) Finish(any, int) error { return nil }

func (l *pwdLens) View(st any) (lens.Node, error) {
	s, ok := st.(*pwdLens)
	if !ok {
		return nil, errBadState
	}
	if !s.got {
		return nil, nil // nothing that looked like a path: show raw
	}
	return lens.Cols(
		lens.Rows(
			lens.Spans(lens.Span{Text: "cwd", Style: "muted"}),
			lens.Badge{Text: "working directory", Tone: "info"},
		),
		lens.Spans(lens.Span{Text: s.path, Style: "path strong", Href: fileURL(s.path)}),
	), nil
}

func (l *pwdLens) Event(any, lens.Event) error { return nil }

// fileURL makes a path clickable in the views.
func fileURL(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return "file://" + p
	}
	return "file:///" + p
}
