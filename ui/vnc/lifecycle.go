package vnc

import (
	"strings"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// SetPaneActions receives the Pane commands the shell supplies for this Pane.
// It is the same seam the terminal surface uses, so split, detach, rename,
// close, and manual reconnect behave identically for a VNC Pane.
func (s *Surface) SetPaneActions(actions sessionui.PaneActions) {
	s.paneActions = actions
}

// ReleaseInput lets go of every key, synthesized modifier, and pointer button
// the guest still believes down. The shell calls it when the Pane stops
// rendering, which MyGo reports with no key-up event of its own.
func (s *Surface) ReleaseInput() { s.releaseHeldKeys() }

// Hibernated reports whether the desktop is replaced by the resumable
// placeholder. The last frame is retained in memory while hibernated.
func (s *Surface) Hibernated() bool {
	s.mu.Lock()
	local := s.hibernated
	s.mu.Unlock()
	if local {
		return true
	}
	return s.session != nil && s.session.Snapshot().Hibernated
}

// Hibernate releases the rendered desktop while keeping the Session, its
// transport, and the last frame. The viewer keeps draining display events so
// a reconnect cannot stall, but only stores the newest snapshot: no bitmap is
// drawn until Resume. It reports false for an already hibernated or stopped
// Session.
func (s *Surface) Hibernate() bool {
	if s.session == nil {
		return false
	}
	s.mu.Lock()
	already := s.hibernated
	s.mu.Unlock()
	if already {
		return false
	}
	snapshot := s.session.Snapshot()
	if snapshot.Status != appsession.StatusRunning {
		return false
	}
	// Hibernating is another way the live Pane stops rendering.
	s.releaseHeldKeys()
	if !snapshot.Hibernated && !s.session.Hibernate() {
		return false
	}
	s.mu.Lock()
	s.hibernated = true
	// Free the drawn bitmaps; pixels is the last frame and is deliberately
	// retained so Resume can show it before any new update arrives.
	s.bitmap = nil
	s.scaled = nil
	s.scaledPix = nil
	s.mu.Unlock()
	s.requestRedraw()
	return true
}

// Resume clears the hibernation and draws the last frame again. The display
// session never left the Session, so the transport is still the same one; a
// full framebuffer update catches anything the retained frame missed.
func (s *Surface) Resume() error {
	if s.session == nil {
		return appsession.ErrNotHibernated
	}
	snapshot := s.session.Snapshot()
	s.mu.Lock()
	local := s.hibernated
	s.mu.Unlock()
	if !local && !snapshot.Hibernated {
		return appsession.ErrNotHibernated
	}
	if snapshot.Hibernated {
		// Resume attaches the Session's one byte view. A pixel Session never
		// reads that view, so close it immediately to detach it again; only
		// the Hibernated flag and the repaint are wanted here.
		resumed, err := s.session.Resume()
		if err != nil {
			return err
		}
		if resumed != nil {
			_ = resumed.Close()
		}
	}
	s.mu.Lock()
	s.hibernated = false
	s.mu.Unlock()
	s.applyPendingFrame()
	s.mu.Lock()
	if s.bitmap == nil {
		s.rebuildBitmapLocked()
	}
	s.mu.Unlock()
	_ = s.session.RequestUpdate(false)
	s.requestRedraw()
	return nil
}

// rebuildBitmapLocked recreates the drawn bitmap from the retained frame. The
// caller holds s.mu.
func (s *Surface) rebuildBitmapLocked() {
	if s.pixels == nil {
		return
	}
	s.bitmap = ui.NewBitmap(s.pixels).Pixelated()
	s.bitmap.Update(s.pixels)
	s.scaled = nil
	s.scaledPix = nil
}

// dropFocus releases held input and forgets the Pane's focus before a
// non-desktop state renders. Resume or a fresh connection re-requests focus
// through the ordinary ViewFocused path.
func (s *Surface) dropFocus() {
	if s.focused || s.heldMods != 0 || len(s.heldKeys) > 0 || s.downMask != 0 {
		s.releaseHeldKeys()
	}
	s.focused = false
	s.focusPending = false
	s.windowFocused = false
}

// connectionFailed reports a desktop that cannot continue without a manual
// reconnect. A permanent authentication failure ends the Session with this
// state, as does any explicit close of the stream.
func connectionFailed(snapshot appsession.Snapshot) bool {
	return snapshot.Status == appsession.StatusExited ||
		snapshot.Connection == appsession.ConnectionStatusError
}

// Toolbar renders the VNC Pane's controls for its header: the per-kind viewer
// controls, the two splits, and the Session menu. It is the host-window form,
// so it includes the fullscreen action. The menu carries the lifecycle actions
// the terminal's menu offers, without the byte-stream-only items.
func (s *Surface) Toolbar(c *ui.Context) {
	s.toolbar(c, true)
}

// ToolbarEmbedded renders the same controls for a renderer that is not a host
// window: the Dropdown Panel in Quake's own mode and the top-down and follow
// routes. It omits fullscreen, which would otherwise target the main window or
// no-op on a non-activating panel. Every other control is unchanged.
func (s *Surface) ToolbarEmbedded(c *ui.Context) {
	s.toolbar(c, false)
}

func (s *Surface) toolbar(c *ui.Context, hostWindow bool) {
	s.locale = c.Locale()
	m := locales.In(c.Locale())
	snapshot := s.session.Snapshot()
	s.vncControls(c, m, hostWindow)
	t := c.Theme()
	s.notices(c, t)
	if s.paneActions.SplitRight != nil &&
		kit.IconButtonWith(c, icons.Columns, m.PaneSplitRight(), kit.IconOptions{Role: kit.IconPane}).Clicked() {
		s.paneActions.SplitRight()
	}
	if s.paneActions.SplitDown != nil &&
		kit.IconButtonWith(c, icons.Rows, m.PaneSplitDown(), kit.IconOptions{Role: kit.IconPane}).Clicked() {
		s.paneActions.SplitDown()
	}
	menuLabel := s.paneActions.MenuLabel
	if menuLabel == "" {
		menuLabel = m.SessionMenuLabel()
	}
	kit.IconButtonWith(c, icons.EllipsisVertical, menuLabel, kit.IconOptions{Role: kit.IconPane}).Menu(func(menu *ui.Menu) {
		reconnect := menu.Item(m.SessionMenuReconnect()).Disabled(!s.reconnectAvailable(snapshot))
		if reconnect.Chosen() {
			s.reconnectNow()
		}

		menu.Separator()
		splitHorizontal := menu.Item(m.SessionMenuSplitHorizontal()).Disabled(s.paneActions.SplitDown == nil)
		if splitHorizontal.Chosen() && s.paneActions.SplitDown != nil {
			s.paneActions.SplitDown()
		}
		splitVertical := menu.Item(m.SessionMenuSplitVertical()).Disabled(s.paneActions.SplitRight == nil)
		if splitVertical.Chosen() && s.paneActions.SplitRight != nil {
			s.paneActions.SplitRight()
		}

		menu.Separator()
		lifetimeLabel := m.SessionMenuHibernate()
		var lifetime func()
		lifetime = func() { s.Hibernate() }
		canHibernate := snapshot.Status == appsession.StatusRunning && !s.Hibernated()
		if s.Hibernated() {
			lifetimeLabel = m.SessionResume()
			lifetime = func() { _ = s.Resume() }
			canHibernate = snapshot.Status == appsession.StatusRunning
		}
		if menu.Item(lifetimeLabel).Disabled(!canHibernate).Chosen() {
			lifetime()
			c.Invalidate()
		}

		menu.Separator()
		rename := menu.Item(m.SessionMenuRename()).Disabled(s.paneActions.Rename == nil)
		if rename.Chosen() && s.paneActions.Rename != nil {
			s.beginRename()
		}

		menu.Separator()
		detach := menu.Item(m.SessionMenuDetach()).Disabled(s.paneActions.Detach == nil)
		if detach.Chosen() && s.paneActions.Detach != nil {
			s.paneActions.Detach()
			c.Invalidate()
		}

		menu.Separator()
		closePane := menu.Item(m.SessionMenuClosePane()).Disabled(s.paneActions.Close == nil)
		if closePane.Chosen() && s.paneActions.Close != nil {
			s.paneActions.Close()
			c.Invalidate()
		}
	})
}

// vncControls is the viewer's per-kind chrome: the scale cycle, view-only,
// clipboard send and pull, Ctrl-Alt-Del, fullscreen, and keyboard grab. It is
// deliberately separate from the terminal pane's toolbar, whose capture,
// broadcast, compose, and terminal-resize controls cannot work on pixels.
// hostWindow gates fullscreen, so a non-activating Panel never offers a
// control that cannot affect its own window.
func (s *Surface) vncControls(c *ui.Context, m locales.Messages, hostWindow bool) {
	scaleLabel := m.SessionVNCScaleFit()
	switch s.scaleMode {
	case modelvnc.OneToOne:
		scaleLabel = m.SessionVNCScaleOneToOne()
	case modelvnc.Clip:
		scaleLabel = m.SessionVNCScaleClip()
	}
	if kit.IconButtonWith(c, icons.SlidersHorizontal, scaleLabel,
		kit.IconOptions{Role: kit.IconPane}).Clicked() {
		s.CycleScale()
		c.Invalidate()
	}
	if kit.IconButtonWith(c, icons.Eye, m.SessionVNCViewOnly(),
		kit.IconOptions{Role: kit.IconPane, On: s.viewOnly}).Clicked() {
		s.SetViewOnly(!s.viewOnly)
		c.Invalidate()
	}
	if kit.IconButtonWith(c, icons.Upload, m.SessionVNCClipboardSend(),
		kit.IconOptions{Role: kit.IconPane}).Clicked() {
		s.SendClipboard(c.ReadClipboard())
		c.Invalidate()
	}
	if kit.IconButtonWith(c, icons.Download, m.SessionVNCClipboardPull(),
		kit.IconOptions{Role: kit.IconPane}).Clicked() {
		s.PullClipboard(c.WriteClipboard)
		c.Invalidate()
	}
	if kit.IconButtonWith(c, icons.Power, m.SessionVNCCtrlAltDel(),
		kit.IconOptions{Role: kit.IconPane}).Clicked() {
		// View-only must not silently ignore the control: the clipboard send
		// button reports the same mode through SendClipboard's result.
		if s.viewOnly {
			s.setViewOnlyNotice(m.SessionVNCViewOnlyAction())
		} else {
			_ = s.SendCtrlAltDel()
		}
		c.Invalidate()
	}
	if hostWindow {
		if kit.IconButtonWith(c, icons.Maximize, m.SessionVNCFullscreen(),
			kit.IconOptions{Role: kit.IconPane}).Clicked() {
			s.ToggleFullscreen()
			c.Invalidate()
		}
	}
	if kit.IconButtonWith(c, icons.Keyboard, m.SessionVNCKeyboardGrab(),
		kit.IconOptions{Role: kit.IconPane, On: s.keyboardGrab}).Clicked() {
		s.SetKeyboardGrab(!s.keyboardGrab)
		c.Invalidate()
	}
}

// reconnectAvailable reports whether the Session menu offers Reconnect. A
// reconnecting Session is already retrying on its own; everything else with a
// live or stopped Session can be retried by hand.
func (s *Surface) reconnectAvailable(snapshot appsession.Snapshot) bool {
	if snapshot.Status == appsession.StatusRunning &&
		snapshot.Connection == appsession.ConnectionStatusReconnecting {
		return false
	}
	if s.session != nil && s.session.CanReconnect() {
		return true
	}
	return s.paneActions.Reconnect != nil
}

// reconnectNow retries immediately. A running Session uses its own reconnect
// command, so the policy delay is skipped. A stopped Session cannot do that:
// the shell reopens it from the Pane's descriptor instead.
func (s *Surface) reconnectNow() {
	if s.session != nil && s.session.CanReconnect() && s.session.ReconnectNow() {
		s.requestRedraw()
		return
	}
	if s.paneActions.Reconnect != nil {
		s.paneActions.Reconnect()
	}
}

// beginRename opens the Pane rename dialog from the shell-supplied current
// title, matching the terminal surface.
func (s *Surface) beginRename() {
	s.renameValue = s.paneActions.Title
	s.renaming = true
	s.requestRedraw()
}

// applyRename hands the trimmed, non-empty title to the shell that owns the
// Tab. It reports whether a rename happened.
func (s *Surface) applyRename() bool {
	title := strings.TrimSpace(s.renameValue)
	if title == "" || s.paneActions.Rename == nil {
		return false
	}
	s.paneActions.Rename(title)
	return true
}

// renameDialog collects a Pane title. It mirrors the terminal surface's
// dialog so the two Pane kinds behave the same.
func (s *Surface) renameDialog(c *ui.Context, m locales.Messages, t *ui.Theme) {
	kit.DialogWith(c, &s.renaming, kit.DialogOptions{Role: kit.DialogSheet}, func() {
		ui.Column(c).Width(320).Padding(14).Gap(10).Background(t.Surface).
			Radius(t.Radius).Border(1, t.Border).Children(func() {
			ui.Text(c, m.SessionMenuRenameTitle()).FontSize(kit.M(c).Font.Callout).Bold().TextColor(t.Text)
			input := ui.TextInput(c, &s.renameValue)
			input.Label(m.SessionRenameField())
			submitted := input.Submitted()
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1)
				confirm := ui.PrimaryButton(c, m.SessionMenuRenameConfirm())
				if (confirm.Clicked() || submitted) && s.applyRename() {
					s.renaming = false
					s.requestRedraw()
				}
				if ui.Button(c, m.SessionPasteCancel()).Clicked() {
					s.renaming = false
					s.requestRedraw()
				}
			})
		})
	}, nil)
}

