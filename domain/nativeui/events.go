package nativeui

import "sort"

// MaxQueuedEvents bounds one plugin's native UI event queue. The oldest event
// is dropped on overflow and counted, never growing memory without bound.
const MaxQueuedEvents = 1024

// EventKind is the closed v1 native UI event set. Values are part of the wire
// contract and must match the generated IDL bindings.
type EventKind int32

const (
	EventClick         EventKind = 0
	EventDoubleClick   EventKind = 1
	EventContextMenu   EventKind = 2
	EventChange        EventKind = 3
	EventInput         EventKind = 4
	EventSubmit        EventKind = 5
	EventFocus         EventKind = 6
	EventBlur          EventKind = 7
	EventKeyDown       EventKind = 8
	EventKeyUp         EventKind = 9
	EventScroll        EventKind = 10
	EventSelection     EventKind = 11
	EventResize        EventKind = 12
	EventDrag          EventKind = 13
	EventDrop          EventKind = 14
	EventHover         EventKind = 15
	EventItemRequest   EventKind = 16
	EventAnimationEnd  EventKind = 17
	EventWebview       EventKind = 18
	EventLocaleChanged EventKind = 19
	EventThemeChanged  EventKind = 20
)

// Valid reports whether k is a declared event kind.
func (k EventKind) Valid() bool { return k >= EventClick && k <= EventThemeChanged }

// String renders the IDL name of k.
func (k EventKind) String() string {
	switch k {
	case EventClick:
		return "click"
	case EventDoubleClick:
		return "double_click"
	case EventContextMenu:
		return "context_menu"
	case EventChange:
		return "change"
	case EventInput:
		return "input"
	case EventSubmit:
		return "submit"
	case EventFocus:
		return "focus"
	case EventBlur:
		return "blur"
	case EventKeyDown:
		return "key_down"
	case EventKeyUp:
		return "key_up"
	case EventScroll:
		return "scroll"
	case EventSelection:
		return "selection"
	case EventResize:
		return "resize"
	case EventDrag:
		return "drag"
	case EventDrop:
		return "drop"
	case EventHover:
		return "hover"
	case EventItemRequest:
		return "item_request"
	case EventAnimationEnd:
		return "animation_end"
	case EventWebview:
		return "webview"
	case EventLocaleChanged:
		return "locale_changed"
	case EventThemeChanged:
		return "theme_changed"
	}
	return "unknown"
}

// SessionEvent reports whether the event is session-wide and may be
// registered with node 0 instead of a live node.
func (k EventKind) SessionEvent() bool {
	switch k {
	case EventWebview, EventLocaleChanged, EventThemeChanged, EventResize:
		return true
	}
	return false
}

// Coalesces reports whether a later event of the same kind for the same node
// replaces an earlier one in place instead of queueing behind it.
func (k EventKind) Coalesces() bool {
	switch k {
	case EventInput, EventScroll, EventHover, EventSelection, EventResize,
		EventDrag, EventItemRequest:
		return true
	}
	return false
}

// Key is the closed key vocabulary the host routes. Values are part of the
// wire contract and must match the generated IDL bindings.
type Key int32

