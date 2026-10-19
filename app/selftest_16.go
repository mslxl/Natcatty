//go:build selftest

package app

// Ticket 16 self-test.
//
// Press F11, then 1, then 6. The check runs on the UI thread one step per
// frame and writes %TEMP%\natcatty-selftest-16.json.
//
// Three real local shell Sessions are opened through the production Session
// service and local PTY adapter. A typed marker is sent from the first Session
// while broadcast is active: the first receives it directly and the second
// through the broadcast fan-out, while the third must not receive it. Each
// shell writes a marker file named with its own PID, so the check proves that
// exactly the selected Sessions ran the input without competing with the
// terminal view for its output stream. Later steps stop broadcast and prove
// typing reaches only the focused Session, then exercise compose previews and
// sends to the focused Session and to the broadcast set. A layout step renders
// the production controls at 1100x720 and 720x480. The final step leaves the
// broadcast panel, per-Session statuses, and compose bar open for visual
// inspection.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/shell"
	"github.com/mslxl/natcatty/ui/shell/state"
)

type selftest16 struct {
	state *state.State

	root   string
	marker string
	tabs   []model.TabID
	ids    []model.SessionID

	typedSent bool
	typedAt   time.Time

	stopSent bool
	stopAt   time.Time

	focusedSent    bool
	focusedAt      time.Time
	focusedPreview string

	broadcastSent    bool
	broadcastAt      time.Time
	broadcastPreview string
}

func (t *selftest16) check() selftest.Check {
	return selftest.Check{
		Ticket: 16,
		Name:   "Broadcast input and compose bar",
		Steps: []selftest.Step{
			{Name: "broadcast-select-and-start", Run: t.setupStep},
			{Name: "broadcast-typed-delivery", Run: t.typedStep},
			{Name: "broadcast-stop-focused-only", Run: t.stopStep},
			{Name: "compose-focused-send", Run: t.composeFocusedStep},
			{Name: "compose-broadcast-send", Run: t.composeBroadcastStep},
			{Name: "broadcast-compose-layout", Run: t.layoutStep},
			{Name: "broadcast-compose-visible", Run: t.demoStep},
		},
	}
}

func (t *selftest16) resetRunState() {
	if t.state == nil {
		return
	}
	t.state.StopBroadcast()
	t.state.CloseCompose()
	t.state.CloseBroadcastPanel()
	for _, info := range t.state.BroadcastSessions() {
		t.state.SetBroadcastTarget(info.ID, false)
	}
	for _, id := range t.ids {
		t.state.SetBroadcastTarget(id, false)
	}
	for _, id := range t.tabs {
		if index := tabIndex(t.state, id); index >= 0 {
			t.state.CloseTab(index)
		}
	}
	t.state.Flush()

	t.tabs = nil
	t.ids = nil
	t.typedSent = false
	t.typedAt = time.Time{}
	t.stopSent = false
	t.stopAt = time.Time{}
	t.focusedSent = false
	t.focusedAt = time.Time{}
	t.focusedPreview = ""
	t.broadcastSent = false
	t.broadcastAt = time.Time{}
	t.broadcastPreview = ""
	t.marker = fmt.Sprintf("NATCATTY16_%d", time.Now().UnixNano())
	t.root = filepath.Join(os.TempDir(), "natcatty-selftest-16")
	_ = os.RemoveAll(t.root)
	_ = os.MkdirAll(t.root, 0o755)
}

