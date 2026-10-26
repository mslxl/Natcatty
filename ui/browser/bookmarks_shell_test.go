package browser

// This file is the long-bookmark-list regression rendered in the real shell.
// It is the defect independent acceptance at 2d02e84b found: the browser panel
// grew with its content instead of constraining the list to a scroll viewport,
// so a long list's last row stayed below the client at 704x421 and 1084x661
// however the user scrolled. The panel-level test in polish_test.go cannot see
// it: the tester's root element is given the window size directly, while in
// the shell the panel is a flex child of the Pane, whose automatic minimum
// height is its content's.

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/ui/locales"
)

const bookmarkShellRows = 14

// wholeLabel reports whether r shows its whole built height: a label clipped
// vertically by the scroll viewport keeps only the intersection with it, so a
// clipped label is shorter than its element (or has no size at all). Width is
// not compared because the first and last rows carry different text; the
// viewport is checked directly through the panel's bookmark viewport handle.
func wholeLabel(r, reference ui.Rect) bool {
	return r.W > 0 && r.H > 0 && r.H >= reference.H-0.5
}

// insideClient reports whether r lies wholly in the w×h client area.
func insideClient(r ui.Rect, width, height int) bool {
	return r.W > 0 && r.H > 0 &&
		r.X >= -0.5 && r.Y >= -0.5 &&
		r.X+r.W <= float32(width)+0.5 && r.Y+r.H <= float32(height)+0.5
}

func bookmarkTitle(index int) string { return fmt.Sprintf("Alpha scroll %02d", index) }
func bookmarkURL(index int) string   { return fmt.Sprintf("https://scroll.example/%02d", index) }

// addLongBookmarkList adds the acceptance's 14-row list through the
// production service the panel reads and returns the last row's ID.
func (h *browserShellHarness) addLongBookmarkList(t *testing.T) string {
	t.Helper()
	lastID := ""
	for i := 0; i < bookmarkShellRows; i++ {
		item, err := h.core.UpsertBookmark(domainbrowser.BookmarkInput{
			Title: bookmarkTitle(i),
			URL:   bookmarkURL(i),
			Scope: domainbrowser.ScopeHost,
		})
		if err != nil {
			t.Fatalf("add bookmark %d: %v", i, err)
		}
		lastID = item.ID
	}
	return lastID
}

// openBookmarkPane opens the pane from the panel's toolbar and waits for the
// rows to be built.
func (h *browserShellHarness) openBookmarkPane(t *testing.T, m locales.Messages) {
	t.Helper()
	if err := h.tester.Click(m.BrowserBookmarks()); err != nil {
		t.Fatalf("click Bookmarks: %v", err)
	}
	h.tester.Frame()
	if _, ok := h.tester.Find(m.BrowserBookmarkAdd()); !ok {
		t.Fatalf("bookmark pane did not open: %q", h.tester.Texts())
	}
	h.waitSettled(t, m, m.BrowserBookmarkAdd(), bookmarkTitle(0), bookmarkURL(0))
}

// scrollUntil runs one input per frame until done reports true, and fails
// with a hard deadline instead of waiting a fixed time.
func (h *browserShellHarness) scrollUntil(t *testing.T, what string, input func(), done func() bool, detail func() string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if done() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s", what, detail())
		}
		input()
		h.tester.Frame()
	}
}

