package browser

// This file mounts the real browser panel inside the real shell at the
// minimum window's client area. The panel's own tests render it alone; only
// the shell supplies the 232-DIP sidebar and the 16-DIP padding that make the
// content column 440 DIPs wide at 704x421, which is where independent
// acceptance at 960f22b1 measured the clipped tab strip.

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	uishell "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

// The shell's fixed geometry at the enforced 720x480 window: a 704x421 client,
// a 232-DIP sidebar, and 16-DIP padding around the content column.
const (
	shellMinimumWidth   = 704
	shellMinimumHeight  = 421
	shellSidebarWidth   = 232
	shellContentPadding = 16
)

func shellContentColumn() (left, right float32) {
	return shellSidebarWidth + shellContentPadding,
		shellMinimumWidth - shellContentPadding
}

// browserShellTransport is a quiet in-process terminal transport: no bytes
// flow, and Close releases the read so the Session can shut down.
type browserShellTransport struct {
	done chan struct{}
	once sync.Once
}

func (t *browserShellTransport) Read([]byte) (int, error) {
	<-t.done
	return 0, io.EOF
}

func (t *browserShellTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *browserShellTransport) Resize(int, int) error       { return nil }

func (t *browserShellTransport) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}

type browserShellFactory struct {
	mu         sync.Mutex
	transports []*browserShellTransport
}

func (f *browserShellFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	transport := &browserShellTransport{done: make(chan struct{})}
	f.mu.Lock()
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

// browserShellHarness owns the shell-mounted Session and browser panel.
type browserShellHarness struct {
	shell     *shellstate.State
	tester    *ui.Tester
	service   *appsession.Service
	factory   *browserShellFactory
	connector *fakeConnector
	pages     *fakePages
	bookmarks *appbrowser.Bookmarks
	context   *ui.Context
	core      *appbrowser.Panel
	panel     *Panel
}

// newBrowserShellTester opens one SSH Session through the production shell
// and opens the browser from its Session menu, exactly as a user does.
func newBrowserShellTester(t *testing.T, locale string, width, height int) *browserShellHarness {
	t.Helper()
	h := &browserShellHarness{
		factory:   &browserShellFactory{},
		connector: &fakeConnector{},
		pages:     &fakePages{},
	}
	nextBookmarkID := 0
	h.bookmarks, _ = appbrowser.NewBookmarks(&polishBookmarkStore{}, time.Now, func() string {
		nextBookmarkID++
		return "shell-bookmark-" + itoa(nextBookmarkID)
	})
	h.service = appsession.NewService(h.factory)
	opener := func(session *appsession.Session) (shellstate.Surface, error) {
		return sessionui.New(session, sessionui.WithBrowser(
			func(onClose func()) (sessionui.BrowserPanel, error) {
				core := appbrowser.New(appbrowser.Config{
					HostID:    session.HostID(),
					Label:     session.Label(),
					Connector: h.connector,
					Bookmarks: h.bookmarks,
				})
				panel := New(core, h.pages, WithCloseRequest(onClose))
				h.core = core
				h.panel = panel
				return panel, nil
			}))
	}
	h.shell = shellstate.NewState(shellstate.WithSessions(h.service, opener))
	t.Cleanup(func() {
		if h.panel != nil {
			h.panel.Close()
		}
		h.shell.CloseAllSessions()
		h.service.CloseAll()
		h.shell.Shutdown()
	})

	view := uishell.View(h.shell)
	h.tester = ui.NewTester(func(c *ui.Context) {
		h.context = c
		view(c)
	}, width, height)
	h.tester.SetLocale(locale)
	h.tester.Frame()

	host := domainvault.Host{
		ID:       model.HostID("host-1"),
		Label:    "web-1",
		Hostname: "web-1.selftest",
		Port:     22,
		Username: "root",
		Protocol: domainvault.ProtocolSSH,
	}
	if err := h.shell.OpenHost(host); err != nil {
		t.Fatalf("open the self-test Session: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.shell.TabCount() != 1 {
		h.shell.Flush()
		h.tester.Frame()
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the Session to attach; texts %q", h.tester.Texts())
		}
	}

	m := locales.In(locale)
	if err := h.tester.Click(m.SessionMenuFor(1)); err != nil {
		t.Fatalf("open the Session menu: %v", err)
	}
	h.tester.Frame()
	if err := h.tester.ChooseMenuItem(m.SessionMenuOpenBrowser()); err != nil {
		t.Fatalf("choose Open Browser: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		h.tester.Frame()
		if h.core != nil && h.core.State().Route == appbrowser.RouteConnected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the browser route; texts %q", h.tester.Texts())
		}
	}
	h.waitSettled(t, m, m.BrowserNewTab(), m.BrowserAddress())
	return h
}

// waitSettled frames until the shell reports the same positive geometry for
// the content pane and the named labels on two consecutive frames. The shell
// applies session state asynchronously, so a measurement taken right after
// the browser opens can land on a pane that is still taking its final size.
func (h *browserShellHarness) waitSettled(t *testing.T, m locales.Messages, labels ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var previous []ui.Rect
	stable := 0
	for {
		h.shell.Flush()
		h.tester.Frame()
		rects := make([]ui.Rect, 0, len(labels)+1)
		pane, ok := h.tester.Find(m.PaneRegion(1))
		good := ok && pane.W > 0 && pane.H > 0
		rects = append(rects, pane)
		for _, label := range labels {
			rect, found := h.tester.Find(label)
			if !found || rect.W <= 0 || rect.H <= 0 {
				good = false
			}
			rects = append(rects, rect)
		}
		if good && slices.Equal(rects, previous) {
			stable++
			if stable >= 2 {
				return
			}
		} else {
			stable = 0
		}
		previous = rects
		if time.Now().After(deadline) {
			t.Fatalf("the shell's pane never settled: pane %+v, labels %v; texts %q",
				pane, labels, h.tester.Texts())
		}
	}
}

func (h *browserShellHarness) newTabTitles(t *testing.T, count int) []appbrowser.Tab {
	t.Helper()
	for len(h.core.State().Tabs) < count {
		if _, err := h.core.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	tabs := h.core.State().Tabs
	if len(tabs) != count {
		t.Fatalf("tabs = %d, want %d", len(tabs), count)
	}
	for index, tab := range tabs {
		title := fmt.Sprintf("Tab %d", index+1)
		h.core.PageChanged(tab.ID, appbrowser.PageState{Title: title})
	}
	h.tester.Frame()
	return append([]appbrowser.Tab(nil), h.core.State().Tabs...)
}

// shellTabBox returns the committed boxes of a tab's item and close control,
// and of the strip viewport and New tab control, in client DIPs. A handle the
// test has not built yet returns an empty box instead of panicking.
func (h *browserShellHarness) shellTabBox(tabID string) (item, closeRect, view, plus ui.Rect) {
	if h.context == nil {
		return ui.Rect{}, ui.Rect{}, ui.Rect{}, ui.Rect{}
	}
	if handle := h.panel.tabItems[tabID]; handle != nil {
		item = handle.Bounds(h.context)
	}
	if handle := h.panel.tabCloses[tabID]; handle != nil {
		closeRect = handle.Bounds(h.context)
	}
	if h.panel.stripView != nil {
		view = h.panel.stripView.Bounds(h.context)
	}
	if h.panel.newTab != nil {
		plus = h.panel.newTab.Bounds(h.context)
	}
	return
}

// waitShellTabRevealed frames until the tab's item and close control are
// whole inside the strip's viewport, then reports their client boxes.
func (h *browserShellHarness) waitShellTabRevealed(t *testing.T, tabID string) (item, closeRect, view, plus ui.Rect) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.shell.Flush()
		h.tester.Frame()
		item, closeRect, view, plus = h.shellTabBox(tabID)
		if boundsInside(item, view) && boundsInside(closeRect, view) {
			return item, closeRect, view, plus
		}
		if time.Now().After(deadline) {
			t.Fatalf("the active tab %q never became whole in the shell's strip", tabID)
		}
	}
}

