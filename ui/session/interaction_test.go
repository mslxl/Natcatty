package sessionui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

func newInteractionTester(t *testing.T, model chromeModel, actions chromeActions,
	width, height int) *ui.Tester {
	t.Helper()
	tester := ui.NewTester(func(c *ui.Context) {
		renderChrome(c, model, locales.In(c.Locale()), c.Theme(), actions)
	}, width, height)
	tester.SetLocale("en")
	tester.Frame()
	return tester
}

func TestSessionMenuListsTheProductionItemsInOrder(t *testing.T) {
	m := locales.In("en")
	model := chromeModel{
		Encoding:     domainterminal.EncodingGB18030,
		LogFormat:    domainterminal.LogFormatText,
		LogDirectory: `C:\logs`,
		CanPaste:     true,
		CanReconnect: true,
	}
	noop := func() {}
	tester := newInteractionTester(t, model, chromeActions{
		Copy:           noop,
		Paste:          noop,
		PasteSelection: noop,
		SelectAll:      noop,
		OpenBrowser:    noop,
		Reconnect:      noop,
		SendYmodem:     noop,
		ReceiveYmodem:  noop,
		SaveScreen:     noop,
		ClearBuffer:    noop,
		Rename:         noop,
		SetEncoding:    func(domainterminal.Encoding) {},
		ToggleLog:      noop,
		SetLogFormat:   func(domainterminal.LogFormat) {},
		SplitRight:     noop,
		SplitDown:      noop,
		Detach:         noop,
		ClosePane:      noop,
	}, 720, 480)

	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatalf("open the Session menu: %v (texts %v)", err, tester.Texts())
	}
	// The order follows the original terminal context menu, with Encoding and
	// Logging after the screen commands because the original put those in its
	// toolbar.
	want := []string{
		m.SessionMenuCopy(), m.SessionMenuPaste(), m.SessionMenuPasteSelection(), m.SessionMenuSelectAll(), "-",
		m.SessionMenuOpenBrowser(), "-",
		m.SessionMenuReconnect(), "-",
		m.SessionMenuSendYmodem(), m.SessionMenuReceiveYmodem(), "-",
		m.SessionMenuSplitHorizontal(), m.SessionMenuSplitVertical(), "-",
		m.SessionMenuSaveScreen(), m.SessionMenuClearBuffer(),
		m.SessionMenuEncoding(), m.SessionMenuLogging(), "-",
		m.SessionMenuHibernate(), m.SessionMenuObserveSnapshot(), "-",
		m.SessionMenuRename(), "-",
		m.SessionMenuDetach(), "-",
		m.SessionMenuCloseTerminal(),
	}
	got := tester.Menu()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("menu = %v, want %v", got, want)
	}
}

func TestSessionMenuLifetimeSlotOffersResumeWhileHibernated(t *testing.T) {
	m := locales.In("en")
	resumed := false
	model := chromeModel{
		Encoding:   domainterminal.EncodingUTF8,
		LogFormat:  domainterminal.LogFormatText,
		Hibernated: true,
	}
	tester := newInteractionTester(t, model, chromeActions{
		Hibernate: func() { t.Error("Hibernate chosen while hibernated") },
		Resume:    func() { resumed = true },
	}, 720, 480)

	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatal(err)
	}
	menu := tester.Menu()
	if contains(menu, m.SessionMenuHibernate()) {
		t.Fatalf("a hibernated Session still offers Hibernate: %v", menu)
	}
	if err := tester.ChooseMenuItem(m.SessionResume()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !resumed {
		t.Fatal("Resume did not reach the surface")
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestSessionMenuActionsReachTheSurfaceAndUnavailableItemsAreDisabled(t *testing.T) {
	m := locales.In("en")
	model := chromeModel{
		Encoding:     domainterminal.EncodingUTF8,
		LogFormat:    domainterminal.LogFormatRaw,
		LogDirectory: "",
	}
	var encoded domainterminal.Encoding
	var format domainterminal.LogFormat
	var splitRight, splitDown bool
	tester := newInteractionTester(t, model, chromeActions{
		Paste:       func() {},
		ClearBuffer: func() {},
		SetEncoding: func(value domainterminal.Encoding) { encoded = value },
		ToggleLog:   func() {},
		SetLogFormat: func(value domainterminal.LogFormat) {
			format = value
		},
		SplitRight: func() { splitRight = true },
		SplitDown:  func() { splitDown = true },
	}, 1100, 720)

	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuEncoding(), "GB18030"); err != nil {
		t.Fatalf("choose encoding: %v", err)
	}
	if encoded != domainterminal.EncodingGB18030 {
		t.Fatalf("encoding action = %q", encoded)
	}

	tester.Frame()
	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuLogging(), m.SessionMenuLogFormat(), "Raw"); err != nil {
		t.Fatalf("choose log format: %v", err)
	}
	if format != domainterminal.LogFormatRaw {
		t.Fatalf("format action = %q", format)
	}

	tester.Frame()
	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuSplitHorizontal()); err != nil {
		t.Fatalf("split action: %v", err)
	}
	if !splitDown {
		t.Fatal("Split Horizontal did not reach the top/bottom split action")
	}
	if splitRight {
		t.Fatal("Split Horizontal reached the left/right split action")
	}

	// With no log directory and no Pane backends, Start, Detach, and Close
	// Pane are visible but disabled. Choosing a disabled item errors, which is
	// how the real menu refuses it.
	tester.Frame()
	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuLogging(), m.SessionLoggingStart()); err == nil {
		t.Fatal("Start log was choosable with no directory")
	}
	tester.Frame()
	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuDetach()); err == nil {
		t.Fatal("Detach Pane was choosable without a Pane action")
	}
}