func (t *selftest16) setupStep(*ui.Context) selftest.Outcome {
	if t.state == nil {
		return selftest.Failed(errors.New("the shell is not wired"))
	}
	// Reset on every first step, not only after a successful demo. A run that
	// failed or was interrupted must not leave its Tabs, targets, marker files,
	// or open controls behind for the next F11,1,6.
	t.resetRunState()

	for len(t.tabs) < 3 {
		if err := t.state.NewTab(); err != nil {
			return selftest.Failed(fmt.Errorf("open real local shell %d: %w", len(t.tabs)+1, err))
		}
		index := t.state.ActiveTab()
		id, ok := t.state.TabID(index)
		if !ok {
			return selftest.Failed(errors.New("the new Tab has no identity"))
		}
		focused, ok := t.state.FocusedSessionID()
		if !ok {
			return selftest.Failed(errors.New("the new Tab has no focused Session"))
		}
		t.tabs = append(t.tabs, id)
		t.ids = append(t.ids, focused)
		t.state.Flush()
	}
	if len(t.ids) != 3 {
		return selftest.Failed(fmt.Errorf("Sessions = %d, want 3", len(t.ids)))
	}

	focused, ok := t.state.FocusedSessionID()
	if !ok || focused != t.ids[2] {
		return selftest.Failed(fmt.Errorf("focused Session = %q, want the third Tab %q", focused, t.ids[2]))
	}

	t.state.OpenBroadcastPanel()
	// Select exactly the two Sessions this check opened. A restored Workspace
	// or the user's other Tabs must not join the check's target set.
	for _, info := range t.state.BroadcastSessions() {
		t.state.SetBroadcastTarget(info.ID, false)
	}
	for _, id := range t.ids[:2] {
		t.state.SetBroadcastTarget(id, true)
	}
	if got := t.state.BroadcastTargetCount(); got != 2 {
		return selftest.Failed(fmt.Errorf("broadcast targets = %d, want the first two Sessions", got))
	}
	if !t.state.StartBroadcast() {
		return selftest.Failed(errors.New("broadcast did not start with two live targets"))
	}
	if !t.state.Broadcasting() {
		return selftest.Failed(errors.New("broadcast is not active after Start"))
	}
	targets, _ := t.state.ComposeTargets()
	if len(targets) != 2 {
		return selftest.Failed(fmt.Errorf("compose broadcast targets = %d, want 2", len(targets)))
	}
	return selftest.Passed(
		"three real local shell Sessions opened; the first two are selected broadcast targets and the third remains focused",
		"localpty.Factory")
}

func (t *selftest16) typedStep(*ui.Context) selftest.Outcome {
	if len(t.ids) < 3 {
		return selftest.Failed(errors.New("the broadcast Sessions are not prepared"))
	}
	if !t.typedSent {
		session, ok := t.state.SessionByID(t.ids[0])
		if !ok {
			return selftest.Failed(errors.New("the source Session is no longer open"))
		}
		command := selftest16WriteCommand(t.root, "typed", "", t.marker)
		if _, err := session.TerminalConn().Write([]byte(command + "\r")); err != nil {
			return selftest.Failed(fmt.Errorf("type into the source Session: %w", err))
		}
		t.typedSent = true
		t.typedAt = time.Now().Add(selftestTimeout)
	}

	result, ok := t.state.BroadcastResult()
	if !ok {
		return pending(t.typedAt, "the typed broadcast delivery result")
	}
	if len(result.Deliveries) != 1 || result.Deliveries[0].SessionID != t.ids[1] ||
		result.Deliveries[0].Status != appsession.DeliveryDelivered {
		return selftest.Failed(fmt.Errorf("typed broadcast deliveries = %+v, want the second Session delivered", result.Deliveries))
	}
	if result.DeliveredCount() != 1 {
		return selftest.Failed(fmt.Errorf("typed broadcast delivered = %d, want 1", result.DeliveredCount()))
	}
	infos := t.state.BroadcastSessions()
	second := selftest16Info(infos, t.ids[1])
	if second == nil || !second.HasStatus || second.Status != appsession.DeliveryDelivered {
		return selftest.Failed(fmt.Errorf("the second Session has no delivered status: %+v", second))
	}

	count, err := t.markerCount("typed", t.marker)
	if err != nil {
		return selftest.Failed(err)
	}
	if count > 2 {
		return selftest.Failed(fmt.Errorf("%d Sessions ran the typed marker, want exactly 2", count))
	}
	if count < 2 {
		return pending(t.typedAt, "both selected local shells to write the typed marker")
	}
	return selftest.Passed(
		"typed input reached the source and the selected peer, with the peer reported delivered; the third Session did not receive it",
		"localpty.Factory")
}

func (t *selftest16) stopStep(*ui.Context) selftest.Outcome {
	if len(t.ids) < 3 {
		return selftest.Failed(errors.New("the broadcast Sessions are not prepared"))
	}
	if !t.stopSent {
		if t.state.Broadcasting() {
			t.state.StopBroadcast()
		}
		if t.state.Broadcasting() {
			return selftest.Failed(errors.New("broadcast stayed active after Stop"))
		}
		focused, ok := t.state.FocusedSessionID()
		if !ok || focused != t.ids[2] {
			return selftest.Failed(fmt.Errorf("focused Session = %q, want %q", focused, t.ids[2]))
		}
		session, ok := t.state.SessionByID(focused)
		if !ok {
			return selftest.Failed(errors.New("the focused Session is no longer open"))
		}
		command := selftest16WriteCommand(t.root, "stopped", "", t.marker)
		if _, err := session.TerminalConn().Write([]byte(command + "\r")); err != nil {
			return selftest.Failed(fmt.Errorf("type after stopping broadcast: %w", err))
		}
		t.stopSent = true
		t.stopAt = time.Now().Add(selftestTimeout)
	}
	if t.state.Broadcasting() {
		return selftest.Failed(errors.New("broadcast became active again after Stop"))
	}

	count, err := t.markerCount("stopped", t.marker)
	if err != nil {
		return selftest.Failed(err)
	}
	if count > 1 {
		return selftest.Failed(fmt.Errorf("%d Sessions ran the post-stop marker, want only the focused one", count))
	}
	if count < 1 {
		return pending(t.stopAt, "the focused local shell to write the post-stop marker")
	}
	return selftest.Passed(
		"broadcast stopped immediately; subsequent typing reached only the focused Session",
		"localpty.Factory")
}

