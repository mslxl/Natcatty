package builtin

import (
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func init() {
	lens.Register(lens.Definition{
		ID:  "py.test",
		New: func() lens.Lens { return &pyTest{} },
	})
}

// pyTest turns pytest's output into a test summary and a list of the failures
// with the file and line they came from.
type pyTest struct{}

type pyFailure struct {
	name string
	msg  string
	file string
	line int
}

type pyState struct {
	collected       int
	passed, failed  int
	skipped, errors int
	took            string
	summary         bool
	verbosePassed   int
	verboseFailed   int
	verboseSkipped  int
	verboseErrors   int
	failures        []pyFailure
	sawAny          bool
}

func (p *pyTest) ID() string { return "py.test" }

func (p *pyTest) Match(cmd model.Cmd) bool {
	return cmd.Program == "pytest" || cmd.Program == "py.test"
}

func (p *pyTest) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) {
	return &pyState{}, nil
}

func (p *pyTest) Line(state any, l model.Line) error {
	st, ok := state.(*pyState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (p *pyTest) Finish(state any, status int) error { return nil }

func (p *pyTest) Event(state any, ev lens.Event) error { return nil }

var pyStatuses = map[string]bool{
	"PASSED": true, "FAILED": true, "ERROR": true, "SKIPPED": true, "XFAIL": true, "XPASS": true,
}

func (st *pyState) observe(text string) {
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "collected ") {
		if n := firstInt(strings.TrimPrefix(text, "collected ")); n > 0 {
			st.collected = n
			st.sawAny = true
		}
		return
	}
	// The final summary is authoritative: "===== 2 passed, 1 failed in 0.15s =====".
	if p, f, s, e, took, ok := parsePySummary(text); ok {
		st.passed, st.failed, st.skipped, st.errors = p, f, s, e
		st.took = took
		st.summary = true
		st.sawAny = true
		return
	}
	// "FAILED tests/test_a.py::test_x - AssertionError: ..." from the short summary.
	if strings.HasPrefix(text, "FAILED ") || strings.HasPrefix(text, "ERROR ") {
		rest := strings.TrimSpace(text[strings.IndexByte(text, ' ')+1:])
		name, msg, _ := strings.Cut(rest, " - ")
		if name != "" {
			st.addFailure(name, msg)
		}
		return
	}
	// Verbose results: "tests/test_a.py::test_x PASSED [ 25%]".
	fields := strings.Fields(text)
	if len(fields) > 0 && strings.Contains(fields[0], "::") {
		for _, f := range fields[1:] {
			if !pyStatuses[f] {
				continue
			}
			switch f {
			case "PASSED":
				st.verbosePassed++
			case "FAILED":
				st.verboseFailed++
				st.addFailure(fields[0], "")
			case "ERROR":
				st.verboseErrors++
				st.addFailure(fields[0], "")
			case "SKIPPED", "XFAIL":
				st.verboseSkipped++
			}
			st.sawAny = true
			return
		}
		return
	}
	// A traceback's last line names where it happened: "tests/test_a.py:12: ...".
	if file, line, ok := pyFileLine(text); ok && len(st.failures) > 0 {
		last := &st.failures[len(st.failures)-1]
		if last.line == 0 {
			last.file, last.line = file, line
		}
	}
}

// addFailure records a failure once, merging what later lines add to it: the
// verbose result, the traceback location and the short-summary message.
func (st *pyState) addFailure(name, msg string) {
	if name == "" {
		return
	}
	for i := range st.failures {
		if st.failures[i].name == name {
			if st.failures[i].msg == "" {
				st.failures[i].msg = msg
			}
			st.sawAny = true
			return
		}
	}
	st.failures = append(st.failures, pyFailure{name: name, msg: msg, file: pyFileOf(name)})
	st.sawAny = true
}

// parsePySummary reads pytest's closing line, with or without the "=" rule
// around it: "2 passed, 1 failed, 1 skipped in 0.15s".
func parsePySummary(text string) (passed, failed, skipped, errors int, took string, ok bool) {
	if !strings.Contains(text, " in ") {
		return 0, 0, 0, 0, "", false
	}
	body := strings.Trim(text, "= ")
	i := strings.LastIndex(body, " in ")
	if i < 0 {
		return 0, 0, 0, 0, "", false
	}
	counts, tail := body[:i], body[i+4:]
	if tail == "" {
		return 0, 0, 0, 0, "", false
	}
	took = strings.Fields(tail)[0]
	seen := false
	for _, part := range strings.Split(counts, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) < 2 {
			continue
		}
		n := firstInt(fields[0])
		switch fields[1] {
		case "passed":
			passed, seen = n, true
		case "failed":
			failed, seen = n, true
		case "skipped":
			skipped, seen = n, true
		case "error", "errors":
			errors, seen = n, true
		}
	}
	if !seen {
		return 0, 0, 0, 0, "", false
	}
	return passed, failed, skipped, errors, took, true
}

// pyFileOf takes the file part of "tests/test_a.py::test_x".
func pyFileOf(name string) string {
	if i := strings.Index(name, "::"); i > 0 {
		return name[:i]
	}
	return ""
}

// pyFileLine reads "tests/test_a.py:12: message" (a traceback location).
func pyFileLine(text string) (string, int, bool) {
	i := strings.Index(text, ".py:")
	if i < 0 {
		return "", 0, false
	}
	file := text[:i+3]
	rest := text[i+4:]
	n := leadingInt(rest)
	if n < 0 {
		return "", 0, false
	}
	if len(rest) > 0 && rest[0] == ':' {
		n = leadingInt(rest[1:])
	}
	if n <= 0 {
		return "", 0, false
	}
	return file, n, true
}

func (p *pyTest) View(state any) (lens.Node, error) {
	st, ok := state.(*pyState)
	if !ok {
		return nil, errBadState
	}
	if !st.sawAny {
		return nil, nil
	}
	passed, failed, skipped, errors := st.passed, st.failed, st.skipped, st.errors
	if !st.summary {
		passed, failed, skipped, errors = st.verbosePassed, st.verboseFailed, st.verboseSkipped, st.verboseErrors
	}
	if passed == 0 && failed == 0 && skipped == 0 && errors == 0 && st.collected == 0 && len(st.failures) == 0 {
		return nil, nil
	}
	summary := lens.TestSummary{Passed: passed, Failed: failed + errors, Skipped: skipped, Took: st.took}
	children := []lens.Node{summary}
	if len(st.failures) > 0 {
		items := make([]lens.Item, 0, len(st.failures))
		for _, f := range st.failures {
			label := f.name
			if label == "" {
				label = "failure"
			}
			detail := make([]lens.Span, 0, 2)
			if f.file != "" {
				loc := f.file
				if f.line > 0 {
					loc += ":" + strconv.Itoa(f.line)
				}
				detail = append(detail, lens.Span{Text: loc, Style: "path"})
			}
			if f.msg != "" {
				detail = append(detail, lens.Span{Text: f.msg, Style: toneMuted})
			}
			items = append(items, lens.Item{
				Label:  []lens.Span{{Text: label, Style: "strong " + toneError}},
				Detail: detail,
				Tone:   toneError,
			})
		}
		children = append(children, lens.List{Items: items})
	} else {
		children = append(children, lens.Text{Spans: []lens.Span{{Text: "all tests passed", Style: toneSuccess}}})
	}
	return lens.Cols(children...), nil
}