func shellColumnBox() ui.Rect {
	left, right := shellContentColumn()
	return ui.Rect{X: left, Y: 0, W: right - left, H: shellMinimumHeight}
}

// TestBrowserTabStripFitsTheShellContentColumnAtTheMinimumWindow is the
// acceptance regression rendered in the real shell: at 704x421 with five
// tabs, each activated tab's whole item and close control stay inside the
// content column (248..688), and New tab stays inside it and clickable. It
// runs in English and zh-CN.
func TestBrowserTabStripFitsTheShellContentColumnAtTheMinimumWindow(t *testing.T) {
	left, right := shellContentColumn()
	column := shellColumnBox()
	for _, locale := range []string{"en", "zh-CN"} {
		t.Run(locale, func(t *testing.T) {
			h := newBrowserShellTester(t, locale, shellMinimumWidth, shellMinimumHeight)
			tabs := h.newTabTitles(t, 5)

			_, _, _, plus := h.shellTabBox(tabs[0].ID)
			if !boundsInside(plus, column) {
				t.Fatalf("New tab is outside the content column %.0f..%.0f with five tabs: %+v",
					left, right, plus)
			}
			t.Logf("%s: New tab at %.1f,%.1f %.0fx%.0f (column %.0f..%.0f)",
				locale, plus.X, plus.Y, plus.W, plus.H, left, right)

			for index, tab := range tabs {
				if !h.core.Activate(tab.ID) {
					t.Fatalf("Activate tab %d = false", index)
				}
				item, closing, view, _ := h.waitShellTabRevealed(t, tab.ID)
				if !boundsInside(item, column) {
					t.Fatalf("%s: active tab %d is outside the content column %.0f..%.0f: item %+v, view %+v",
						locale, index, left, right, item, view)
				}
				if !boundsInside(closing, column) {
					t.Fatalf("%s: tab %d's close control is outside the content column %.0f..%.0f: %+v",
						locale, index, left, right, closing)
				}
				t.Logf("%s: active tab %d item %.1f,%.1f %.0fx%.0f; close %.1f,%.1f %.0fx%.0f; view %.1f..%.1f",
					locale, index, item.X, item.Y, item.W, item.H,
					closing.X, closing.Y, closing.W, closing.H, view.X, view.X+view.W)
			}

			// Five fixed items overflow the 374-DIP viewport, so the last
			// activation above needed a real scroll.
			lastItem, _, view, _ := h.shellTabBox(tabs[len(tabs)-1].ID)
			if view.W >= lastItem.W*float32(len(tabs)) {
				t.Fatalf("%s: five items (%.0f each) fit the %.0f-DIP viewport; scrolling was not exercised",
					locale, lastItem.W, view.W)
			}

			// New tab is clickable with five tabs open and makes a sixth,
			// which is revealed and then closed by its close control.
			before := len(h.core.State().Tabs)
			h.tester.ClickAt(plus.X+plus.W/2, plus.Y+plus.H/2)
			h.tester.Frame()
			if got := len(h.core.State().Tabs); got != before+1 {
				t.Fatalf("%s: New tab made %d tabs, want %d", locale, got, before+1)
			}
			extra := h.core.State().ActiveID
			_, extraClose, _, _ := h.waitShellTabRevealed(t, extra)
			h.tester.ClickAt(extraClose.X+extraClose.W/2, extraClose.Y+extraClose.H/2)
			h.tester.Frame()
			if hasTab(h.core.State(), extra) {
				t.Fatalf("%s: clicking the extra tab's close control did not close it", locale)
			}

			// Each of the five original close controls is its hit target.
			for index, tab := range tabs {
				if hasTab(h.core.State(), tab.ID) {
					if !h.core.Activate(tab.ID) {
						t.Fatalf("%s: Activate tab %d before close = false", locale, index)
					}
				}
				_, closing, _, _ := h.waitShellTabRevealed(t, tab.ID)
				h.tester.ClickAt(closing.X+closing.W/2, closing.Y+closing.H/2)
				h.tester.Frame()
				if hasTab(h.core.State(), tab.ID) {
					t.Fatalf("%s: clicking tab %d's close control did not close it: %+v",
						locale, index, h.core.State().Tabs)
				}
			}
		})
	}
}