func (t *selftest16) composeFocusedStep(*ui.Context) selftest.Outcome {
	focused, ok := t.state.FocusedSessionID()
	if !ok {
		return selftest.Failed(errors.New("there is no focused Session"))
	}
	if !t.focusedSent {
		if !t.state.ComposeOpen() {
			t.state.OpenCompose()
			*t.state.ComposeTrim() = true
			*t.state.ComposeDecodeEscapes() = true
			t.state.SetComposeLineMode(appsession.ComposeLineByLine)
		}
		one := selftest16WriteCommand(t.root, "compose-focused", "one", t.marker)
		two := selftest16WriteCommand(t.root, "compose-focused", "two", t.marker)
		t.focusedPreview = one + "\n" + two
		t.setDraft(one + `\n` + two)

		preview := t.state.ComposePreview()
		if preview.Text != t.focusedPreview {
			return selftest.Failed(fmt.Errorf("compose preview = %q, want the decoded and trimmed draft %q", preview.Text, t.focusedPreview))
		}
		if len(preview.Chunks) != 2 || preview.LineMode != appsession.ComposeLineByLine || !preview.AutoRun {
			return selftest.Failed(fmt.Errorf("compose preview = %+v, want two auto-run line chunks", preview))
		}
		if !strings.Contains(preview.PreviewText(), `\n`) {
			return selftest.Failed(fmt.Errorf("compose preview does not show the decoded newline: %q", preview.PreviewText()))
		}
		targets, broadcast := t.state.ComposeTargets()
		if broadcast || len(targets) != 1 || targets[0] != focused {
			return selftest.Failed(fmt.Errorf("compose targets = %v broadcast=%v, want only %q", targets, broadcast, focused))
		}
		if !t.state.SendCompose() {
			return selftest.Failed(errors.New("compose did not send the focused draft"))
		}
		t.focusedSent = true
		t.focusedAt = time.Now().Add(selftestTimeout)
	}

	finish, err := t.finishCompose(t.focusedAt)
	if err != nil {
		return selftest.Failed(err)
	}
	if finish.waiting {
		return pending(t.focusedAt, "the focused compose line-by-line send")
	}
	if *t.state.ComposeDraft() != "" {
		return selftest.Failed(errors.New("a delivered compose draft was not cleared"))
	}
	for _, prefix := range []string{"compose-focused-one", "compose-focused-two"} {
		count, err := t.markerCount(prefix, t.marker)
		if err != nil {
			return selftest.Failed(err)
		}
		if count > 1 {
			return selftest.Failed(fmt.Errorf("%s ran in %d Sessions, want only the focused Session", prefix, count))
		}
		if count < 1 {
			return pending(t.focusedAt, "the focused local shell to run "+prefix)
		}
	}
	return selftest.Passed(
		"the trimmed, escape-decoded multi-line draft previewed as two lines and both lines reached the focused Session; the draft cleared after delivery",
		"shell State")
}

