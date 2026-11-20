package builtin

import (
	"fmt"
	"strings"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// tf family: tf.plan.
//
// Mined from payload\flow.exe (Terminal 0.9.4) per findings/lens-conditions.md §1.1
// (offsets 149956040 / S:783150): the plan vocabulary is a literal cluster —
// "+ created", "- destroyed", "~ updated", "-/+ replaced", "Plan:",
// "Changes to Outputs:", "No changes", and the phrases "will be
// created/updated in-place/destroyed/must be replaced/read during apply/
// imported/no longer be managed/has changed".                              【观察】
// The program name `terraform` is not a literal in the lens region, so
// matching it (and the alias `tf` → terraform, which lens.go already expands)
// is 【推断】 per docs/lens-parity.md rule 2.
// View components follow findings/lens-widgets.md §1.3: card → Card, summary
// counts → Badge (add=success, change=warning, destroy=error), table → Table.

func init() {
	lens.Register(lens.Definition{ID: "tf.plan", New: func() lens.Lens { return &tfPlan{} }})
}

// tfFlagValue reports whether a terraform flag consumes the next token.
func tfFlagValue(flag string) bool {
	switch flag {
	case "-out", "-state", "-state-out", "-backup", "-var", "-var-file",
		"-target", "-chdir", "-parallelism", "-lock-timeout", "-replace",
		"-refresh-only", "-generate-config-out":
		return true
	}
	return false
}

// tfVerb returns terraform's subcommand.                                     【推断】
func tfVerb(cmd model.Cmd) (string, bool) {
	if cmd.Program != "terraform" {
		return "", false
	}
	for i := 0; i < len(cmd.Args); i++ {
		a := cmd.Args[i]
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && tfFlagValue(a) && i+1 < len(cmd.Args) {
				i++
			}
			continue
		}
		return a, true
	}
	return "", true
}

// tfPlan renders `terraform plan` as a card with the add/change/destroy
// summary and the resource and output sections.
type tfPlan struct{}

type tfResource struct {
	action string // create, update, destroy, replace, read, import, forget
	symbol string // +, ~, -, -/+, ≺
	addr   string
}

type tfPlanState struct {
	resources []tfResource
	outputs   []string
	add       int
	change    int
	destroy   int
	sawPlan   bool
	noChanges bool
	inOutputs bool
	saw       bool
}

func (t *tfPlan) ID() string { return "tf.plan" }

func (t *tfPlan) Match(cmd model.Cmd) bool {
	v, ok := tfVerb(cmd)
	return ok && v == "plan"
}

func (t *tfPlan) Open(cmd model.Cmd, cx *lens.Ctx) (any, error) { return &tfPlanState{}, nil }

func (t *tfPlan) Line(state any, l model.Line) error {
	st, ok := state.(*tfPlanState)
	if !ok {
		return errBadState
	}
	st.observe(l.Text)
	return nil
}

func (t *tfPlan) Finish(state any, status int) error { return nil }

func (t *tfPlan) Event(state any, ev lens.Event) error { return nil }

// tfAction maps a plan phrase to its action, symbol and table tone.          【观察】
func tfAction(phrase, addr string) (tfResource, bool) {
	switch {
	case strings.Contains(phrase, "will be created"):
		return tfResource{action: "create", symbol: "+", addr: addr}, true
	case strings.Contains(phrase, "will be destroyed"):
		return tfResource{action: "destroy", symbol: "-", addr: addr}, true
	case strings.Contains(phrase, "will be updated in-place"):
		return tfResource{action: "update", symbol: "~", addr: addr}, true
	case strings.Contains(phrase, "must be replaced"):
		return tfResource{action: "replace", symbol: "-/+", addr: addr}, true
	case strings.Contains(phrase, "will be read during apply"):
		return tfResource{action: "read", symbol: "≺", addr: addr}, true
	case strings.Contains(phrase, "will be imported"):
		return tfResource{action: "import", symbol: "+", addr: addr}, true
	case strings.Contains(phrase, "will no longer be managed"):
		return tfResource{action: "forget", symbol: "-", addr: addr}, true
	case strings.Contains(phrase, "has changed"):
		return tfResource{action: "update", symbol: "~", addr: addr}, true
	}
	return tfResource{}, false
}