// TestBrowserTabStripRevealsTheActiveTabAfterTheShellNarrows activates the
// last of five tabs in a wide shell, then restores the minimum window: the
// active tab and its close control must be whole inside the content column,
// and New tab must stay inside it.
func TestBrowserTabStripRevealsTheActiveTabAfterTheShellNarrows(t *testing.T) {
	left, right := shellContentColumn()
	column := shellColumnBox()
	for _, locale := range []string{"en", "zh-CN"} {
		t.Run(locale, func(t *testing.T) {
			h := newBrowserShellTester(t, locale, 1100, 720)
			tabs := h.newTabTitles(t, 5)
			last := tabs[len(tabs)-1].ID
			if !h.core.Activate(last) {
				t.Fatalf("%s: Activate the last tab = false", locale)
			}
			h.waitShellTabRevealed(t, last)

			h.tester.SetSize(shellMinimumWidth, shellMinimumHeight)
			h.tester.Frame()
			item, closing, view, plus := h.waitShellTabRevealed(t, last)
			if !boundsInside(item, column) {
				t.Fatalf("%s: the active tab is cut after the shell narrowed: %+v", locale, item)
			}
			if !boundsInside(closing, column) {
				t.Fatalf("%s: the active tab's close control is cut after the shell narrowed: %+v",
					locale, closing)
			}
			if !boundsInside(plus, column) {
				t.Fatalf("%s: New tab is outside the column after the shell narrowed: %+v", locale, plus)
			}
			t.Logf("%s after 1100x720 -> 704x421: active item %.1f,%.1f %.0fx%.0f; close %.1f,%.1f %.0fx%.0f; view %.1f..%.1f; New tab %.1f,%.1f %.0fx%.0f (column %.0f..%.0f)",
				locale, item.X, item.Y, item.W, item.H,
				closing.X, closing.Y, closing.W, closing.H,
				view.X, view.X+view.W, plus.X, plus.Y, plus.W, plus.H, left, right)
		})
	}
}