func (t *selftest16) composeBroadcastStep(*ui.Context) selftest.Outcome {
	if len(t.ids) < 3 {
		return selftest.Failed(errors.New("the broadcast Sessions are not prepared"))
	}
	if !t.broadcastSent {
		if !t.state.Broadcasting() {
			if !t.state.StartBroadcast() {
				return selftest.Failed(errors.New("broadcast did not restart for the compose check"))
			}
		}
		targets, broadcast := t.state.ComposeTargets()
		if !broadcast || len(targets) != 2 {
			return selftest.Failed(fmt.Errorf("compose targets = %v broadcast=%v, want two broadcast Sessions", targets, broadcast))
		}
		one := selftest16WriteCommand(t.root, "compose-broadcast", "one", t.marker)
		two := selftest16WriteCommand(t.root, "compose-broadcast", "two", t.marker)
		t.broadcastPreview = one + "\n" + two
		t.setDraft(one + `\n` + two)
		if t.state.ComposePreview().Text != t.broadcastPreview {
			return selftest.Failed(fmt.Errorf("broadcast compose preview = %q, want %q",
				t.state.ComposePreview().Text, t.broadcastPreview))
		}
		if !t.state.SendCompose() {
			return selftest.Failed(errors.New("compose did not send the broadcast draft"))
		}
		t.broadcastSent = true
		t.broadcastAt = time.Now().Add(selftestTimeout)
	}
	if !t.state.Broadcasting() {
		return selftest.Failed(errors.New("broadcast stopped during the compose send"))
	}
	finish, err := t.finishCompose(t.broadcastAt)
	if err != nil {
		return selftest.Failed(err)
	}
	if finish.waiting {
		return pending(t.broadcastAt, "the broadcast compose line-by-line send")
	}
	if finish.delivered != 2 {
		return selftest.Failed(fmt.Errorf("broadcast compose delivered = %d, want both targets", finish.delivered))
	}
	if *t.state.ComposeDraft() != "" {
		return selftest.Failed(errors.New("a delivered broadcast compose draft was not cleared"))
	}

	result, ok := t.state.BroadcastResult()
	if !ok || result.DeliveredCount() != 2 || len(result.Deliveries) != 2 {
		return selftest.Failed(fmt.Errorf("broadcast panel result = %+v, want two deliveries", result))
	}
	infos := t.state.BroadcastSessions()
	for _, id := range t.ids[:2] {
		info := selftest16Info(infos, id)
		if info == nil || !info.HasStatus || info.Status != appsession.DeliveryDelivered {
			return selftest.Failed(fmt.Errorf("the broadcast panel status for %q is %+v", id, info))
		}
	}
	if info := selftest16Info(infos, t.ids[2]); info == nil || info.HasStatus || info.Selected {
		return selftest.Failed(fmt.Errorf("the unfocused third Session status = %+v, want unselected and untouched", info))
	}
	for _, prefix := range []string{"compose-broadcast-one", "compose-broadcast-two"} {
		count, err := t.markerCount(prefix, t.marker)
		if err != nil {
			return selftest.Failed(err)
		}
		if count > 2 {
			return selftest.Failed(fmt.Errorf("%s ran in %d Sessions, want 2", prefix, count))
		}
		if count < 2 {
			return pending(t.broadcastAt, "both broadcast Sessions to run "+prefix)
		}
	}
	return selftest.Passed(
		"the multi-line composed draft reached exactly the two selected Sessions, both reported delivered, and the focused third Session stayed untouched",
		"shell State")
}

type selftest16ComposeFinish struct {
	waiting   bool
	delivered int
}

func (t *selftest16) finishCompose(deadline time.Time) (selftest16ComposeFinish, error) {
	if t.state.ComposeSending() {
		return selftest16ComposeFinish{waiting: true}, nil
	}
	result, ok := t.state.ComposeResult()
	if !ok {
		// The asynchronous line-by-line send has not recorded result yet.
		if time.Now().Before(deadline) {
			return selftest16ComposeFinish{waiting: true}, nil
		}
		return selftest16ComposeFinish{}, errors.New("compose did not record a delivery result")
	}
	return selftest16ComposeFinish{delivered: result.DeliveredCount()}, nil
}