const (
	KeyUnknown      Key = 0
	KeyTab          Key = 1
	KeyEnter        Key = 2
	KeyEscape       Key = 3
	KeySpace        Key = 4
	KeyBackspace    Key = 5
	KeyDelete       Key = 6
	KeyLeft         Key = 7
	KeyRight        Key = 8
	KeyUp           Key = 9
	KeyDown         Key = 10
	KeyHome         Key = 11
	KeyEnd          Key = 12
	KeyPageUp       Key = 13
	KeyPageDown     Key = 14
	KeyInsert       Key = 15
	KeyF1           Key = 16
	KeyF2           Key = 17
	KeyF3           Key = 18
	KeyF4           Key = 19
	KeyF5           Key = 20
	KeyF6           Key = 21
	KeyF7           Key = 22
	KeyF8           Key = 23
	KeyF9           Key = 24
	KeyF10          Key = 25
	KeyF11          Key = 26
	KeyF12          Key = 27
	KeyA            Key = 28
	KeyB            Key = 29
	KeyC            Key = 30
	KeyD            Key = 31
	KeyE            Key = 32
	KeyF            Key = 33
	KeyG            Key = 34
	KeyH            Key = 35
	KeyI            Key = 36
	KeyJ            Key = 37
	KeyK            Key = 38
	KeyL            Key = 39
	KeyM            Key = 40
	KeyN            Key = 41
	KeyO            Key = 42
	KeyP            Key = 43
	KeyQ            Key = 44
	KeyR            Key = 45
	KeyS            Key = 46
	KeyT            Key = 47
	KeyU            Key = 48
	KeyV            Key = 49
	KeyW            Key = 50
	KeyX            Key = 51
	KeyY            Key = 52
	KeyZ            Key = 53
	Key0            Key = 54
	Key1            Key = 55
	Key2            Key = 56
	Key3            Key = 57
	Key4            Key = 58
	Key5            Key = 59
	Key6            Key = 60
	Key7            Key = 61
	Key8            Key = 62
	Key9            Key = 63
	KeyMinus        Key = 64
	KeyEqual        Key = 65
	KeyComma        Key = 66
	KeyPeriod       Key = 67
	KeySlash        Key = 68
	KeySemicolon    Key = 69
	KeyQuote        Key = 70
	KeyBracketLeft  Key = 71
	KeyBracketRight Key = 72
	KeyBackslash    Key = 73
	KeyGrave        Key = 74
)

// Valid reports whether k is a declared key.
func (k Key) Valid() bool { return k >= KeyUnknown && k <= KeyGrave }

// Modifiers are the modifier bits held while a key is pressed.
type Modifiers uint32

const (
	ModShift   Modifiers = 1 << 0
	ModControl Modifiers = 1 << 1
	ModAlt     Modifiers = 1 << 2
	ModMeta    Modifiers = 1 << 3
)

// Event is one native UI event queued for the plugin. Payload fields are
// meaningful per kind; a session-wide event has Node 0. Node and Related are
// the generation-tagged handles the guest received for the nodes, never raw
// guest ids, so a stale handle in an event is detectable by the guest.
type Event struct {
	Kind   EventKind
	Node   NodeRef
	Time   int64
	Text   string
	Key    Key
	Mods   Modifiers
	Button int32
	Index  int32
	X      float32
	Y      float32
	DX     float32
	DY     float32
	Width  float32
	Height float32
	// Related is the other node of a drag, drop, or resize event. A webview
	// event carries the generation-tagged WebviewHandle of the page that
	// sent or receives the message; Text carries its JSON envelope.
	Related NodeRef
}

// Queue is a bounded, per-node-ordered native UI event queue. Coalescing
// kinds replace an earlier event of the same kind for the same node in place,
// so ordering relative to other events is preserved.
type Queue struct {
	events  []Event
	limit   int
	dropped int
}

// NewQueue returns a queue with limit events, or MaxQueuedEvents when limit
// is not positive.
func NewQueue(limit int) *Queue {
	if limit <= 0 {
		limit = MaxQueuedEvents
	}
	return &Queue{limit: limit}
}

// Enqueue appends ev. A coalescing kind replaces the last queued event of the
// same kind and node. Overflow drops the oldest event and counts it.
func (q *Queue) Enqueue(ev Event) {
	if q == nil {
		return
	}
	if ev.Kind.Coalesces() {
		for i := len(q.events) - 1; i >= 0; i-- {
			if q.events[i].Node == ev.Node && q.events[i].Kind == ev.Kind {
				q.events[i] = ev
				return
			}
		}
	}
	if len(q.events) >= q.limit {
		q.events = q.events[1:]
		q.dropped++
	}
	q.events = append(q.events, ev)
}

