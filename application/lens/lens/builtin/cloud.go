package builtin

import (
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// cloud.json is Terminal's "Cloud CLIs" lens in the Containers and cloud family:
// `aws`, `gcloud --format json` and `az` print JSON that becomes a tree, exactly
// like view.json's jq branch. The trigger list is the 0.9.14 catalog's
// (`cloud.json` / "Cloud CLIs" / `aws, gcloud --format json, az`).
func init() {
	lens.Register(lens.Definition{
		ID:  "cloud.json",
		New: func() lens.Lens { return &cloudJSON{} },
	})
}

type cloudJSON struct{ lines []string }

func (l *cloudJSON) ID() string { return "cloud.json" }

func (l *cloudJSON) Match(cmd model.Cmd) bool {
	switch program(cmd) {
	case "aws", "az":
		return true
	case "gcloud":
		// `gcloud … --format json` (or --format=json) is the JSON shape.
		for i, a := range cmd.Args {
			if a == "--format" && i+1 < len(cmd.Args) && strings.EqualFold(cmd.Args[i+1], "json") {
				return true
			}
			if strings.HasPrefix(a, "--format=") && strings.EqualFold(strings.TrimPrefix(a, "--format="), "json") {
				return true
			}
		}
	}
	return false
}

func (l *cloudJSON) Open(_ model.Cmd, _ *lens.Ctx) (any, error) { return &cloudJSON{}, nil }

func (l *cloudJSON) Line(st any, ln model.Line) error {
	s, ok := st.(*cloudJSON)
	if !ok {
		return errBadState
	}
	if len(s.lines) < maxFileLines {
		s.lines = append(s.lines, ln.Text)
	}
	return nil
}

func (l *cloudJSON) Finish(any, int) error { return nil }

func (l *cloudJSON) Event(any, lens.Event) error { return nil }

func (l *cloudJSON) View(st any) (lens.Node, error) {
	s, ok := st.(*cloudJSON)
	if !ok {
		return nil, errBadState
	}
	if len(s.lines) == 0 {
		return nil, nil
	}
	text := strings.TrimSpace(strings.Join(s.lines, "\n"))
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		if n, ok := lens.JSONTree(text); ok {
			return n, nil
		}
	}
	return nil, nil // text output (aws s3 ls, gcloud list): keep raw
}
