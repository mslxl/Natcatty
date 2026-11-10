package workspace

import (
	"fmt"
	"strings"

	"github.com/mslxl/natcatty/domain/model"
	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
	"github.com/mslxl/natcatty/domain/model/serial"
)

// PayloadVersion is the restore payload version this app writes. Version 1
// payloads remain readable: they predate plugin-backed Sessions, whose
// descriptors add a provider, a protocol, and an opaque restore descriptor.
const PayloadVersion = 2

// readablePayloadVersion reports the payload versions SanitizePayload can
// repair without guessing. A writer always emits PayloadVersion.
func readablePayloadVersion(version int) bool {
	return version == 1 || version == PayloadVersion
}

// MaxSessionRestoreBytes bounds the opaque state one plugin-backed Session may
// contribute to the restore payload. Oversized state is dropped, not the Pane.
const MaxSessionRestoreBytes = 64 << 10

// SessionKind is the transport family a restored pane needs.
type SessionKind string

const (
	// SessionLocal is a shell on this machine.
	SessionLocal SessionKind = "local"
	// SessionSSH is a shell on a saved Host.
	SessionSSH SessionKind = "ssh"
	// SessionSerial is a device on a serial port.
	SessionSerial SessionKind = "serial"
	// SessionTelnet is a terminal on a saved Telnet Host.
	SessionTelnet SessionKind = "telnet"
	// SessionMosh is a Mosh Session on a saved Host.
	SessionMosh SessionKind = "mosh"
	// SessionET is an EternalTerminal Session on a saved Host.
	SessionET SessionKind = "et"
	// SessionPlugin is a Session whose bytes come from a connection provider.
	// It is distinct from every built-in kind so a missing provider can never
	// be mistaken for another transport.
	SessionPlugin SessionKind = "plugin"
)

// SessionDescriptor is the non-secret description of a Pane's Session. It is
// what restore needs to reopen the Session: no output, scrollback, or
// credential material is stored.
type SessionDescriptor struct {
	Kind SessionKind `json:"kind"`
	// Number is the stable local-session number used in auto titles.
	Number int    `json:"number,omitempty"`
	Label  string `json:"label,omitempty"`
	// CustomName is the user's rename of this Pane's Session. It wins over the
	// Host label and the shell-reported title, and survives a restore.
	CustomName string       `json:"customName,omitempty"`
	HostID     model.HostID `json:"hostId,omitempty"`
	Hostname   string       `json:"hostname,omitempty"`
	Port       int          `json:"port,omitempty"`
	Username   string       `json:"username,omitempty"`
	// Dir is the working directory to reopen in. For local Sessions it is the
	// start directory; for SSH it is the last known directory.
	Dir string `json:"dir,omitempty"`
	// X11Forwarding records the Host preference captured when the pane opened.
	X11Forwarding bool `json:"x11Forwarding,omitempty"`
	// AutoAttachTmux records the SSH Host preference captured when the pane
	// opened, so a restored Session reattaches the same tmux session when the
	// Host record is unavailable.
	AutoAttachTmux bool `json:"autoAttachTmux,omitempty"`
	// Serial is the port and framing a serial Pane reopens with. It carries no
	// credential material.
	Serial *serial.Config `json:"serial,omitempty"`
	// ResumeKey locates an EternalTerminal Pane's saved session credentials in
	// the encrypted secret store. It is an opaque locator, never the ET
	// session id or passkey; those stay only in the store.
	ResumeKey string `json:"resumeKey,omitempty"`
	// Provider is the plugin ID that owned the protocol when the Pane was
	// saved. It is informational; availability is decided by resolving
	// Protocol in the provider registry at restore time.
	Provider string `json:"provider,omitempty"`
	// Protocol is the provider-owned x-<publisher>-<name> Host protocol.
	Protocol string `json:"protocol,omitempty"`
	// Restore is the provider's bounded opaque restore descriptor. It is
	// base64 in JSON, carries no secrets by contract, and never captures
	// terminal state.
	Restore []byte `json:"restore,omitempty"`
}

// Valid reports whether a descriptor can be restored.
func (d SessionDescriptor) Valid() bool {
	switch d.Kind {
	case SessionLocal:
		return true
	case SessionSSH, SessionTelnet, SessionMosh, SessionET:
		return d.HostID != "" && d.Hostname != ""
	case SessionSerial:
		return d.Serial != nil && d.Serial.Normalized().Validate() == nil
	case SessionPlugin:
		return d.HostID != "" && d.Hostname != "" &&
			protocolmodel.ValidProviderID(protocolmodel.ID(d.Protocol)) &&
			len(d.Restore) <= MaxSessionRestoreBytes
	default:
		return false
	}
}

// PaneKind is the content kind a restored Pane holds. Only PaneKindSession is
// openable in this release; PaneKindView reserves the descriptor shape for
// plugin Views.
type PaneKind string