// TestBookmarkPaneLongListStaysInsideTheShellAtBothSizes is the acceptance
// regression: at 704x421 and 1084x661, in English and zh-CN, the 14th row
// must scroll fully into view by wheel, by keyboard, and by focus reveal
// (ScrollIntoView), stay inside the client and below the pinned action bar,
// and remain clickable there.
func TestBookmarkPaneLongListStaysInsideTheShellAtBothSizes(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{"704x421", 704, 421},
		{"1084x661", 1084, 661},
	} {
		for _, locale := range []string{"en", "zh-CN"} {
			t.Run(locale+"-"+size.name, func(t *testing.T) {
				m := locales.In(locale)
				h := newBrowserShellTester(t, locale, size.width, size.height)
				lastID := h.addLongBookmarkList(t)
				h.tester.Frame()
				h.openBookmarkPane(t, m)

				client := ui.Rect{X: 0, Y: 0, W: float32(size.width), H: float32(size.height)}
				paneRegion, paneOK := h.tester.Find(m.PaneRegion(1))
				newTab, newTabOK := h.tester.Find(m.BrowserNewTab())
				address, addressOK := h.tester.Find(m.BrowserAddress())
				t.Logf("layout: pane=%+v (%v) newTab=%+v (%v) address=%+v (%v)",
					paneRegion, paneOK, newTab, newTabOK, address, addressOK)
				firstTitle, ok := h.tester.Find(bookmarkTitle(0))
				if !ok || firstTitle.H <= 0 {
					t.Fatalf("first bookmark missing from the pane: %q", h.tester.Texts())
				}
				firstURL, ok := h.tester.Find(bookmarkURL(0))
				if !ok || firstURL.H <= 0 {
					t.Fatalf("first bookmark address missing from the pane: %q", h.tester.Texts())
				}
				bar, ok := h.tester.Find(m.BrowserBookmarkAdd())
				if !ok {
					t.Fatalf("the pinned action bar is missing: %q", h.tester.Texts())
				}
				// A point over the list that is not a control: the row's
				// favicon glyph, left of its title and address column.
				listX := firstURL.X - 14
				listY := firstTitle.Y + firstTitle.H/2

				// The long list must genuinely overflow the viewport, or the
				// test proves nothing: initially the last row is not whole.
				lastTitle, _ := h.tester.Find(bookmarkTitle(bookmarkShellRows - 1))
				lastURL, _ := h.tester.Find(bookmarkURL(bookmarkShellRows - 1))
				t.Logf("%s %dx%d: first title %+v address %+v; last title %+v address %+v; bar %+v; point %.1f,%.1f",
					locale, size.width, size.height, firstTitle, firstURL, lastTitle, lastURL, bar, listX, listY)
				if wholeLabel(lastTitle, firstTitle) && wholeLabel(lastURL, firstURL) &&
					insideClient(lastTitle, size.width, size.height) {
					t.Fatalf("%s %dx%d: the %d-row list fits without scrolling; the test did not exercise it",
						locale, size.width, size.height, bookmarkShellRows)
				}

				assertBottom := func(how string, title, url ui.Rect) {
					t.Helper()
					if !wholeLabel(title, firstTitle) {
						t.Fatalf("%s %dx%d: after %s the last title is clipped: %+v (a whole one is %+v)",
							locale, size.width, size.height, how, title, firstTitle)
					}
					if !wholeLabel(url, firstURL) {
						t.Fatalf("%s %dx%d: after %s the last address is clipped: %+v (a whole one is %+v)",
							locale, size.width, size.height, how, url, firstURL)
					}
					for name, r := range map[string]ui.Rect{"title": title, "address": url} {
						if !insideClient(r, size.width, size.height) {
							t.Fatalf("%s %dx%d: after %s the last %s is outside the client %+v: %+v",
								locale, size.width, size.height, how, name, client, r)
						}
					}
					// The pinned bar stays put above the scroller; the row
					// must not run under it.
					if title.Y < bar.Y+bar.H-0.5 {
						t.Fatalf("%s %dx%d: after %s the last row runs under the pinned bar %+v: %+v",
							locale, size.width, size.height, how, bar, title)
					}
					// Measure the Scroll viewport itself, not only the rows it
					// clips: the acceptance defect was that this viewport was
					// unbounded and the panel grew with the list.
					viewport := h.panel.bookmarkViewport.Bounds(h.context)
					if viewport.W <= 0 || viewport.H <= 0 {
						t.Fatalf("%s %dx%d: the bookmark scroll viewport has no size: %+v",
							locale, size.width, size.height, viewport)
					}
					if !insideClient(viewport, size.width, size.height) {
						t.Fatalf("%s %dx%d: the bookmark scroll viewport is outside the client: %+v",
							locale, size.width, size.height, viewport)
					}
					if title.Y < viewport.Y-0.5 || title.Y+title.H > viewport.Y+viewport.H+0.5 ||
						url.Y < viewport.Y-0.5 || url.Y+url.H > viewport.Y+viewport.H+0.5 {
						t.Fatalf("%s %dx%d: after %s the last row is not inside the scroll viewport %+v: title %+v address %+v",
							locale, size.width, size.height, how, viewport, title, url)
					}
					t.Logf("%s %dx%d %s: last row title %+v, address %+v; viewport %+v; bar %+v; client bottom %.1f",
						locale, size.width, size.height, how, title, url, viewport, bar, client.Y+client.H)
				}

				// Wheel: one notch per frame, stopping on the real condition.
				current := func() string {
					title, _ := h.tester.Find(bookmarkTitle(bookmarkShellRows - 1))
					url, _ := h.tester.Find(bookmarkURL(bookmarkShellRows - 1))
					return fmt.Sprintf("last title %+v address %+v", title, url)
				}
				h.scrollUntil(t, "the wheel never brought the last row into view",
					func() { h.tester.Scroll(listX, listY, 0, 300) },
					func() bool {
						title, _ := h.tester.Find(bookmarkTitle(bookmarkShellRows - 1))
						url, _ := h.tester.Find(bookmarkURL(bookmarkShellRows - 1))
						return wholeLabel(title, firstTitle) && wholeLabel(url, firstURL) &&
							insideClient(title, size.width, size.height) && insideClient(url, size.width, size.height)
					}, current)
				lastTitle, _ = h.tester.Find(bookmarkTitle(bookmarkShellRows - 1))
				lastURL, _ = h.tester.Find(bookmarkURL(bookmarkShellRows - 1))
				assertBottom("the wheel", lastTitle, lastURL)

				// Scrolling moved the list: the first row is no longer whole.
				if firstTitle, ok := h.tester.Find(bookmarkTitle(0)); ok && insideClient(firstTitle, size.width, size.height) {
					t.Fatalf("%s %dx%d: the first row is still whole after scrolling to the last; the list did not move: %+v",
						locale, size.width, size.height, firstTitle)
				}

				// Clickable: the last row's Edit control opens that bookmark.
				// The address line spans the row's growing column, so its
				// right edge is where the fixed Edit button's gap starts.
				h.tester.ClickAt(lastURL.X+lastURL.W+40, lastURL.Y-5)
				h.tester.Frame()
				if got := h.panel.bookmarkForm.editingID; got != lastID {
					t.Fatalf("%s %dx%d: clicking the last row's Edit control edited %q, want %q",
						locale, size.width, size.height, got, lastID)
				}
				if err := h.tester.Click(m.BrowserBookmarkCancel()); err != nil {
					t.Fatalf("close the editor: %v", err)
				}
				h.tester.Frame()

				// Keyboard: End and Home scroll the hovered list to its ends.
				// The pointer is over the list and no text field has the
				// keyboard, so MyGo scrolls the container under it.
				h.tester.Move(listX, listY)
				h.tester.Key(0, ui.KeyEnd)
				h.scrollUntil(t, "End never brought the last row into view", func() {},
					func() bool {
						title, _ := h.tester.Find(bookmarkTitle(bookmarkShellRows - 1))
						url, _ := h.tester.Find(bookmarkURL(bookmarkShellRows - 1))
						return wholeLabel(title, firstTitle) && wholeLabel(url, firstURL)
					}, current)
				h.tester.Frame()
				lastTitle, _ = h.tester.Find(bookmarkTitle(bookmarkShellRows - 1))
				lastURL, _ = h.tester.Find(bookmarkURL(bookmarkShellRows - 1))
				assertBottom("End", lastTitle, lastURL)
				h.tester.Key(0, ui.KeyHome)
				h.scrollUntil(t, "Home never brought the first row back", func() {},
					func() bool {
						title, _ := h.tester.Find(bookmarkTitle(0))
						url, _ := h.tester.Find(bookmarkURL(0))
						return wholeLabel(title, firstTitle) && wholeLabel(url, firstURL)
					}, func() string {
						title, _ := h.tester.Find(bookmarkTitle(0))
						url, _ := h.tester.Find(bookmarkURL(0))
						return fmt.Sprintf("first title %+v address %+v", title, url)
					})

				// ScrollIntoView: tabbing into the list focuses each row's
				// controls in turn, and the row comes into view whole when
				// the focus enters it. Reaching the last row reveals it.
				h.tester.ClickAt(listX, listY)
				h.tester.Frame()
				revealed := false
				for i := 0; i < 600 && !revealed; i++ {
					h.tester.Key(0, ui.KeyTab)
					h.tester.Frame()
					title, _ := h.tester.Find(bookmarkTitle(bookmarkShellRows - 1))
					url, _ := h.tester.Find(bookmarkURL(bookmarkShellRows - 1))
					revealed = wholeLabel(title, firstTitle) && wholeLabel(url, firstURL) &&
						insideClient(title, size.width, size.height)
				}
				if !revealed {
					t.Fatalf("%s %dx%d: Tab focus never revealed the last row", locale, size.width, size.height)
				}
				if !h.tester.Focused(bookmarkTitle(bookmarkShellRows - 1)) {
					t.Fatalf("%s %dx%d: the last row became whole while its controls did not hold the focus", locale, size.width, size.height)
				}
				lastTitle, _ = h.tester.Find(bookmarkTitle(bookmarkShellRows - 1))
				lastURL, _ = h.tester.Find(bookmarkURL(bookmarkShellRows - 1))
				assertBottom("focus reveal", lastTitle, lastURL)

				// Delete from the loaded long list is a content change
				// inside the fixed viewport, not a pane resize.
				viewportBefore := h.panel.bookmarkViewport.Bounds(h.context)
				if removed, err := h.core.RemoveBookmark(lastID); err != nil || !removed {
					t.Fatalf("%s %dx%d: delete the last bookmark = %v, %v",
						locale, size.width, size.height, removed, err)
				}
				h.tester.Frame()
				viewportAfter := h.panel.bookmarkViewport.Bounds(h.context)
				if viewportAfter != viewportBefore {
					t.Fatalf("%s %dx%d: deleting the last row resized the scroll viewport %+v -> %+v",
						locale, size.width, size.height, viewportBefore, viewportAfter)
				}
			})
		}
	}
}
