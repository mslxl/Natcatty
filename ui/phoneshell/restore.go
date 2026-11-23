package phoneshell

import (
	"strings"

	domainmodel "github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// RestoredPane is one restorable Pane of a persisted Tab. ID is the identity the
// shared split tree uses, Title is the label the phone shows until the Session
// reports its own, and Descriptor is the non-secret Session description the
// phone reopens.
type RestoredPane struct {
	ID         domainmodel.PaneID
	Title      string
	Descriptor domainworkspace.SessionDescriptor
}

// RestoredTab is one persisted Tab with its split tree intact. Root is the same
// recursive tree the desktop writes, Focus is the leaf the Tab shows on a
// phone, and Panes holds the descriptor for every leaf the phone can reopen.
type RestoredTab struct {
	ID             domainmodel.TabID
	Number         int
	TitleNumber    int
	Title          string
	AutoTitle      bool
	GeneratedTitle bool
	Copy           int
	Root           *domainworkspace.Node
	Focus          domainmodel.PaneID
	Panes          []RestoredPane
}

// RestoreTabs turns a shared Workspace payload into the phone's Tabs without
// flattening them. The split structure, ratios, Pane IDs, and focus path are
// kept exactly as the desktop wrote them; only Panes the phone cannot open are
// removed, collapsing the split around them. A Tab left with no restorable
// Pane is omitted.
//
// A phone's older single-Pane record is a subset of this shape: its one leaf
// and one descriptor restore directly, with no migration step.
func RestoreTabs(payload domainworkspace.Payload) []RestoredTab {
	payload = domainworkspace.SanitizePayload(payload)
	descriptors := make(map[domainmodel.PaneID]domainworkspace.SessionDescriptor, len(payload.Panes))
	for _, pane := range payload.Panes {
		descriptors[pane.ID] = pane.Session
	}
	tabs := make([]RestoredTab, 0, len(payload.Tabs))
	for _, tab := range payload.Tabs {
		root := restorableRoot(tab.Root, descriptors)
		if root == nil {
			continue
		}
		focusPath := domainworkspace.SanitizeFocusPath(root, tab.FocusPath)
		focus := domainmodel.PaneID("")
		if leaf, ok := domainworkspace.PaneAtPath(root, focusPath); ok && leaf != nil {
			focus = leaf.PaneID
		}
		panes := make([]RestoredPane, 0, len(payload.Panes))
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			descriptor := descriptors[id]
			panes = append(panes, RestoredPane{
				ID:         id,
				Title:      restoredTitle(descriptor),
				Descriptor: descriptor,
			})
		}
		tabs = append(tabs, RestoredTab{
			ID:             tab.ID,
			Number:         tab.Number,
			TitleNumber:    tab.TitleNumber,
			Title:          strings.TrimSpace(tab.Title),
			AutoTitle:      tab.AutoTitle,
			GeneratedTitle: tab.GeneratedTitle,
			Copy:           tab.Copy,
			Root:           root,
			Focus:          focus,
			Panes:          panes,
		})
	}
	return tabs
}

// restorableRoot copies a split tree, dropping leaves the phone cannot reopen.
// A split with one surviving side collapses into that side, so the remaining
// Panes keep the ratios and orientation the desktop wrote around them.
func restorableRoot(n *domainworkspace.Node, descriptors map[domainmodel.PaneID]domainworkspace.SessionDescriptor) *domainworkspace.Node {
	if n == nil {
		return nil
	}
	if n.Type == domainworkspace.PaneNode || (n.Type == "" && n.PaneID != "") {
		descriptor, ok := descriptors[n.PaneID]
		if !ok || !phoneCanRestore(descriptor) {
			return nil
		}
		return domainworkspace.NewPaneNode(n.PaneID)
	}
	if n.Type != domainworkspace.SplitNode || !n.Direction.Valid() {
		return nil
	}
	first := restorableRoot(n.First, descriptors)
	second := restorableRoot(n.Second, descriptors)
	switch {
	case first == nil && second == nil:
		return nil
	case first == nil:
		return second
	case second == nil:
		return first
	default:
		return domainworkspace.NewSplitNode(n.SplitID, n.Direction, n.Ratio, first, second)
	}
}

// phoneCanRestore reports whether a descriptor names a Session the phone can
// open. A Local shell would run on the device and a serial device has no phone
// adapter; both stay on the desktop.
func phoneCanRestore(descriptor domainworkspace.SessionDescriptor) bool {
	if !descriptor.Valid() {
		return false
	}
	switch descriptor.Kind {
	case domainworkspace.SessionSSH, domainworkspace.SessionTelnet,
		domainworkspace.SessionMosh, domainworkspace.SessionET:
		return true
	default:
		return false
	}
}

// restoredTitle names one Pane from its own descriptor. A split-era Tab title
// describes several panes ("web/db"), which would mislabel each Pane, so it is
// never used; the Pane's name or Host does that instead.
func restoredTitle(descriptor domainworkspace.SessionDescriptor) string {
	for _, candidate := range []string{descriptor.CustomName, descriptor.Label, descriptor.Hostname} {
		if title := strings.TrimSpace(candidate); title != "" {
			return title
		}
	}
	return "Session"
}