const (
	// PaneKindSession is a Pane that opens a terminal Session.
	PaneKindSession PaneKind = "session"
	// PaneKindView is a Pane occupied by a plugin View.
	PaneKindView PaneKind = "view"
)

// MaxViewStateBytes bounds the opaque state one View Pane may contribute to
// the restore payload. Oversized state is dropped, not the Pane.
const MaxViewStateBytes = 64 << 10

// ViewPlacement is the placement a restored View Instance occupied. It mirrors
// the host's closed placement vocabulary without making the pure Workspace
// model depend on the plugin domain.
type ViewPlacement string

const (
	// ViewPlacementPane is a leaf in a work Tab's split tree.
	ViewPlacementPane ViewPlacement = "pane"
	// ViewPlacementTab is a work Tab whose whole content is one View.
	ViewPlacementTab ViewPlacement = "tab"
	// ViewPlacementPage is a singleton Home page entry.
	ViewPlacementPage ViewPlacement = "page"
	// ViewPlacementWindow is a detached window that hosts one View.
	ViewPlacementWindow ViewPlacement = "window"
	// ViewPlacementAside is a side-panel slot.
	ViewPlacementAside ViewPlacement = "aside"
)

// ViewPlacements returns the closed placement vocabulary in display order.
func ViewPlacements() []ViewPlacement {
	return []ViewPlacement{
		ViewPlacementPane, ViewPlacementTab, ViewPlacementPage,
		ViewPlacementWindow, ViewPlacementAside,
	}
}

// Valid reports whether p is a declared placement.
func (p ViewPlacement) Valid() bool {
	for _, placement := range ViewPlacements() {
		if placement == p {
			return true
		}
	}
	return false
}

// ViewPaneDescriptor is the restore shape of a plugin View in a Pane. It
// carries the placement so a View Tab and a View Pane restore into the right
// surface, and per-instance state capped at MaxViewStateBytes.
type ViewPaneDescriptor struct {
	ViewID     string        `json:"viewId,omitempty"`
	ContextKey string        `json:"contextKey,omitempty"`
	Placement  ViewPlacement `json:"placement,omitempty"`
	State      []byte        `json:"state,omitempty"`
}

// PlacementOrDefault returns the descriptor's placement, treating an unwritten
// placement as the legacy Pane placement.
func (d ViewPaneDescriptor) PlacementOrDefault() ViewPlacement {
	if d.Placement == "" {
		return ViewPlacementPane
	}
	return d.Placement
}

// RestoredView is the per-placement descriptor of a View Instance that is not
// stored inside a work Tab: a Home page, a detached window, or a side panel.
// Pane and Tab placements are restored from their Tab's Pane descriptor.
type RestoredView struct {
	Placement  ViewPlacement `json:"placement"`
	ViewID     string        `json:"viewId,omitempty"`
	ContextKey string        `json:"contextKey,omitempty"`
	// Title is the last displayed title. An unavailable surface uses it
	// instead of inventing a name for a View this build cannot resolve.
	Title string `json:"title,omitempty"`
	// Order is the restore order inside the placement's own container.
	Order int    `json:"order,omitempty"`
	State []byte `json:"state,omitempty"`
}

// PaneDescriptor is the discriminated union describing what a Pane holds. An
// empty Kind is a Session, so payloads written before content kinds existed
// still restore as Sessions.
type PaneDescriptor struct {
	Kind    PaneKind            `json:"kind,omitempty"`
	Session *SessionDescriptor  `json:"session,omitempty"`
	View    *ViewPaneDescriptor `json:"view,omitempty"`
}

// NewSessionPaneDescriptor returns the descriptor of a Session Pane.
func NewSessionPaneDescriptor(descriptor SessionDescriptor) PaneDescriptor {
	value := descriptor
	return PaneDescriptor{Kind: PaneKindSession, Session: &value}
}

// Normalized makes the content kind explicit, treating an unwritten kind as
// the legacy Session kind.
func (d PaneDescriptor) Normalized() PaneDescriptor {
	if d.Kind == "" {
		d.Kind = PaneKindSession
	}
	return d
}

// SessionContent returns the Session descriptor when this Pane holds a
// Session content kind.
func (d PaneDescriptor) SessionContent() (SessionDescriptor, bool) {
	d = d.Normalized()
	if d.Kind != PaneKindSession || d.Session == nil {
		return SessionDescriptor{}, false
	}
	return *d.Session, true
}

// Available reports whether this build can open the Pane content. Only valid
// Session content is available; View and unknown kinds restore as unavailable
// placeholders.
func (d PaneDescriptor) Available() bool {
	session, ok := d.SessionContent()
	return ok && session.Valid()
}

