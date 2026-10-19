package workspace

import (
	"fmt"
	"strings"

	"github.com/mslxl/natcatty/domain/model"
)

// PayloadVersion is the restore payload version this app writes. Older or
// unknown versions are discarded rather than guessed at.
const PayloadVersion = 1

// SessionKind is the transport family a restored pane needs.
type SessionKind string

const (
	// SessionLocal is a shell on this machine.
	SessionLocal SessionKind = "local"
	// SessionSSH is a shell on a saved Host.
	SessionSSH SessionKind = "ssh"
)

// SessionDescriptor is the non-secret description of a Pane's Session. It is
// what restore needs to reopen the Session: no output, scrollback, or
// credential material is stored.
type SessionDescriptor struct {
	Kind SessionKind `json:"kind"`
	// Number is the stable local-session number used in auto titles.
	Number   int          `json:"number,omitempty"`
	Label    string       `json:"label,omitempty"`
	HostID   model.HostID `json:"hostId,omitempty"`
	Hostname string       `json:"hostname,omitempty"`
	Port     int          `json:"port,omitempty"`
	Username string       `json:"username,omitempty"`
	// Dir is the working directory to reopen in. For local Sessions it is the
	// start directory; for SSH it is the last known directory.
	Dir string `json:"dir,omitempty"`
	// X11Forwarding records the Host preference captured when the pane opened.
	X11Forwarding bool `json:"x11Forwarding,omitempty"`
}

// Valid reports whether a descriptor can be restored.
func (d SessionDescriptor) Valid() bool {
	switch d.Kind {
	case SessionLocal:
		return true
	case SessionSSH:
		return d.HostID != "" && d.Hostname != ""
	default:
		return false
	}
}

// RestoredPane pairs a Pane id with its Session descriptor.
type RestoredPane struct {
	ID      model.PaneID      `json:"id"`
	Session SessionDescriptor `json:"session"`
}

// RestoredTab is one ordered Tab in a restore payload.
type RestoredTab struct {
	ID     model.TabID `json:"id"`
	Number int         `json:"number,omitempty"`
	// TitleNumber is the number an auto title shows. A duplicate keeps the
	// source's number while getting its own stable Tab Number.
	TitleNumber int    `json:"titleNumber,omitempty"`
	Title       string `json:"title,omitempty"`
	AutoTitle   bool   `json:"autoTitle,omitempty"`
	// Copy counts duplicate generations: 0 is the original, 1 the first copy.
	Copy      int   `json:"copy,omitempty"`
	Root      *Node `json:"root"`
	FocusPath []int `json:"focusPath,omitempty"`
}

// Payload is the whole workspace restore payload. Scrollback is deliberately
// absent: only layout, Session descriptors, and the last working directory
// survive a restart.
type Payload struct {
	Version   int            `json:"version"`
	ActiveTab int            `json:"activeTab"`
	Tabs      []RestoredTab  `json:"tabs"`
	Panes     []RestoredPane `json:"panes"`
}

// EmptyPayload is a workspace with no Tabs and no active Tab.
func EmptyPayload() Payload {
	return Payload{Version: PayloadVersion, ActiveTab: -1}
}

// SanitizePayload drops everything unusable and repairs structural mistakes,
// so a corrupt row can never prevent the app from starting. Unknown versions
// restore an empty Workspace.
func SanitizePayload(payload Payload) Payload {
	out := EmptyPayload()
	if payload.Version != PayloadVersion {
		return out
	}

	descriptors := make(map[model.PaneID]SessionDescriptor)
	for _, pane := range payload.Panes {
		if pane.ID == "" || !pane.Session.Valid() {
			continue
		}
		if _, exists := descriptors[pane.ID]; exists {
			continue
		}
		descriptors[pane.ID] = pane.Session
	}

	usedPanes := make(map[model.PaneID]bool)
	usedTabs := make(map[model.TabID]bool)
	usedSplits := make(map[model.SplitID]bool)
	splitSequence := 0

	activeIndex := -1
	for index, tab := range payload.Tabs {
		if tab.ID == "" || usedTabs[tab.ID] {
			continue
		}
		root, ok := sanitizeNode(tab.Root, descriptors, usedPanes, usedSplits, &splitSequence)
		if !ok {
			continue
		}
		usedTabs[tab.ID] = true
		if index == payload.ActiveTab {
			activeIndex = len(out.Tabs)
		}
		out.Tabs = append(out.Tabs, RestoredTab{
			ID:          tab.ID,
			Number:      tab.Number,
			TitleNumber: tab.TitleNumber,
			Title:       strings.TrimSpace(tab.Title),
			AutoTitle:   tab.AutoTitle,
			Copy:        max(tab.Copy, 0),
			Root:        root,
			FocusPath:   SanitizeFocusPath(root, tab.FocusPath),
		})
	}

	for _, pane := range payload.Panes {
		if usedPanes[pane.ID] {
			out.Panes = append(out.Panes, pane)
		}
	}

	if len(out.Tabs) == 0 {
		return out
	}
	switch {
	case activeIndex >= 0:
		// Keep the active Tab when earlier Tabs were dropped.
		out.ActiveTab = activeIndex
	case payload.ActiveTab < 0:
		out.ActiveTab = 0
	case payload.ActiveTab >= len(out.Tabs):
		out.ActiveTab = len(out.Tabs) - 1
	default:
		// The active Tab itself was dropped.
		out.ActiveTab = 0
	}
	return out
}

func sanitizeNode(
	n *Node,
	descriptors map[model.PaneID]SessionDescriptor,
	usedPanes map[model.PaneID]bool,
	usedSplits map[model.SplitID]bool,
	splitSequence *int,
) (*Node, bool) {
	if n == nil {
		return nil, false
	}
	if n.Type == PaneNode || (n.Type == "" && n.PaneID != "") {
		if n.PaneID == "" || usedPanes[n.PaneID] {
			return nil, false
		}
		if _, ok := descriptors[n.PaneID]; !ok {
			return nil, false
		}
		usedPanes[n.PaneID] = true
		return NewPaneNode(n.PaneID), true
	}
	if n.Type != SplitNode || !n.Direction.Valid() {
		return nil, false
	}

	first, okFirst := sanitizeNode(n.First, descriptors, usedPanes, usedSplits, splitSequence)
	second, okSecond := sanitizeNode(n.Second, descriptors, usedPanes, usedSplits, splitSequence)
	if !okFirst && !okSecond {
		return nil, false
	}
	if okFirst != okSecond {
		if okFirst {
			return first, true
		}
		return second, true
	}

	id := n.SplitID
	if id == "" || usedSplits[id] {
		*splitSequence++
		id = model.SplitID(fmt.Sprintf("restored-split-%d", *splitSequence))
	}
	usedSplits[id] = true
	ratio := n.Ratio
	if ratio <= 0 || ratio >= 100 {
		ratio = DefaultRatio
	}
	return NewSplitNode(id, n.Direction, ratio, first, second), true
}

// FocusPath returns the path of focused, or the first leaf's path when the
// focus is absent. A root pane has an empty path.
func FocusPath(root *Node, focused model.PaneID) []int {
	if path := PathTo(root, focused); path != nil {
		return path
	}
	ids := CollectPaneIDs(root)
	if len(ids) == 0 {
		return nil
	}
	return PathTo(root, ids[0])
}

// SanitizeFocusPath keeps a path that resolves to a leaf and replaces
// anything else with the first leaf.
func SanitizeFocusPath(root *Node, path []int) []int {
	if node, ok := PaneAtPath(root, path); ok && node != nil {
		return append([]int{}, path...)
	}
	return FocusPath(root, "")
}
