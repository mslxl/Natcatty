// pkg.go implements Terminal's pkg family: the `pkg.outdated` lens.
//
// The id is an inference. The raw string at offset 149,995,424 of
// payload\flow.exe is glued: "pkgcurrentwantedlatestx0x1x2x3x4x5x6x7"
// [observed]. Rust concatenates neighbouring .rdata literals without
// separators, so the de-glued reading is the family prefix "pkg" followed by
// the column words "current", "wanted", "latest" and the column ids x0..x7
// [observed]. The table in docs/lens-parity.md reads the single lens id of
// this family as "pkg.outdated" [推断]; that is the id registered here.
//
// Nearby observed literals that shape this lens:
//   - 149,995,294 "script available via", 149,995,376 "Wanted"/"Update",
//     149,995,352 "Latest", 149,995,592 "major minor patch deprecated del ins"
//     [observed]: the update kind and the deprecated tone.
//   - 149,995,674 "invalid UNMET extraneous missing" [observed]: the problem
//     words that tint a row and count as unmet.
//   - 149,993,943/149,993,992 "node_modules/PASS Package" [observed]: npm's
//     table header begins with "Package".
//
// Trigger: `npm outdated` (and the sibling package managers), plus
// `npm ls --outdated` [推断: the subcommand/flags are the tool's, the mapping
// to this family comes from the table above]. Output that is not such a table
// leaves View nil and the block shows raw.
package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// pkgManagers are the package managers whose outdated table this lens reads.
// npm is observed in the tool table at 149,988,267; pnpm, yarn and bun are
// [推断] as in js.go.
var pkgManagers = map[string]bool{"npm": true, "pnpm": true, "yarn": true, "bun": true}

func init() {
	// No globs: Match checks the package manager and the exact subcommand, so
	// `npm ls --outdated` is claimed here while plain `npm ls` stays js.pkg.
	lens.Register(lens.Definition{
		ID:  "pkg.outdated",
		New: func() lens.Lens { return &pkgOutdated{} },
	})
}

type pkgOutdated struct{}

type pkgRow struct {
	name                    string
	current, wanted, latest string
	location                string
	bump                    string
	deprecated              bool
	unmet                   bool
}

type pkgProblem struct {
	kind string
	text string
}

type pkgState struct {
	rows     []pkgRow
	problems []pkgProblem
	header   bool
	sawAny   bool
}

func (l *pkgOutdated) ID() string { return "pkg.outdated" }

func (l *pkgOutdated) Match(cmd model.Cmd) bool {
	if !pkgManagers[cmd.Program] {
		return false
	}
	for i, a := range cmd.Args {
		switch a {
		case "outdated":
			return true
		case "ls", "list":
			for _, rest := range cmd.Args[i+1:] {
				if rest == "--outdated" {
					return true
				}
			}
		}
	}
	return false
}

func (l *pkgOutdated) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &pkgState{}, nil }
func (l *pkgOutdated) Finish(state any, status int) error            { return nil }
func (l *pkgOutdated) Event(state any, ev lens.Event) error          { return nil }

func (l *pkgOutdated) Line(state any, ln model.Line) error {
	st, ok := state.(*pkgState)
	if !ok {
		return errBadState
	}
	st.observe(ln.Text)
	return nil
}

func (st *pkgState) observe(text string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	if !st.header && pkgHeader(t) {
		st.header = true
		st.sawAny = true
		return
	}
	if st.header {
		if row, ok := pkgRowOf(t); ok {
			st.rows = append(st.rows, row)
			st.sawAny = true
		}
		return
	}
	// npm ls problem lines: "invalid: \"foo@1.0.0\" ...", "UNMET DEPENDENCY ...",
	// "extraneous: ...", "missing: ...". npm ls draws them under tree glyphs,
	// so strip the box-drawing prefix before matching.
	scan := strings.TrimLeft(t, "│├└─ \t")
	lower := strings.ToLower(scan)
	for _, kind := range []string{"invalid", "unmet", "extraneous", "missing", "deprecated"} {
		if strings.HasPrefix(lower, kind+":") || strings.HasPrefix(lower, strings.ToUpper(kind)+" ") {
			st.problems = append(st.problems, pkgProblem{kind: kind, text: scan})
			st.sawAny = true
			return
		}
	}
	if strings.Contains(lower, "unmet dependency") {
		st.problems = append(st.problems, pkgProblem{kind: "unmet", text: scan})
		st.sawAny = true
	}
}

// pkgHeader reports npm's outdated table header.
func pkgHeader(line string) bool {
	fields := strings.Fields(line)
	has := func(w string) bool {
		for _, f := range fields {
			if strings.EqualFold(f, w) {
				return true
			}
		}
		return false
	}
	return has("Package") && has("Current") && has("Wanted") && has("Latest")
}