func TestPastePromptShowsCountsAndCallsExactlyOneAction(t *testing.T) {
	m := locales.In("en")
	pending := PendingPaste{
		Text:    "first\nsecond",
		Lines:   2,
		Chars:   12,
		Preview: "first\nsecond",
	}
	accepted, cancelled := 0, 0
	tester := newInteractionTester(t, chromeModel{
		Encoding:     domainterminal.EncodingUTF8,
		LogFormat:    domainterminal.LogFormatText,
		PendingPaste: &pending,
	}, chromeActions{
		AcceptPaste: func() { accepted++ },
		CancelPaste: func() { cancelled++ },
	}, 720, 480)

	for _, text := range []string{
		m.SessionPasteConfirmTitle(2),
		m.SessionPasteConfirmBody(12),
		m.SessionPastePreviewLabel(),
		m.SessionPasteSend(),
		m.SessionPasteCancel(),
		"first\nsecond",
	} {
		if !tester.HasText(text) {
			t.Fatalf("the confirmation does not show %q: %v", text, tester.Texts())
		}
	}
	if err := tester.Click(m.SessionPasteCancel()); err != nil {
		t.Fatal(err)
	}
	if cancelled != 1 || accepted != 0 {
		t.Fatalf("cancel = %d, accept = %d", cancelled, accepted)
	}
}

func TestDropBannerShowsDragAndOutcomeStates(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		model chromeModel
		want  string
	}{
		{chromeModel{DropActive: true, DropAction: domainterminal.DropInsert},
			m.SessionDropActive(m.SessionDropInsertAction())},
		{chromeModel{DropActive: true, DropAction: domainterminal.DropUpload},
			m.SessionDropActive(m.SessionDropUploadAction())},
		{chromeModel{DropNotice: DropNoticeInserted, DropCount: 3},
			m.SessionDropInserted(3)},
		{chromeModel{DropNotice: DropNoticeUploaded, DropCount: 2},
			m.SessionDropUploaded(2)},
		{chromeModel{DropNotice: DropNoticeUnavailable},
			m.SessionDropUnavailable()},
		{chromeModel{DropNotice: DropNoticeFailed},
			m.SessionDropFailed()},
	}
	for _, test := range cases {
		tester := newInteractionTester(t, test.model, chromeActions{}, 720, 480)
		if !tester.HasText(test.want) {
			t.Errorf("drop banner does not show %q: %v", test.want, tester.Texts())
		}
	}
}

func TestInteractionControlsFitAtBothSizes(t *testing.T) {
	m := locales.In("en")
	model := chromeModel{
		Command:      appsession.CommandState{Text: "make test", Running: true},
		Busy:         true,
		Logging:      appsession.LogStatus{Active: true, Format: domainterminal.LogFormatText, Path: `C:\logs\session.txt`},
		LogFormat:    domainterminal.LogFormatText,
		LogDirectory: `C:\logs`,
		Encoding:     domainterminal.EncodingLatin1,
		PendingPaste: &PendingPaste{
			Text: "one\ntwo\nthree", Lines: 3, Chars: 13, Preview: "one\ntwo\nthree",
		},
		DropActive: true,
		DropAction: domainterminal.DropInsert,
	}
	noop := func() {}
	actions := chromeActions{
		ToggleLog: noop, SetLogFormat: func(domainterminal.LogFormat) {},
		SetEncoding: func(domainterminal.Encoding) {}, Paste: noop, ClearBuffer: noop,
		AcceptPaste: noop, CancelPaste: noop, DismissDrop: noop,
		SplitRight: noop, SplitDown: noop, Detach: noop, ClosePane: noop,
	}
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		tester := newInteractionTester(t, model, actions, size[0], size[1])
		wanted := []string{
			m.SessionMenuLabel(),
			m.SessionPasteConfirmTitle(3),
			m.SessionPasteSend(),
			m.SessionPasteCancel(),
			m.SessionPastePreviewLabel(),
			m.SessionDropActive(m.SessionDropInsertAction()),
			m.SessionLoggingPath(model.Logging.Path),
		}
		rects := make(map[string]ui.Rect, len(wanted))
		const epsilon = 0.5
		for _, text := range wanted {
			rect, ok := tester.Find(text)
			if !ok {
				t.Fatalf("%dx%d does not show %q: %v", size[0], size[1], text, tester.Texts())
			}
			if rect.X < -epsilon || rect.Y < -epsilon ||
				rect.X+rect.W > float32(size[0])+epsilon ||
				rect.Y+rect.H > float32(size[1])+epsilon {
				t.Fatalf("%dx%d clips %q: %+v", size[0], size[1], text, rect)
			}
			rects[text] = rect
		}
		if rects[m.SessionPasteConfirmTitle(3)].Y >= rects[m.SessionDropActive(m.SessionDropInsertAction())].Y {
			t.Fatalf("%dx%d paste prompt is not above the drop banner", size[0], size[1])
		}
		send := rects[m.SessionPasteSend()]
		cancel := rects[m.SessionPasteCancel()]
		if send.X+send.W > cancel.X+epsilon {
			t.Fatalf("%dx%d Send overlaps Cancel", size[0], size[1])
		}
	}
}

