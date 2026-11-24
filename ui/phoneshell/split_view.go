package phoneshell

// The tablet Workspace view: the same shared split tree the desktop writes,
// laid out in the phone Shell. A phone keeps the in-Tab Pane switcher; a
// tablet or an unfolded book-style phone shows every leaf of the tree at
// once, in the tree's own direction and proportions.
//
// The view is a pure function of the window and the Tab. One window size
// therefore moves between split and switcher without touching the Tabs, and a
// fold/unfold is just a size change. Only the focused Pane owns the Session's
// one live terminal view; the other tiles show their Pane's state and focus on
// a tap, which is what keeps every Pane running behind the one live view.

import (
	"time"

	"github.com/egoist/mygo/ui"

	domainmodel "github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// splitViewMinWidth and splitViewMinHeight are the room below which the phone
// keeps its in-Tab Pane switcher. Both must hold: a phone in landscape is wide
// but short, and stays on the switcher, while a tablet or an unfolded
// book-style phone has both dimensions.
const (
	splitViewMinWidth  float32 = 600
	splitViewMinHeight float32 = 600
)

// splitViewFits reports whether the window has room to lay out a Tab's panes
// side by side or stacked according to the shared split tree.
func splitViewFits(c *ui.Context) bool {
	width, height := c.Size()
	return width >= splitViewMinWidth && height >= splitViewMinHeight
}

// drawSplitView renders view.root recursively. Every leaf is a tile; every
// split is a draggable divider in the tree's direction and ratio.
func (t *sessionTabs) drawSplitView(c *ui.Context, view tabView, retry, back, addPane func()) {
	t.drawSplitNode(c, view, view.root, retry, back, addPane)
}

// drawSplitNode draws one node of the tree. A split's children share the
// room by its Ratio; a divider drag reports through Changed, and the new
// percentage is folded back into the shared tree.
func (t *sessionTabs) drawSplitNode(c *ui.Context, view tabView, n *domainworkspace.Node,
	retry, back, addPane func()) {

	if n == nil {
		return
	}
	if n.IsPane() {
		t.drawSplitPane(c, view, n.PaneID, retry, back, addPane)
		return
	}
	ratio := n.Ratio
	if ratio <= 0 || ratio >= 100 {
		ratio = domainworkspace.DefaultRatio
	}
	first := func() { t.drawSplitNode(c, view, n.First, retry, back, addPane) }
	second := func() { t.drawSplitNode(c, view, n.Second, retry, back, addPane) }
	// A horizontal split stacks its children top and bottom; a vertical one
	// puts them side by side, which is the shared Direction's meaning.
	var split ui.Element
	if n.Direction == domainworkspace.Horizontal {
		split = ui.SplitVerticalPercent(c, &ratio, first, second)
	} else {
		split = ui.SplitPercent(c, &ratio, first, second)
	}
	// Changed applies the divider's move in this build pass, so ratio already
	// holds the new percentage. Writing it back here is what makes the drag
	// survive a restore and reach the desktop. Fill is the desktop renderer's
	// rule too: the split, not its content, owns the page's room.
	if split.Changed() && ratio != n.Ratio {
		t.setSplitRatio(view.id, n.SplitID, ratio)
	}
	split.Fill()
}

// setSplitRatio writes a divider move back into the shared split tree and the
// persisted Workspace payload.
func (t *sessionTabs) setSplitRatio(tabID string, splitID domainmodel.SplitID, ratio float32) {
	changed := false
	t.mu.Lock()
	if tab := t.find(tabID); tab != nil && tab.root != nil {
		if root, ok := domainworkspace.WithSplitRatio(tab.root, splitID, ratio); ok {
			tab.root = root
			changed = true
		}
	}
	t.mu.Unlock()
	if changed {
		// The payload is the same shared Workspace the desktop reads, so the
		// new ratio is already on its way to both ends.
		t.persist()
	}
}

// drawSplitPane draws one leaf tile. The focused tile owns the live terminal;
// every other tile shows the Pane's identity and state and takes focus when
// tapped. A hidden Pane is hibernated, not closed: its transport keeps
// running and its output replays when it is focused again.
func (t *sessionTabs) drawSplitPane(c *ui.Context, view tabView, id domainmodel.PaneID,
	retry, back, addPane func()) {

	pane, ok := view.paneState(id)
	if !ok {
		return
	}
	if pane.id != view.focus {
		tile := ui.Column(c).Fill().Padding(12).Gap(8).
			Background(c.Theme().Surface).Label("Pane " + pane.label)
		tile.Children(func() {
			ui.Text(c, pane.label).Bold().SingleLine()
			splitPaneStatusLine(c, pane)
			hint := "Tap to focus. This Session keeps running."
			if pane.problem != "" {
				hint = pane.problem
			}
			ui.Text(c, hint).FontSize(12).TextColor(c.Theme().TextMuted)
		})
		if tile.Clicked() {
			t.focusPane(view.id, pane.id)
		}
		return
	}
	t.drawSplitFocusedPane(c, view, pane, retry, back, addPane)
}

// drawSplitFocusedPane draws the focused tile: its header and controls, then
// the state its Pane is in. It is the tablet counterpart of the single-Pane
// page's body.
func (t *sessionTabs) drawSplitFocusedPane(c *ui.Context, view tabView, pane paneView,
	retry, back, addPane func()) {

	ui.Column(c).Fill().Label("Pane " + pane.label).Children(func() {
		toolsOpen := view.tools != nil && view.tools.open
		header := ui.Row(c).FillWidth().Padding(6, 10).Gap(6).AlignItems(ui.Center).
			Background(c.Theme().Surface)
		header.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, pane.label).SingleLine().Bold()
				splitPaneStatusLine(c, pane)
			})
			toolsLabel := "Tools for " + pane.label
			if toolsOpen {
				toolsLabel = "Close tools for " + pane.label
			}
			if kit.IconButton(c, icons.Command, toolsLabel).Clicked() && view.tools != nil {
				if toolsOpen {
					t.closeTools(view.tools)
				} else {
					t.openTools(view.tools)
				}
			}
			appearanceLabel := "Terminal appearance for " + pane.label
			if view.appearanceOpen {
				appearanceLabel = "Close terminal appearance for " + pane.label
			}
			if kit.IconButton(c, icons.Palette, appearanceLabel).Clicked() {
				t.togglePaneAppearance(view.id, pane.id)
			}
			if kit.IconButton(c, icons.Plus, "Add pane to "+pane.label).Clicked() && addPane != nil {
				addPane()
			}
			if kit.IconButton(c, icons.X, "Close session "+pane.label).Clicked() {
				t.requestPaneClose(view.id, pane.id)
			}
		})
		switch pane.status {
		case tabConnecting:
			ui.Column(c).Fill().Padding(12).Gap(8).Children(func() {
				kit.EmptyHint(c, "Connecting to "+pane.label+"…")
				if t.prompts != nil {
					t.prompts.HostKey.drawHostKey(c, view.focus, back)
					t.prompts.Auth.drawAuth(c, view.focus, back)
				}
				c.After(15 * time.Millisecond)
			})
		case tabReconnecting, tabFailed:
			ui.Column(c).Fill().Padding(12).Gap(8).Children(func() {
				color := c.Theme().Warning
				if pane.status == tabFailed {
					color = c.Theme().Danger
				}
				kit.Notice(c, icons.TriangleAlert, color, pane.problem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Try again").Clicked() && retry != nil {
						retry()
					}
				})
			})
		case tabDormant:
			ui.Column(c).Fill().Padding(12).Gap(8).Children(func() {
				if pane.problem != "" {
					kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, pane.problem, func() {
						if kit.GhostButton(c, icons.RefreshCw, "Try again").Clicked() && retry != nil {
							retry()
						}
					})
					return
				}
				kit.EmptyHint(c, "Restoring "+pane.label+"…")
				c.After(15 * time.Millisecond)
			})
		default:
			if view.screen == nil {
				kit.EmptyHint(c, "The terminal is not ready.")
				return
			}
			if toolsOpen {
				t.drawTools(c, view)
				return
			}
			if view.appearanceOpen {
				t.drawQuickAppearance(c, view)
			}
			t.applyScreenMode(view.screen)
			accessoryBar(c, view.accessory, view.screen, t.pasteNeedsConfirmation)
			view.screen.View(c).Fill()
		}
	})
}

// paneState finds one Pane's state in a frame snapshot.
func (view tabView) paneState(id domainmodel.PaneID) (paneView, bool) {
	for _, pane := range view.panes {
		if pane.id == string(id) {
			return pane, true
		}
	}
	return paneView{}, false
}

// splitPaneStatusLine is one tile's lifecycle line, the same one a Sessions
// row shows but without the row's multi-Pane suffix.
func splitPaneStatusLine(c *ui.Context, pane paneView) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		kit.StatusDot(c, sessionStatusColor(c, pane.status))
		ui.Text(c, sessionStatusText(pane.status, pane.host)).FontSize(12).
			TextColor(c.Theme().TextMuted).SingleLine()
	})
}
