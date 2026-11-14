package builtin

import (
	"sort"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID:  "git.status",
		New: func() lens.Lens { return &gitStatus{} },
	})
}

// gitStatus turns `git status` (short, porcelain or long) into a card with a
// section per side of the index and a path tree with a status badge per file.
type gitStatus struct{}

type gitChange struct {
	path string
	code string
}

type gitStatusState struct {
	branch    string
	upstream  string
	ahead     int
	behind    int
	clean     bool
	fatal     bool
	sawAny    bool
	section   string // staged, unstaged, untracked, conflicts
	staged    []gitChange
	unstaged  []gitChange
	untracked []gitChange
	conflicts []gitChange
}

func (g *gitStatus) ID() string { return "git.status" }

// Match is the fallback for a definition without globs.
func (g *gitStatus) Match(cmd model.Cmd) bool {
	if cmd.Program != "git" {
		return false
	}
	for _, a := range cmd.Args {
		if a == "status" {
			return true
		}
	}
	return false
}

func (g *gitStatus) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &gitStatusState{}, nil
}

func (g *gitStatus) Line(state any, l model.Line) error {
	st, ok := state.(*gitStatusState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (g *gitStatus) Finish(state any, status int) error { return nil }

func (g *gitStatus) Event(state any, ev lens.Event) error { return nil }

// gitShortCodes are the two status letters of the short format: the index
// side then the work tree side.
const gitShortCodes = " MADRCUX?!"

func (st *gitStatusState) observe(text string) {
	if text == "" {
		return
	}
	// ConPTY drops some line breaks when a program writes many short lines
	// (Windows consoles re-serialise the screen, and a delayed newline can be
	// lost), so `git status` can arrive as
	// "…to discard changes in working directory)modified:   a.txtUntracked files:".
	// Split on the markers the long format uses so the card still finds its
	// sections and entries.
	for _, part := range gitStatusSplit(text) {
		if st.v2Line(part) {
			st.sawAny = true
			continue
		}
		if st.shortLine(part) {
			st.sawAny = true
			continue
		}
		if st.longLine(part) {
			st.sawAny = true
		}
	}
}

// gitStatusSplits are the markers of `git status`'s long format. They only
// split when they appear after the start of a line, which is exactly the shape
// ConPTY produces when it loses a newline.
var gitStatusSplits = []string{
	"Changes to be committed:", "Changes not staged for commit:",
	"Untracked files:", "Unmerged paths:", "nothing to commit",
	"no changes added", "modified:   ", "new file:   ", "deleted:    ",
	"renamed:    ", "copied:     ", "typechange: ",
}

// gitStatusSplit splits a line at those markers (keeping the marker). A line
// without any marker is returned untouched, leading spaces and all, because the
// short format's " M path" relies on them.
func gitStatusSplit(text string) []string {
	parts := []string{text}
	changed := false
	for _, marker := range gitStatusSplits {
		var next []string
		for _, part := range parts {
			if len(part) <= 1 {
				next = append(next, part)
				continue
			}
			for {
				i := strings.Index(part[1:], marker)
				if i <= 0 {
					break
				}
				i++
				next = append(next, strings.TrimRight(part[:i], " "))
				part = strings.TrimLeft(part[i:], " ")
				changed = true
			}
			next = append(next, part)
		}
		parts = next
	}
	if !changed {
		return []string{text}
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

// v2Line parses `--porcelain=v2` records, which start with the record type:
//
//	1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
//	2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <score> <path>
//	u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
//	? <path>            ! <path>
//
// The XY letters are the short format's, except that "." means unmodified.
func (st *gitStatusState) v2Line(text string) bool {
	if len(text) > 1 && text[1] == ' ' {
		switch text[0] {
		case '1', '2', 'u':
			f := strings.Fields(text)
			idx := 8 // the path follows the two object hashes
			switch text[0] {
			case '2':
				idx = 9 // plus the rename/copy score
			case 'u':
				idx = 10 // three stages
			}
			if len(f) <= idx || len(f[1]) != 2 {
				return false
			}
			st.applyXY(f[1], cleanGitPath(strings.Join(f[idx:], " ")))
			return true
		case '?':
			path := cleanGitPath(strings.TrimSpace(text[2:]))
			if path == "" {
				return false
			}
			st.untracked = append(st.untracked, gitChange{path: path, code: "?"})
			return true
		case '!':
			return true // ignored files are not worth a line
		}
	}
	return false
}

// applyXY files one change by its two status letters (index then work tree).
func (st *gitStatusState) applyXY(xy, path string) {
	if len(xy) != 2 || path == "" {
		return
	}
	x, y := xy[0], xy[1]
	if isConflictXY(x, y) {
		st.conflicts = append(st.conflicts, gitChange{path: path, code: "U"})
		return
	}
	if x != '.' && x != ' ' {
		st.staged = append(st.staged, gitChange{path: path, code: string(x)})
	}
	if y != '.' && y != ' ' {
		st.unstaged = append(st.unstaged, gitChange{path: path, code: string(y)})
	}
}

// shortLine parses `XY path` lines: ` M file`, `A  file`, `?? file`.
func (st *gitStatusState) shortLine(text string) bool {
	if len(text) < 4 || text[2] != ' ' {
		return false
	}
	x, y := text[0], text[1]
	if !strings.ContainsRune(gitShortCodes, rune(x)) || !strings.ContainsRune(gitShortCodes, rune(y)) {
		return false
	}
	if x == ' ' && y == ' ' {
		return false
	}
	path := strings.TrimSpace(text[3:])
	if path == "" {
		return false
	}
	path = cleanGitPath(path)
	switch {
	case x == '!' && y == '!':
		return true // ignored files are not worth a line
	case x == '?' && y == '?':
		st.untracked = append(st.untracked, gitChange{path: path, code: "?"})
		return true
	case isConflictXY(x, y):
		st.conflicts = append(st.conflicts, gitChange{path: path, code: "U"})
		return true
	}
	if x != ' ' {
		st.staged = append(st.staged, gitChange{path: path, code: string(x)})
	}
	if y != ' ' {
		st.unstaged = append(st.unstaged, gitChange{path: path, code: string(y)})
	}
	return true
}

func isConflictXY(x, y byte) bool {
	if x == 'U' || y == 'U' {
		return true
	}
	return (x == 'A' && y == 'A') || (x == 'D' && y == 'D')
}

// longLine parses the human-readable format: headers, branch lines and the
// status-word entries under them.
func (st *gitStatusState) longLine(text string) bool {
	switch {
	case strings.HasPrefix(text, "On branch "):
		st.branch = strings.TrimSpace(strings.TrimPrefix(text, "On branch "))
		return true
	case strings.HasPrefix(text, "HEAD detached"):
		st.branch = text
		return true
	case strings.HasPrefix(text, "Your branch is up to date with '"):
		st.upstream = quotedAfter(text, "with '")
		return true
	case strings.HasPrefix(text, "Your branch is ahead of '"):
		st.upstream = quotedAfter(text, "of '")
		st.ahead = firstInt(text[strings.LastIndex(text, "by ")+3:])
		return true
	case strings.HasPrefix(text, "Your branch is behind '"):
		st.upstream = quotedAfter(text, "behind '")
		st.behind = firstInt(text[strings.LastIndex(text, "by ")+3:])
		return true
	case strings.HasPrefix(text, "Your branch and '"):
		st.upstream = quotedAfter(text, "and '")
		if nums := parseFloats(text); len(nums) >= 2 {
			st.ahead, st.behind = int(nums[0]), int(nums[1])
		}
		return true
	case text == "Changes to be committed:":
		st.section = "staged"
		return true
	case text == "Changes not staged for commit:":
		st.section = "unstaged"
		return true
	case text == "Untracked files:":
		st.section = "untracked"
		return true
	case text == "Unmerged paths:":
		st.section = "conflicts"
		return true
	case strings.HasPrefix(text, "nothing to commit"), strings.Contains(text, "working tree clean"):
		st.clean = true
		return true
	case strings.HasPrefix(text, "no changes added"), strings.HasPrefix(text, "(use "),
		strings.HasPrefix(text, "  (use "), strings.HasPrefix(text, "  ("):
		return true // git's advice text, not a change
	case strings.HasPrefix(text, "fatal:"), strings.HasPrefix(text, "error:"):
		st.fatal = true
		return true
	}
	if st.section != "" {
		return st.entry(text)
	}
	return false
}

// entry parses one line under a long-format section header.
func (st *gitStatusState) entry(text string) bool {
	body := text
	if strings.HasPrefix(body, "\t") {
		body = strings.TrimLeft(body, "\t")
	} else {
		b := strings.TrimLeft(body, " ")
		if b == "" || strings.HasPrefix(b, "(") {
			return false
		}
		if _, _, ok := gitStatusWord(b); !ok && st.section != "untracked" {
			return false
		}
		body = b
	}
	if code, path, ok := gitStatusWord(body); ok {
		ch := gitChange{path: cleanGitPath(path), code: code}
		switch st.section {
		case "staged":
			st.staged = append(st.staged, ch)
		case "conflicts":
			st.conflicts = append(st.conflicts, ch)
		case "untracked":
			st.untracked = append(st.untracked, ch)
		default:
			st.unstaged = append(st.unstaged, ch)
		}
		return true
	}
	if st.section == "untracked" {
		p := cleanGitPath(strings.TrimSpace(body))
		if p == "" {
			return false
		}
		st.untracked = append(st.untracked, gitChange{path: p, code: "?"})
		return true
	}
	return false
}

// gitStatusWords are the long-format status words, longest first so that
// "both modified:" wins over "modified:".
var gitStatusWords = []struct{ word, code string }{
	{"both modified:", "U"},
	{"both added:", "U"},
	{"both deleted:", "U"},
	{"added by us:", "U"},
	{"added by them:", "U"},
	{"deleted by us:", "U"},
	{"deleted by them:", "U"},
	{"new file:", "A"},
	{"modified:", "M"},
	{"deleted:", "D"},
	{"renamed:", "R"},
	{"copied:", "C"},
	{"typechange:", "T"},
}

func gitStatusWord(s string) (code, path string, ok bool) {
	for _, w := range gitStatusWords {
		if strings.HasPrefix(s, w.word) {
			return w.code, strings.TrimSpace(s[len(w.word):]), true
		}
	}
	return "", "", false
}

// cleanGitPath turns `"old -> new"` into the new path and unquotes a quoted
// porcelain path.
func cleanGitPath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.LastIndex(p, " -> "); i >= 0 {
		p = p[i+4:]
	}
	p = strings.Trim(p, "\"")
	return p
}

// quotedAfter returns the text between the first quote after marker and the
// closing quote, as "with 'origin/main'." reports the upstream.
func quotedAfter(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	if j := strings.IndexByte(rest, '\''); j >= 0 {
		return rest[:j]
	}
	return strings.TrimSpace(rest)
}

func (g *gitStatus) View(state any) (lens.Node, error) {
	st, ok := state.(*gitStatusState)
	if !ok {
		return nil, errBadState
	}
	if st.fatal || !st.sawAny {
		return nil, nil
	}
	changes := len(st.staged) + len(st.unstaged) + len(st.untracked) + len(st.conflicts)
	if changes == 0 && st.branch == "" && st.upstream == "" && !st.clean {
		return nil, nil // nothing that looks like git status
	}

	head := []lens.Span{{Text: "git status", Style: "strong"}}
	if st.branch != "" {
		head = append(head, lens.Span{Text: " " + st.branch, Style: "muted"})
	}
	if st.upstream != "" {
		head = append(head, lens.Span{Text: " → " + st.upstream, Style: "muted"})
	}
	tone := toneNeutral
	switch {
	case len(st.conflicts) > 0:
		tone = toneError
	case len(st.unstaged)+len(st.untracked) > 0:
		tone = toneWarning
	case len(st.staged) > 0, st.clean:
		tone = toneSuccess
	}

	var children []lens.Node
	if st.upstream != "" || st.ahead > 0 || st.behind > 0 {
		var spans []lens.Span
		if st.ahead > 0 {
			spans = append(spans, lens.Span{Text: "ahead " + strconv.Itoa(st.ahead), Style: toneSuccess})
		}
		if st.behind > 0 {
			if len(spans) > 0 {
				spans = append(spans, lens.Span{Text: " · ", Style: toneMuted})
			}
			spans = append(spans, lens.Span{Text: "behind " + strconv.Itoa(st.behind), Style: toneWarning})
		}
		if len(spans) == 0 && st.upstream != "" {
			spans = append(spans, lens.Span{Text: "up to date with " + st.upstream, Style: toneMuted})
		}
		children = append(children, lens.Text{Spans: spans})
	}
	sections := 0
	if len(st.staged) > 0 {
		children = append(children, statusSection("Staged", st.staged))
		sections++
	}
	if len(st.unstaged)+len(st.untracked) > 0 {
		entries := append(append([]gitChange{}, st.unstaged...), st.untracked...)
		children = append(children, statusSection("Unstaged", entries))
		sections++
	}
	if len(st.conflicts) > 0 {
		children = append(children, statusSection("Conflicts", st.conflicts))
		sections++
	}
	if sections == 0 {
		if !st.clean {
			// A bare branch line with no change information is not enough
			// to claim the command: let the block show raw output.
			return nil, nil
		}
		children = append(children, lens.Text{Spans: []lens.Span{{Text: "nothing to commit, working tree clean", Style: toneSuccess}}})
	}
	return lens.Card{Head: head, Tone: tone, Children: children}, nil
}

// statusSection is one side of the index: badges counting the statuses, then
// the paths grouped by directory.
func statusSection(head string, entries []gitChange) lens.Node {
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.code]++
	}
	codes := make([]string, 0, len(counts))
	for c := range counts {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	badges := make([]lens.Node, 0, len(codes))
	for _, c := range codes {
		badges = append(badges, lens.Badge{Text: c + " " + strconv.Itoa(counts[c]), Tone: toneForCode(c)})
	}
	headSpans := []lens.Span{
		{Text: head, Style: "strong"},
		{Text: " " + strconv.Itoa(len(entries)), Style: toneMuted},
	}
	tree := make([]treeEntry, 0, len(entries))
	for _, e := range entries {
		tree = append(tree, treeEntry{path: e.path, code: e.code})
	}
	return lens.Section{Head: headSpans, Children: []lens.Node{
		lens.Row{Children: badges},
		buildTree(tree),
	}}
}