func (t *selftest16) layoutStep(*ui.Context) selftest.Outcome {
	if !t.state.Broadcasting() {
		return selftest.Failed(errors.New("broadcast is not active for the layout check"))
	}
	t.state.SetComposeDraft("  echo one\\necho two  ")
	t.state.OpenBroadcastPanel()
	if !t.state.ComposeOpen() {
		t.state.OpenCompose()
		*t.state.ComposeTrim() = true
		*t.state.ComposeDecodeEscapes() = true
	}
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		tester := ui.NewTester(func(c *ui.Context) {
			shell.RenderSelftestInputs(c, t.state)
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()
		for _, want := range []struct {
			text string
			minH float32
		}{
			{m.BroadcastPanelTitle(), 15},
			{m.BroadcastActive(2), 12},
			{m.BroadcastTargetsTitle(), 12},
			{m.BroadcastDelivered(), 12},
			{m.BroadcastSummary(2, 2), 12},
			{m.BroadcastFocused(), 8},
			{m.BroadcastStop(), 16},
			{m.ComposeTitle(), 15},
			{m.ComposeTargetBroadcast(2), 12},
			{m.ComposePreview(), 11},
			{"echo one\\necho two", 12},
			{m.ComposeTrim(), 16},
			{m.ComposeDecodeEscapes(), 16},
			{m.ComposeSend(), 16},
			{m.ComposeCancel(), 16},
		} {
			rect, ok := tester.Find(want.text)
			if !ok {
				return selftest.Failed(fmt.Errorf("%dx%d lacks %q: %v", size[0], size[1], want.text, tester.Texts()))
			}
			if rect.X < 0 || rect.Y < 0 ||
				rect.X+rect.W > float32(size[0]) || rect.Y+rect.H > float32(size[1]) {
				return selftest.Failed(fmt.Errorf("%dx%d clips %q: %+v", size[0], size[1], want.text, rect))
			}
			if rect.H < want.minH {
				return selftest.Failed(fmt.Errorf("%dx%d clips %q to height %.1f, want at least %.1f",
					size[0], size[1], want.text, rect.H, want.minH))
			}
		}
		panel, _ := tester.Find(m.BroadcastPanelTitle())
		compose, _ := tester.Find(m.ComposeTitle())
		if panel.Y+panel.H > compose.Y+0.5 {
			return selftest.Failed(fmt.Errorf("%dx%d arrange order is wrong: panel %+v compose %+v",
				size[0], size[1], panel, compose))
		}
	}
	return selftest.Passed(
		"the production broadcast panel, per-Session statuses, and compose preview rendered inside 1100x720 and 720x480",
		"shell.RenderSelftestInputs")
}

func (t *selftest16) demoStep(*ui.Context) selftest.Outcome {
	if t.state == nil {
		return selftest.Failed(errors.New("the shell is not wired"))
	}
	if !t.state.Broadcasting() {
		if !t.state.StartBroadcast() {
			return selftest.Failed(errors.New("broadcast did not start for the demo state"))
		}
	}
	t.state.OpenBroadcastPanel()
	if !t.state.ComposeOpen() {
		t.state.OpenCompose()
	}
	t.state.Flush()
	// The next F11,1,6 resets this state, closing these Sessions and removing
	// the marker files before it starts.
	return selftest.Passed(
		"three real local Session Tabs, the active broadcast panel with statuses, and the compose bar are visible",
		"shell State")
}

func (t *selftest16) setDraft(value string) {
	t.state.SetComposeDraft("  " + value + "  ")
}

// markerCount counts marker files a shell command created. The file name is
// unique per shell process, so the count is the number of Sessions that ran
// the input. An empty file is a write still in progress and is ignored until
// the step deadline.
func (t *selftest16) markerCount(prefix, marker string) (int, error) {
	entries, err := os.ReadDir(t.root)
	if err != nil {
		return 0, fmt.Errorf("read the self-test directory: %w", err)
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix+"-") || !strings.HasSuffix(name, ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(t.root, name))
		if err != nil {
			continue
		}
		text := string(data)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if !strings.Contains(text, marker) {
			return 0, fmt.Errorf("marker file %s does not contain %q: %q", name, marker, text)
		}
		count++
	}
	return count, nil
}

func selftest16Info(infos []state.BroadcastSessionInfo, id model.SessionID) *state.BroadcastSessionInfo {
	for index := range infos {
		if infos[index].ID == id {
			return &infos[index]
		}
	}
	return nil
}

// selftest16WriteCommand renders a shell command that writes marker to a file
// whose name contains the shell's own PID. Both selected shells therefore
// write distinct files even though broadcast input is byte-for-byte identical.
func selftest16WriteCommand(root, prefix, suffix, marker string) string {
	name := prefix
	if suffix != "" {
		name += "-" + suffix
	}
	root = filepath.ToSlash(root)
	if runtime.GOOS == "windows" {
		child := "('" + selftest16PowerShellQuote(name) + "-' + $PID + '.txt')"
		return "Set-Content -LiteralPath (Join-Path -Path '" +
			selftest16PowerShellQuote(root) + "' -ChildPath " + child + ") -Value '" +
			selftest16PowerShellQuote(marker) + "' -Encoding ASCII"
	}
	return "printf '%s' '" + selftest16ShellQuote(marker) + "' > '" +
		selftest16ShellQuote(root+"/"+name) + "-'$$'.txt'"
}

func selftest16PowerShellQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func selftest16ShellQuote(value string) string {
	return strings.ReplaceAll(value, "'", `'\''`)
}