// TestDropNoticeDismissIsHitTestableAndClearsTheBanner covers the demo state's
// Cancel bug: the drop banner had no uniquely named control, so clicking the
// visible Cancel resolved to the paste prompt's Cancel instead.
func TestDropNoticeDismissIsHitTestableAndClearsTheBanner(t *testing.T) {
	m := locales.In("en")
	model := chromeModel{DropNotice: DropNoticeInserted, DropCount: 2}
	dismissed := 0
	tester := ui.NewTester(func(c *ui.Context) {
		renderChrome(c, model, locales.In(c.Locale()), c.Theme(), chromeActions{
			DismissDrop: func() {
				dismissed++
				model.DropNotice = DropNoticeNone
				model.DropCount = 0
			},
		})
	}, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	notice := m.SessionDropInserted(2)
	if !tester.HasText(notice) {
		t.Fatalf("the drop result is not shown: %v", tester.Texts())
	}
	rect, ok := tester.Find(m.SessionDropDismissLabel())
	if !ok {
		t.Fatalf("the drop result has no uniquely named Dismiss: %v", tester.Texts())
	}
	if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > 720 || rect.Y+rect.H > 480 {
		t.Fatalf("Dismiss is outside the window: %+v", rect)
	}
	if err := tester.Click(m.SessionDropDismissLabel()); err != nil {
		t.Fatal(err)
	}
	if dismissed != 1 {
		t.Fatalf("Dismiss calls = %d, want 1", dismissed)
	}
	tester.Frame()
	if tester.HasText(notice) {
		t.Fatalf("the drop result survived Dismiss: %v", tester.Texts())
	}
}

// TestPasteItemFollowsTheClipboardState is D4: Paste used to be enabled
// whenever the surface had a paste closure, even with an empty clipboard.
func TestPasteItemFollowsTheClipboardState(t *testing.T) {
	m := locales.In("en")
	for _, canPaste := range []bool{false, true} {
		canPaste := canPaste
		pasted := 0
		model := chromeModel{
			Encoding:  domainterminal.EncodingUTF8,
			LogFormat: domainterminal.LogFormatText,
			CanPaste:  canPaste,
		}
		tester := newInteractionTester(t, model, chromeActions{
			Paste: func() { pasted++ },
		}, 720, 480)
		if err := tester.Click(m.SessionMenuLabel()); err != nil {
			t.Fatal(err)
		}
		err := tester.ChooseMenuItem(m.SessionMenuPaste())
		if !canPaste {
			if err == nil {
				t.Fatal("Paste was choosable with an empty clipboard")
			}
			if pasted != 0 {
				t.Fatalf("empty-clipboard Paste ran %d times", pasted)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Paste was refused with a non-empty clipboard: %v", err)
		}
		if pasted != 1 {
			t.Fatalf("Paste ran %d times, want 1", pasted)
		}
	}
}

// TestSessionMenuRefusesDisabledItems names the disabled-item contract
// directly, so a future enabled-looking but inert item fails.
func TestSessionMenuRefusesDisabledItems(t *testing.T) {
	m := locales.In("en")
	tester := newInteractionTester(t, chromeModel{
		Encoding:  domainterminal.EncodingUTF8,
		LogFormat: domainterminal.LogFormatText,
	}, chromeActions{}, 1100, 720)
	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatal(err)
	}
	for _, path := range [][]string{
		{m.SessionMenuCopy()},
		{m.SessionMenuPaste()},
		{m.SessionMenuPasteSelection()},
		{m.SessionMenuSelectAll()},
		{m.SessionMenuOpenBrowser()},
		{m.SessionMenuReconnect()},
		{m.SessionMenuSendYmodem()},
		{m.SessionMenuReceiveYmodem()},
		{m.SessionMenuSaveScreen()},
		{m.SessionMenuClearBuffer()},
		{m.SessionMenuSplitHorizontal()},
		{m.SessionMenuSplitVertical()},
		{m.SessionMenuLogging(), m.SessionLoggingStart()},
		{m.SessionMenuRename()},
		{m.SessionMenuDetach()},
		{m.SessionMenuCloseTerminal()},
	} {
		tester.Frame()
		if err := tester.Click(m.SessionMenuLabel()); err != nil {
			t.Fatalf("reopen menu: %v", err)
		}
		if err := tester.ChooseMenuItem(path...); err == nil {
			t.Errorf("disabled item %v was choosable", path)
		}
	}
}
