//go:build selftest

package app

// Ticket 16 self-test.
//
// Press F11, then 1, then 6. The check runs on the UI thread one step per
// frame and writes %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-16.json.
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

	// detachStep removes one selected target from the main window and proves
	// that neither typed nor composed broadcast input reaches it until it is
	// moved back.
	detachedTabID      model.TabID
	detachedSession    *appsession.Session
	detachTypedSent    bool
	detachTypedAt      time.Time
	detachSourceSent   bool
	detachSourceAt     time.Time
	detachComposeSent  bool
	detachComposeAt    time.Time
	detachPreview      string
	detachRestoredSent bool
	detachRestoredAt   time.Time

	detachStarted          bool
	detachTypedVerified    bool
	detachSourceVerified   bool
	detachComposeVerified  bool
	detachRestoredVerified bool

	// demoStep ends one selected Session so the final panel shows a Missing
	// row beside a Delivered one and the compose header uses the singular.
	demoClosed bool
	demoAt     time.Time
	demoSent   bool
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
			{Name: "broadcast-detached-target", Run: t.detachStep},
			{Name: "broadcast-compose-layout", Run: t.layoutStep},
			{Name: "broadcast-compose-visible", Run: t.demoStep},
		},
	}
}

func (t *selftest16) resetRunState() {
	if t.state == nil {
		return
	}
	// A run interrupted after the detach step must put the Tab back before it
	// is closed, or the selection and the visible Tabs would disagree.
	if t.detachedTabID != "" {
		if !t.state.ReattachDetachedTab(t.detachedTabID) {
			t.state.CloseDetachedTab(t.detachedTabID)
		}
		t.detachedTabID = ""
	}
	t.detachedSession = nil
	t.detachTypedSent = false
	t.detachTypedAt = time.Time{}
	t.detachSourceSent = false
	t.detachSourceAt = time.Time{}
	t.detachComposeSent = false
	t.detachComposeAt = time.Time{}
	t.detachPreview = ""
	t.detachRestoredSent = false
	t.detachRestoredAt = time.Time{}
	t.detachStarted = false
	t.detachTypedVerified = false
	t.detachSourceVerified = false
	t.detachComposeVerified = false
	t.detachRestoredVerified = false
	t.demoClosed = false
	t.demoAt = time.Time{}
	t.demoSent = false
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
	if t.root == "" {
		root, err := os.MkdirTemp("", "natcatty-selftest-16-")
		if err != nil {
			root = filepath.Join(os.TempDir(),
				fmt.Sprintf("natcatty-selftest-16-%d", os.Getpid()))
		}
		t.root = root
	}
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

// detachStep is the detached-target regression on the real artifact: one of
// the two selected Sessions leaves the main window, and both typed and
// composed broadcast input must stop reaching it while the panel and compose
// header agree on the one remaining target. Typing inside the detached window
// must not fan out through the main selection either. The Tab is moved back
// afterwards so the later layout and demo steps see two live targets again.
func (t *selftest16) detachStep(*ui.Context) selftest.Outcome {
	if len(t.ids) < 3 {
		return selftest.Failed(errors.New("the broadcast Sessions are not prepared"))
	}
	if !t.detachStarted {
		if !t.state.Broadcasting() {
			return selftest.Failed(errors.New("broadcast is not active for the detach check"))
		}
		if got := len(t.state.BroadcastTargets()); got != 2 {
			return selftest.Failed(fmt.Errorf("targets before detach = %d, want 2", got))
		}
		source, ok := t.state.SessionByID(t.ids[0])
		if !ok {
			return selftest.Failed(errors.New("the first target is no longer open"))
		}
		index := tabIndex(t.state, t.tabs[0])
		if index < 0 {
			return selftest.Failed(errors.New("the first target has no Tab"))
		}
		if !t.state.DetachTab(index) {
			return selftest.Failed(errors.New("the first target did not detach"))
		}
		t.detachedTabID = t.tabs[0]
		t.detachedSession = source

		targets := t.state.BroadcastTargets()
		if len(targets) != 1 || targets[0] != t.ids[1] {
			return selftest.Failed(fmt.Errorf("targets after detach = %v, want only %q", targets, t.ids[1]))
		}
		if got := t.state.BroadcastNotice(); got != 1 {
			return selftest.Failed(fmt.Errorf("removal notice = %d, want 1", got))
		}
		if info := selftest16Info(t.state.BroadcastSessions(), t.ids[0]); info != nil {
			return selftest.Failed(fmt.Errorf("the detached Session still has a target row: %+v", info))
		}
		if _, ok := t.state.BroadcastResult(); ok {
			return selftest.Failed(errors.New("a delivery result survived the detach"))
		}
		composeTargets, broadcast := t.state.ComposeTargets()
		if !broadcast || len(composeTargets) != 1 || composeTargets[0] != t.ids[1] {
			return selftest.Failed(fmt.Errorf("compose targets after detach = %v broadcast=%v", composeTargets, broadcast))
		}
		t.detachStarted = true
	}

	if !t.detachTypedSent {
		session, ok := t.state.SessionByID(t.ids[2])
		if !ok {
			return selftest.Failed(errors.New("the source Session is no longer open"))
		}
		command := selftest16WriteCommand(t.root, "detach-typed", "", t.marker)
		if _, err := session.TerminalConn().Write([]byte(command + "\r")); err != nil {
			return selftest.Failed(fmt.Errorf("type while a target is detached: %w", err))
		}
		t.detachTypedSent = true
		t.detachTypedAt = time.Now().Add(selftestTimeout)
	}
	if !t.detachTypedVerified {
		result, ok := t.state.BroadcastResult()
		if !ok || len(result.Deliveries) != 1 || result.Deliveries[0].SessionID != t.ids[1] ||
			result.Deliveries[0].Status != appsession.DeliveryDelivered || result.DeliveredCount() != 1 {
			if !ok && time.Now().Before(t.detachTypedAt) {
				return pending(t.detachTypedAt, "the typed result after the detach")
			}
			return selftest.Failed(fmt.Errorf("typed broadcasts after detach = %+v, want only the second Session", result.Deliveries))
		}
		count, err := t.markerCount("detach-typed", t.marker)
		if err != nil {
			return selftest.Failed(err)
		}
		if count > 2 {
			return selftest.Failed(fmt.Errorf("%d Sessions ran the typed marker, want the source and the one remaining target", count))
		}
		if count < 2 {
			return pending(t.detachTypedAt, "the source and the remaining target to write the typed marker")
		}
		t.detachTypedVerified = true
	}

	// Typing inside the detached window must not fan out through the main
	// target set. The detached shell runs its own command, nobody else.
	if !t.detachSourceSent {
		if t.detachedSession == nil {
			return selftest.Failed(errors.New("the detached Session is not retained"))
		}
		command := selftest16WriteCommand(t.root, "detach-source", "", t.marker)
		if _, err := t.detachedSession.TerminalConn().Write([]byte(command + "\r")); err != nil {
			return selftest.Failed(fmt.Errorf("type in the detached window: %w", err))
		}
		t.detachSourceSent = true
		t.detachSourceAt = time.Now().Add(selftestTimeout)
	}
	if !t.detachSourceVerified {
		count, err := t.markerCount("detach-source", t.marker)
		if err != nil {
			return selftest.Failed(err)
		}
		if count > 1 {
			return selftest.Failed(fmt.Errorf("input typed in the detached window reached %d Sessions, want only itself", count))
		}
		if count < 1 {
			return pending(t.detachSourceAt, "the detached shell to write its own marker")
		}
		t.detachSourceVerified = true
	}

	if !t.detachComposeSent {
		t.state.OpenCompose()
		*t.state.ComposeTrim() = true
		*t.state.ComposeDecodeEscapes() = true
		t.state.SetComposeLineMode(appsession.ComposePaste)
		command := selftest16WriteCommand(t.root, "detach-compose", "", t.marker)
		t.detachPreview = command
		t.setDraft(command)
		if t.state.ComposePreview().Text != command {
			return selftest.Failed(fmt.Errorf("detach compose preview = %q, want %q",
				t.state.ComposePreview().Text, command))
		}
		composeTargets, broadcast := t.state.ComposeTargets()
		if !broadcast || len(composeTargets) != 1 || composeTargets[0] != t.ids[1] {
			return selftest.Failed(fmt.Errorf("compose targets before the detached send = %v broadcast=%v", composeTargets, broadcast))
		}
		if !t.state.SendCompose() {
			return selftest.Failed(errors.New("compose did not send after the detach"))
		}
		t.detachComposeSent = true
		t.detachComposeAt = time.Now().Add(selftestTimeout)
	}
	if !t.detachComposeVerified {
		composeResult, ok := t.state.ComposeResult()
		if !ok || composeResult.DeliveredCount() != 1 || len(composeResult.Deliveries) != 1 ||
			composeResult.Deliveries[0].SessionID != t.ids[1] {
			if !ok && time.Now().Before(t.detachComposeAt) {
				return pending(t.detachComposeAt, "the composed result after the detach")
			}
			return selftest.Failed(fmt.Errorf("composed broadcast after detach = %+v, want only the second Session", composeResult.Deliveries))
		}
		count, err := t.markerCount("detach-compose", t.marker)
		if err != nil {
			return selftest.Failed(err)
		}
		if count > 1 {
			return selftest.Failed(fmt.Errorf("the composed draft reached %d Sessions, want only the remaining target", count))
		}
		if count < 1 {
			return pending(t.detachComposeAt, "the remaining target to run the composed marker")
		}
		infos := t.state.BroadcastSessions()
		if info := selftest16Info(infos, t.ids[1]); info == nil || !info.HasStatus || info.Status != appsession.DeliveryDelivered {
			return selftest.Failed(fmt.Errorf("the remaining target status = %+v, want delivered", info))
		}
		if info := selftest16Info(infos, t.ids[0]); info != nil {
			return selftest.Failed(fmt.Errorf("the detached Session reappeared in the target list: %+v", info))
		}
		t.detachComposeVerified = true
	}

	// Put the Tab back and restore the two-target selection. A reattached
	// target must receive broadcast input again.
	if t.detachedTabID != "" {
		if !t.state.ReattachDetachedTab(t.detachedTabID) {
			return selftest.Failed(errors.New("the detached Tab did not reattach"))
		}
		t.detachedTabID = ""
		t.detachedSession = nil
		t.state.SetBroadcastTarget(t.ids[0], true)
		if got := len(t.state.BroadcastTargets()); got != 2 {
			return selftest.Failed(fmt.Errorf("targets after reattach = %d, want 2", got))
		}
		if !t.state.StartBroadcast() {
			return selftest.Failed(errors.New("broadcast did not restart after the reattach"))
		}
	}
	if !t.detachRestoredSent {
		session, ok := t.state.SessionByID(t.ids[2])
		if !ok {
			return selftest.Failed(errors.New("the source Session is no longer open after the reattach"))
		}
		command := selftest16WriteCommand(t.root, "detach-restored", "", t.marker)
		if _, err := session.TerminalConn().Write([]byte(command + "\r")); err != nil {
			return selftest.Failed(fmt.Errorf("type after the reattach: %w", err))
		}
		t.detachRestoredSent = true
		t.detachRestoredAt = time.Now().Add(selftestTimeout)
	}
	if !t.detachRestoredVerified {
		result, ok := t.state.BroadcastResult()
		if !ok || len(result.Deliveries) != 2 || result.DeliveredCount() != 2 {
			if !ok && time.Now().Before(t.detachRestoredAt) {
				return pending(t.detachRestoredAt, "the broadcast result after the reattach")
			}
			return selftest.Failed(fmt.Errorf("broadcast after the reattach = %+v, want two deliveries", result.Deliveries))
		}
		for _, id := range t.ids[:2] {
			info := selftest16Info(t.state.BroadcastSessions(), id)
			if info == nil || !info.HasStatus || info.Status != appsession.DeliveryDelivered {
				return selftest.Failed(fmt.Errorf("the reattached target status for %q = %+v", id, info))
			}
		}
		count, err := t.markerCount("detach-restored", t.marker)
		if err != nil {
			return selftest.Failed(err)
		}
		if count > 3 {
			return selftest.Failed(fmt.Errorf("%d Sessions ran the post-reattach marker, want the source and both targets", count))
		}
		if count < 3 {
			return pending(t.detachRestoredAt, "the source and both targets to run the post-reattach marker")
		}
		t.detachRestoredVerified = true
	}
	return selftest.Passed(
		"a selected target left the main window: typed and composed broadcast input reached only the one remaining target, the panel and compose header agreed, input typed in the detached window stayed there, and the reattached target received broadcast input again",
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

// demoStep leaves a state that shows every ticket-16 surface at once: an
// active two-target selection where one Session has ended (a visible Missing
// row beside a Delivered one), a compose header with a singular result on one
// line, a long multi-line preview, and three Tabs. The next F11,1,6 resets it.
func (t *selftest16) demoStep(*ui.Context) selftest.Outcome {
	if t.state == nil {
		return selftest.Failed(errors.New("the shell is not wired"))
	}
	if len(t.ids) < 3 {
		return selftest.Failed(errors.New("the broadcast Sessions are not prepared"))
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

	if !t.demoClosed {
		session, ok := t.state.SessionByID(t.ids[1])
		if !ok {
			return selftest.Failed(errors.New("the demo target is no longer open"))
		}
		// Ending the Session keeps its Tab and selected row; the next send
		// reports it as Missing instead of hiding it.
		_ = session.Close()
		t.demoClosed = true
		t.demoAt = time.Now().Add(selftestTimeout)
	}
	info := selftest16Info(t.state.BroadcastSessions(), t.ids[1])
	if info == nil {
		return selftest.Failed(errors.New("the ended target lost its panel row"))
	}
	if info.Running {
		if time.Now().After(t.demoAt) {
			return selftest.Failed(errors.New("the demo target did not end"))
		}
		return pending(t.demoAt, "the second target to end")
	}

	if !t.demoSent {
		*t.state.ComposeTrim() = true
		*t.state.ComposeDecodeEscapes() = true
		t.state.SetComposeLineMode(appsession.ComposePaste)
		t.state.SetComposeDraft("echo selftest demo")
		if !t.state.SendCompose() {
			return selftest.Failed(errors.New("the demo compose send failed"))
		}
		t.demoSent = true
	}
	result, ok := t.state.ComposeResult()
	if !ok {
		if time.Now().After(t.demoAt) {
			return selftest.Failed(errors.New("the demo compose recorded no result"))
		}
		return pending(t.demoAt, "the demo compose result")
	}
	if len(result.Deliveries) != 2 || result.DeliveredCount() != 1 {
		return selftest.Failed(fmt.Errorf("demo compose result = %+v, want one delivered and one missing", result.Deliveries))
	}
	statuses := map[model.SessionID]appsession.DeliveryStatus{}
	for _, delivery := range result.Deliveries {
		statuses[delivery.SessionID] = delivery.Status
	}
	if statuses[t.ids[0]] != appsession.DeliveryDelivered || statuses[t.ids[1]] != appsession.DeliveryMissing {
		return selftest.Failed(fmt.Errorf("demo statuses = %+v, want delivered and missing", statuses))
	}
	if broadcast, ok := t.state.BroadcastResult(); !ok || len(broadcast.Deliveries) != 2 ||
		broadcast.DeliveredCount() != 1 {
		return selftest.Failed(fmt.Errorf("demo panel result = %+v", broadcast.Deliveries))
	}

	// A long, multi-line draft proves the preview wraps and scrolls instead of
	// showing only the first line.
	t.state.SetComposeDraft("echo demo header\n" +
		"echo first long argument --flag value --another value\n" +
		"echo second long argument --flag value --another value\n" +
		"echo third long argument --flag value --another value\n" +
		"echo fourth long argument --flag value --another value\n" +
		"echo fifth long argument --flag value --another value\n" +
		"echo demo footer")
	t.state.Flush()
	return selftest.Passed(
		"three real local Session Tabs, an active broadcast with Delivered and Missing rows, a one-line compose header, and a long multi-line preview are visible",
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