// Drain removes and returns at most max events, oldest first. A non-positive
// max drains everything.
func (q *Queue) Drain(max int) []Event {
	if q == nil || len(q.events) == 0 {
		return nil
	}
	if max <= 0 || max > len(q.events) {
		max = len(q.events)
	}
	out := append([]Event(nil), q.events[:max]...)
	q.events = q.events[max:]
	return out
}

// Len returns the number of queued events.
func (q *Queue) Len() int {
	if q == nil {
		return 0
	}
	return len(q.events)
}

// Dropped returns how many events overflow dropped.
func (q *Queue) Dropped() int {
	if q == nil {
		return 0
	}
	return q.dropped
}

// Clear discards every queued event.
func (q *Queue) Clear() {
	if q == nil {
		return
	}
	q.events = nil
}

// FocusOrder returns the tab traversal order: focusable nodes with a
// non-negative tab index, stably ordered by tab index and then by document
// order. Hidden nodes and their descendants are excluded.
func FocusOrder(tree *Tree) []NodeID {
	if tree == nil || tree.Root == 0 {
		return nil
	}
	type entry struct {
		id       NodeID
		tabIndex int64
		order    int
	}
	var entries []entry
	order := 0
	var walk func(id NodeID, hidden bool)
	walk = func(id NodeID, hidden bool) {
		node, ok := tree.Nodes[id]
		if !ok || hidden {
			return
		}
		hidden = node.BoolProp(PropHidden, false)
		if !hidden && Focusable(tree, id) {
			index := node.IntProp(PropTabIndex, 0)
			if index >= 0 {
				entries = append(entries, entry{id: id, tabIndex: index, order: order})
			}
		}
		order++
		for _, child := range node.Children {
			walk(child, hidden)
		}
	}
	walk(tree.Root, false)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].tabIndex != entries[j].tabIndex {
			return entries[i].tabIndex < entries[j].tabIndex
		}
		return entries[i].order < entries[j].order
	})
	ids := make([]NodeID, len(entries))
	for i, entry := range entries {
		ids[i] = entry.id
	}
	return ids
}

// Focusable reports whether a node can take keyboard focus. An explicit
// focusable property wins; otherwise interactive widgets are focusable and
// disabled or hidden nodes are not.
func Focusable(tree *Tree, id NodeID) bool {
	node, ok := tree.Nodes[id]
	if !ok || node.BoolProp(PropHidden, false) || node.BoolProp(PropDisabled, false) {
		return false
	}
	if value, ok := node.Prop(PropFocusable); ok && value.Kind == PropBool {
		return value.Int != 0
	}
	return node.Kind.FocusableKind()
}

// HiddenByAncestor reports whether id or one of its ancestors is hidden.
func HiddenByAncestor(tree *Tree, id NodeID) bool {
	for id != 0 {
		node, ok := tree.Nodes[id]
		if !ok {
			return false
		}
		if node.BoolProp(PropHidden, false) {
			return true
		}
		id = node.Parent
	}
	return false
}

// Shortcut is one host shortcut. The host wins over a focused node unless
// the node declares key capture.
type Shortcut struct {
	Mods    Modifiers
	Key     Key
	Command string
}

// RouteResult is the host's decision for one key press.
type RouteResult struct {
	// HostShortcut is the command the host consumed, if any.
	HostShortcut string
	// Deliver asks the caller to queue key_down/key_up for the plugin.
	Deliver bool
	// Capture reports that the focused node declared key capture, so the
	// host shortcuts did not run.
	Capture bool
}

// RouteKey applies the keyboard routing rules: host shortcuts win unless the
// focused node captures keys. A node without key capture but with no matching
// shortcut receives the key.
func RouteKey(tree *Tree, focused NodeID, mods Modifiers, key Key, shortcuts []Shortcut) RouteResult {
	capture := false
	if node, ok := tree.Node(focused); ok {
		capture = node.BoolProp(PropKeyCapture, false)
	}
	if !capture {
		for _, shortcut := range shortcuts {
			if shortcut.Key == key && shortcut.Mods == mods {
				return RouteResult{HostShortcut: shortcut.Command}
			}
		}
	}
	return RouteResult{Deliver: true, Capture: capture}
}