// connectionOverlay floats the reconnecting state over the last frame. It is
// an overlay, so showing it never changes the pane size or asks the server for
// a desktop resize. A live Pane shows its state in the header badge instead.
// There is no Connecting case: a Session publishes Active only once its dial
// has succeeded, and the shell shows its own pending badge while the Pane
// opens, so this Surface never observes ConnectionStatusConnecting.
func (s *Surface) connectionOverlay(c *ui.Context, m locales.Messages, t *ui.Theme,
	snapshot appsession.Snapshot, anchor ui.Element) {
	if snapshot.Status != appsession.StatusRunning {
		return
	}
	var text string
	switch snapshot.Connection {
	case appsession.ConnectionStatusReconnecting:
		text = m.ConnectionStatusReconnecting()
	default:
		return
	}
	ui.Overlay(c, func() {
		kit.Badge(c, t.Warning, text).AttachTo(anchor, ui.AnchorTopLeft, ui.AnchorTopLeft).Margin(8, 0, 0, 8)
	})
}

// failedView replaces the dead desktop with the reason and the manual
// reconnect action. An authentication rejection is named explicitly; every
// other permanent stop keeps the generic connection failure title.
func (s *Surface) failedView(c *ui.Context, m locales.Messages,
	snapshot appsession.Snapshot) ui.Element {
	title := m.SessionVNCConnectionFailed()
	if snapshot.EndReason == appsession.EndReasonAuthFailed {
		title = m.ConnectionErrorAuth()
	}
	var action func()
	if s.reconnectAvailable(snapshot) {
		action = func() {
			if kit.PrimaryButton(c, icons.RefreshCw, m.SessionMenuReconnect()).Clicked() {
				s.reconnectNow()
				s.requestRedraw()
			}
		}
	}
	return ui.Column(c).Fill().Justify(ui.Center).Children(func() {
		kit.EmptyState(c, icons.TriangleAlert, title, m.SessionVNCReconnectHint(), action)
	})
}

// hibernatedView replaces the live desktop with the resumable placeholder. The
// Session and transport stay connected, and the last frame returns on Resume.
func (s *Surface) hibernatedView(c *ui.Context, m locales.Messages) ui.Element {
	return ui.Column(c).Fill().Justify(ui.Center).Children(func() {
		kit.EmptyState(c, icons.Moon, m.SessionHibernatedTitle(), m.SessionVNCHibernatedBody(), func() {
			if ui.PrimaryButton(c, m.SessionResume()).Clicked() {
				_ = s.Resume()
				s.requestRedraw()
			}
		})
	})
}