// pkgRowOf reads one "Package  Current  Wanted  Latest  Location" row.
func pkgRowOf(line string) (pkgRow, bool) {
	cols := jsSplitColumns(line)
	if len(cols) < 4 {
		// A single-space table (some package managers): fall back to fields.
		cols = strings.Fields(line)
	}
	if len(cols) < 4 {
		return pkgRow{}, false
	}
	r := pkgRow{name: cols[0], current: cols[1], wanted: cols[2], latest: cols[3]}
	if len(cols) >= 5 {
		r.location = cols[4]
	}
	lower := strings.ToLower(line)
	r.deprecated = strings.Contains(lower, "deprecated")
	r.unmet = r.current == "" || strings.EqualFold(r.current, "MISSING") ||
		strings.EqualFold(r.current, "UNMET") || strings.EqualFold(r.latest, "MISSING") ||
		strings.Contains(lower, "invalid") || strings.Contains(lower, "unmet")
	r.bump = pkgBump(r.current, r.latest)
	if r.deprecated {
		r.bump = "deprecated"
	}
	return r, true
}

// pkgBump classifies the update from current to latest: major, minor or patch.
func pkgBump(current, latest string) string {
	cur, ok1 := pkgSemver(current)
	lat, ok2 := pkgSemver(latest)
	if !ok1 || !ok2 {
		return ""
	}
	switch {
	case cur[0] != lat[0]:
		return "major"
	case cur[1] != lat[1]:
		return "minor"
	case cur[2] != lat[2]:
		return "patch"
	}
	return ""
}

// pkgSemver reads the numeric major/minor/patch of a version, ignoring a range
// prefix (^, ~, >=) and a pre-release suffix.
func pkgSemver(v string) ([3]int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimLeft(v, "^~>=<v ")
	if v == "" {
		return [3]int{}, false
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func (l *pkgOutdated) View(state any) (lens.Node, error) {
	st, ok := state.(*pkgState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny {
		return nil, nil
	}
	outdated, unmet, deprecated := 0, 0, 0
	for _, r := range st.rows {
		if r.deprecated {
			deprecated++
		}
		if r.unmet {
			unmet++
		} else if r.bump != "" {
			outdated++
		}
	}
	var children []lens.Node
	if len(st.rows) > 0 {
		badges := []lens.Node{}
		if outdated > 0 {
			badges = append(badges, lens.Badge{Text: strconv.Itoa(outdated) + " outdated", Tone: toneWarning})
		}
		if unmet > 0 {
			badges = append(badges, lens.Badge{Text: strconv.Itoa(unmet) + " unmet", Tone: toneError})
		}
		if deprecated > 0 {
			badges = append(badges, lens.Badge{Text: strconv.Itoa(deprecated) + " deprecated", Tone: toneWarning})
		}
		if len(badges) == 0 {
			badges = append(badges, lens.Badge{Text: "up to date", Tone: toneSuccess})
		}
		children = append(children, lens.Row{Children: badges})

		cols := []lens.Col{
			{ID: "package", Head: "Package", Grow: 1},
			{ID: "current", Head: "Current", Align: lens.AlignEnd},
			{ID: "wanted", Head: "Wanted", Align: lens.AlignEnd},
			{ID: "latest", Head: "Latest", Align: lens.AlignEnd},
			{ID: "bump", Head: "Update"},
		}
		rows := make([][]lens.Cell, 0, len(st.rows))
		for _, r := range st.rows {
			nameStyle := "strong"
			if r.deprecated {
				nameStyle = "strong " + toneWarning
			}
			bumpStyle := toneMuted
			switch r.bump {
			case "major":
				bumpStyle = toneError
			case "minor":
				bumpStyle = toneWarning
			case "patch":
				bumpStyle = toneSuccess
			case "deprecated":
				bumpStyle = toneWarning
			}
			if r.unmet {
				bumpStyle = toneError
			}
			rows = append(rows, []lens.Cell{
				{Spans: []lens.Span{{Text: r.name, Style: nameStyle}}},
				{Spans: []lens.Span{{Text: r.current, Style: "num"}}},
				{Spans: []lens.Span{{Text: r.wanted, Style: "num"}}},
				{Spans: []lens.Span{{Text: r.latest, Style: "num"}}},
				{Spans: []lens.Span{{Text: r.bump, Style: bumpStyle}}},
			})
		}
		children = append(children, lens.Table{Cols: cols, Rows: rows})
		children = append(children, lens.KV{Items: [][2][]lens.Span{{
			[]lens.Span{{Text: "packages"}},
			[]lens.Span{{Text: strconv.Itoa(len(st.rows)), Style: "num"}},
		}}})
	}
	if len(st.problems) > 0 {
		badges := []lens.Node{}
		if unmet > 0 {
			badges = append(badges, lens.Badge{Text: strconv.Itoa(unmet) + " unmet", Tone: toneError})
		}
		children = append(children, lens.Row{Children: badges})
		items := make([]lens.Item, 0, len(st.problems))
		for _, p := range st.problems {
			tone := toneError
			if p.kind == "deprecated" || p.kind == "extraneous" {
				tone = toneWarning
			}
			items = append(items, lens.Item{
				Label:  []lens.Span{{Text: p.kind, Style: "strong " + tone}},
				Detail: []lens.Span{{Text: p.text, Style: toneMuted}},
				Tone:   tone,
			})
		}
		// Package lists are often long: offer the filter box once they are.
		children = append(children, lens.List{Items: items, Filter: len(items) > 8})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Cols(children...), nil
}