func (st *tfPlanState) observe(text string) {
	raw := text
	t := strings.TrimSpace(text)
	if t == "" {
		return
	}
	switch {
	case strings.HasPrefix(t, "Terraform will perform the following actions"),
		strings.HasPrefix(t, "OpenTofu will perform the following actions"):
		st.sawPlan, st.saw = true, true
		return
	case strings.HasPrefix(t, "No changes."), strings.HasPrefix(t, "No changes "):
		st.noChanges, st.saw = true, true
		return
	case strings.HasPrefix(t, "Changes to Outputs:"):
		st.inOutputs, st.sawPlan, st.saw = true, true, true
		return
	case strings.HasPrefix(t, "Plan:"):
		st.parseCounts(t)
		st.sawPlan, st.saw = true, true
		return
	}
	if st.inOutputs {
		if tfOutputLine(t) {
			st.outputs = append(st.outputs, t)
			st.saw = true
			return
		}
		if strings.HasPrefix(t, "─") || strings.HasPrefix(t, "You can apply") {
			st.inOutputs = false
			return
		}
	}
	if rest, ok := strings.CutPrefix(strings.TrimLeft(raw, " "), "# "); ok {
		if i := strings.Index(rest, " will "); i > 0 {
			if r, ok := tfAction(rest[i:], strings.TrimSpace(rest[:i])); ok {
				st.resources = append(st.resources, r)
				st.sawPlan, st.saw = true, true
				return
			}
		}
		if i := strings.Index(rest, " must be replaced"); i > 0 {
			if r, ok := tfAction("must be replaced", strings.TrimSpace(rest[:i])); ok {
				st.resources = append(st.resources, r)
				st.sawPlan, st.saw = true, true
				return
			}
		}
	}
}

// tfOutputLine reports whether a line inside "Changes to Outputs:" is an
// output change ("+ name = …", "~ name = …", "- name = …").               【推断】
func tfOutputLine(t string) bool {
	for _, p := range []string{"+ ", "~ ", "- ", "-/+ "} {
		if strings.HasPrefix(t, p) && strings.Contains(t, " = ") {
			return true
		}
	}
	return false
}

// parseCounts reads "Plan: 2 to add, 1 to change, 0 to destroy.".
func (st *tfPlanState) parseCounts(t string) {
	body := strings.TrimPrefix(t, "Plan:")
	for _, part := range strings.Split(body, ",") {
		n := firstInt(part)
		switch {
		case strings.Contains(part, "to add"), strings.Contains(part, "to import"):
			st.add += n
		case strings.Contains(part, "to change"):
			st.change += n
		case strings.Contains(part, "to destroy"):
			st.destroy += n
		}
	}
}

func (t *tfPlan) View(state any) (lens.Node, error) {
	st, ok := state.(*tfPlanState)
	if !ok {
		return nil, errBadState
	}
	if !st.saw {
		return nil, nil
	}
	head := []lens.Span{{Text: "terraform plan", Style: "strong"}}
	if st.noChanges && len(st.resources) == 0 && len(st.outputs) == 0 &&
		st.add+st.change+st.destroy == 0 {
		return lens.Card{Head: head, Tone: toneSuccess, Children: []lens.Node{
			lens.Badge{Text: "no changes", Tone: toneMuted},
			lens.Text{Spans: []lens.Span{{Text: "No changes. Your infrastructure matches the configuration.", Style: toneMuted}}},
		}}, nil
	}
	var children []lens.Node
	var badges []lens.Node
	if st.add > 0 {
		badges = append(badges, lens.Badge{Text: fmt.Sprintf("%d to add", st.add), Tone: toneSuccess})
	}
	if st.change > 0 {
		badges = append(badges, lens.Badge{Text: fmt.Sprintf("%d to change", st.change), Tone: toneWarning})
	}
	if st.destroy > 0 {
		badges = append(badges, lens.Badge{Text: fmt.Sprintf("%d to destroy", st.destroy), Tone: toneError})
	}
	if len(badges) > 0 {
		children = append(children, lens.Rows(badges...))
	}
	if len(st.resources) > 0 {
		children = append(children, lens.Section{
			Head:     []lens.Span{{Text: "Resources"}},
			Children: []lens.Node{tfResourceTable(st.resources)},
		})
	}
	if len(st.outputs) > 0 {
		children = append(children, lens.Section{
			Head: []lens.Span{{Text: "Outputs"}},
			Children: []lens.Node{lens.Code{
				Text: strings.Join(st.outputs, "\n"), Lang: "hcl", Clamp: 100,
			}},
		})
	}
	if len(children) == 0 {
		return nil, nil
	}
	return lens.Card{Head: head, Tone: toneAccent, Children: children}, nil
}

// tfActionTone maps an action to the tone its table cell and Badge use.     【推断】
func tfActionTone(action string) string {
	switch action {
	case "create", "import":
		return toneSuccess
	case "update", "read":
		return toneWarning
	case "destroy", "forget":
		return toneError
	case "replace":
		return toneAccent
	}
	return toneMuted
}

func tfResourceTable(resources []tfResource) lens.Table {
	cols := []lens.Col{
		{ID: "action", Head: "ACTION", Priority: 1},
		{ID: "resource", Head: "RESOURCE", Grow: 1},
	}
	rows := make([][]lens.Cell, 0, len(resources))
	for _, r := range resources {
		rows = append(rows, []lens.Cell{
			{Spans: []lens.Span{{Text: r.symbol + " " + r.action, Style: tfActionTone(r.action)}}},
			{Spans: []lens.Span{{Text: r.addr, Style: "mono"}}},
		})
	}
	return lens.Table{Cols: cols, Rows: rows}
}