// RestoredPane pairs a Pane id with its content descriptor.
type RestoredPane struct {
	ID model.PaneID `json:"id"`
	PaneDescriptor
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
	// GeneratedTitle marks a title composed from the merged Panes' connection
	// labels, such as "web-01/db-02". It keeps following pane renames and
	// membership until the user renames the Tab.
	GeneratedTitle bool `json:"generatedTitle,omitempty"`
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
	// Views holds the placements that are not part of a work Tab: a Page, a
	// detached Window, or an Aside. Pane and Tab placements restore from their
	// Tab's Pane descriptor instead. The field is additive: a payload written
	// before placements existed restores with no entries.
	Views []RestoredView `json:"views,omitempty"`
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
	if !readablePayloadVersion(payload.Version) {
		return out
	}

	descriptors := make(map[model.PaneID]PaneDescriptor)
	for _, pane := range payload.Panes {
		if pane.ID == "" {
			continue
		}
		descriptor, keep := sanitizePaneDescriptor(pane.PaneDescriptor)
		if !keep {
			continue
		}
		if _, exists := descriptors[pane.ID]; exists {
			continue
		}
		descriptors[pane.ID] = descriptor
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
			ID:             tab.ID,
			Number:         tab.Number,
			TitleNumber:    tab.TitleNumber,
			Title:          strings.TrimSpace(tab.Title),
			AutoTitle:      tab.AutoTitle,
			GeneratedTitle: tab.GeneratedTitle,
			Copy:           max(tab.Copy, 0),
			Root:           root,
			FocusPath:      SanitizeFocusPath(root, tab.FocusPath),
		})
	}

	for _, pane := range payload.Panes {
		if !usedPanes[pane.ID] {
			continue
		}
		descriptor, ok := descriptors[pane.ID]
		if !ok {
			continue
		}
		out.Panes = append(out.Panes, RestoredPane{ID: pane.ID, PaneDescriptor: descriptor})
	}
	// A Workspace may consist only of Pages, Windows, and Asides, so their
	// descriptors survive even when no work Tab does.
	out.Views = sanitizeViews(payload.Views)

	if len(out.Tabs) == 0 {
		out.ActiveTab = -1
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

// sanitizeViews validates the per-placement descriptors outside work Tabs.
// Pane and Tab placements belong to a Tab and are ignored here. An unknown
// placement or an entry without a view id is dropped; oversized state is
// dropped, never the entry. Only the first Page per view survives, because a
// Page is a singleton slot.
func sanitizeViews(views []RestoredView) []RestoredView {
	var out []RestoredView
	pages := make(map[string]bool)
	for _, view := range views {
		view.Title = strings.TrimSpace(view.Title)
		view.ContextKey = strings.TrimSpace(view.ContextKey)
		if strings.TrimSpace(view.ViewID) == "" || !view.Placement.Valid() {
			continue
		}
		switch view.Placement {
		case ViewPlacementPane, ViewPlacementTab:
			// Stored inside a Tab's Pane descriptor; a second copy here would
			// resurrect a closed surface.
			continue
		case ViewPlacementPage:
			if pages[view.ViewID] {
				continue
			}
			pages[view.ViewID] = true
		}
		if len(view.State) > MaxViewStateBytes {
			view.State = nil
		} else {
			view.State = append([]byte(nil), view.State...)
		}
		view.Order = max(view.Order, 0)
		out = append(out, view)
	}
	return out
}

// sanitizePaneDescriptor validates one Pane's content. A Session with an
// unusable descriptor is dropped, as before. A View or an unknown kind is kept
// but made unavailable: its Pane stays in the layout as a placeholder, so an
// unsupported kind can never prevent a Workspace from starting.
func sanitizePaneDescriptor(descriptor PaneDescriptor) (PaneDescriptor, bool) {
	descriptor = descriptor.Normalized()
	switch descriptor.Kind {
	case PaneKindSession:
		if descriptor.Session == nil {
			return PaneDescriptor{}, false
		}
		// The restore descriptor is opaque; bound it before validation so a
		// corrupt oversized row cannot grow the payload further. The Pane stays
		// and opens without restore state.
		if len(descriptor.Session.Restore) > MaxSessionRestoreBytes {
			descriptor.Session.Restore = nil
		}
		if !descriptor.Session.Valid() {
			return PaneDescriptor{}, false
		}
		descriptor.View = nil
	case PaneKindView:
		descriptor.Session = nil
		if descriptor.View != nil {
			placement := descriptor.View.PlacementOrDefault()
			if placement != ViewPlacementPane && placement != ViewPlacementTab {
				placement = ViewPlacementPane
			}
			descriptor.View.Placement = placement
			if len(descriptor.View.State) > MaxViewStateBytes {
				descriptor.View.State = nil
			}
		}
	default:
		// Unknown kinds keep the Pane as an unavailable placeholder but carry
		// no content shape this build understands.
		descriptor.Session = nil
		descriptor.View = nil
	}
	return descriptor, true
}

func sanitizeNode(
	n *Node,
	descriptors map[model.PaneID]PaneDescriptor,
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
