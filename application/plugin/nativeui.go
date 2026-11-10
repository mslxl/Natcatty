package plugin

import (
	"errors"
	"fmt"
	"sync"
	"time"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
)

// UIDirection is the writing direction the host hands to a native view.
type UIDirection string

const (
	// UIDirectionLTR is left-to-right text and mirrored logical layout.
	UIDirectionLTR UIDirection = "ltr"
	// UIDirectionRTL is right-to-left text; logical start/end properties
	// mirror, while physical properties do not.
	UIDirectionRTL UIDirection = "rtl"
)

// UIEnvironment is the host-owned environment a native view follows. The
// guest never sets it; locale and theme changes are delivered as
// locale_changed and theme_changed events, and the renderer re-reads it.
type UIEnvironment struct {
	Locale       string
	Direction    UIDirection
	Dark         bool
	ReduceMotion bool
	// Shortcuts are the live host shortcuts. A host shortcut wins over a
	// focused node unless that node declares key capture.
	Shortcuts []domainnativeui.Shortcut
}

func (e UIEnvironment) clone() UIEnvironment {
	e.Shortcuts = append([]domainnativeui.Shortcut(nil), e.Shortcuts...)
	return e
}

// UIListener is one guest registration of an event kind for a node.
type UIListener struct {
	Node  domainnativeui.NodeID
	Event domainnativeui.EventKind
}

// MaxUIListeners bounds registrations per view. Node handles share the same
// generation-tagged table and are bounded by domainnativeui.MaxNodes.
const MaxUIListeners = 1024

// uiHandleLimit is the node plus listener handle capacity of one view.
const uiHandleLimit = domainnativeui.MaxNodes + MaxUIListeners

// ErrUINativeClosed reports a native UI call on a view whose session was
// closed by unload, revocation, or a trap.
var ErrUINativeClosed = errors.New("plugin native ui: the view is closed")

// UIUnknownNodeError reports a node reference the retained tree does not hold.
type UIUnknownNodeError struct {
	Node domainnativeui.NodeRef
}

func (e *UIUnknownNodeError) Error() string {
	return fmt.Sprintf("plugin native ui: node %#x does not exist", uint64(e.Node))
}

// UIStatusOf maps a native UI host error to the binary status the guest sees.
func UIStatusOf(err error) domainnativeui.Status {
	if err == nil {
		return domainnativeui.StatusOK
	}
	var stale *domainnativeui.StaleHandleError
	var invalid *InvalidHandleError
	var unknown *UIUnknownNodeError
	var limit *LimitError
	switch {
	case errors.As(err, &stale), errors.As(err, &invalid):
		return domainnativeui.StatusStaleHandle
	case errors.Is(err, ErrUINativeClosed):
		return domainnativeui.StatusClosed
	case errors.As(err, &limit):
		return domainnativeui.StatusLimit
	case errors.As(err, &unknown):
		return domainnativeui.StatusInvalid
	}
	return domainnativeui.StatusInvalid
}

// UISession is the host-side retained native UI for one Plugin Instance. It
// owns the tree, node and listener handles, the bounded event queue, the
// host-owned focus and keyboard routing, and the environment. Guest calls are
// serialized by the runtime; renderer calls are safe concurrently.
type UISession struct {
	now func() time.Time

	mu          sync.Mutex
	closed      bool
	tree        *domainnativeui.Tree
	handles     *HandleTable
	nodeHandles map[domainnativeui.NodeID]Handle
	listeners   map[Handle]UIListener
	queue       *domainnativeui.Queue
	env         UIEnvironment
	focused     domainnativeui.NodeID

	subscribers map[uint64]func(*domainnativeui.Tree)
	nextSub     uint64
}

func newUISession(now func() time.Time) *UISession {
	if now == nil {
		now = time.Now
	}
	return &UISession{
		now:         now,
		tree:        domainnativeui.NewTree(),
		handles:     NewHandleTable(uiHandleLimit),
		nodeHandles: make(map[domainnativeui.NodeID]Handle),
		listeners:   make(map[Handle]UIListener),
		queue:       domainnativeui.NewQueue(domainnativeui.MaxQueuedEvents),
		env:         UIEnvironment{Locale: "en", Direction: UIDirectionLTR},
		subscribers: make(map[uint64]func(*domainnativeui.Tree)),
	}
}

// Closed reports whether the view released its handles.
func (u *UISession) Closed() bool {
	if u == nil {
		return true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.closed
}

// Tree returns the current committed snapshot. A published tree is immutable:
// a later batch builds a new tree and never mutates this one, so the renderer
// may read it without holding the session lock.
func (u *UISession) Tree() *domainnativeui.Tree {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.tree
}

// Revision returns the committed tree revision.
func (u *UISession) Revision() uint64 {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.tree == nil {
		return 0
	}
	return u.tree.Revision
}

// NodeHandle returns the generation-tagged handle of a live node.
func (u *UISession) NodeHandle(id domainnativeui.NodeID) (uint64, bool) {
	if u == nil {
		return 0, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	handle, ok := u.nodeHandles[id]
	return uint64(handle), ok
}

// Apply commits one atomic patch batch. On failure the last good tree and
// every live handle stay untouched.
func (u *UISession) Apply(batch domainnativeui.Batch) (domainnativeui.ApplyResult, error) {
	if u == nil {
		return domainnativeui.ApplyResult{}, ErrUINativeClosed
	}
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return domainnativeui.ApplyResult{}, ErrUINativeClosed
	}
	next, result, err := u.tree.Applied(batch, uiHandleResolver{ui: u})
	if err != nil {
		u.mu.Unlock()
		return domainnativeui.ApplyResult{}, err
	}
	u.tree = next
	if len(result.Destroyed) > 0 {
		destroyed := make(map[domainnativeui.NodeID]bool, len(result.Destroyed))
		for _, id := range result.Destroyed {
			destroyed[id] = true
		}
		for handle, listener := range u.listeners {
			if destroyed[listener.Node] {
				_ = u.handles.Release(handle)
				delete(u.listeners, handle)
			}
		}
		if destroyed[u.focused] {
			u.focused = 0
		}
	}
	subscribers := u.subscriberListLocked()
	u.mu.Unlock()
	for _, notify := range subscribers {
		notify(next)
	}
	return result, nil
}

// Listen registers one event kind for one node. A node handle and a node it
// names are accepted; session-wide events register on node 0.
func (u *UISession) Listen(node domainnativeui.NodeRef, kind domainnativeui.EventKind) (Handle, error) {
	if u == nil {
		return 0, ErrUINativeClosed
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return 0, ErrUINativeClosed
	}
	if !kind.Valid() {
		return 0, fmt.Errorf("plugin native ui: unknown event kind %d", kind)
	}
	id, err := u.listenerNodeLocked(node, kind)
	if err != nil {
		return 0, err
	}
	if len(u.listeners) >= MaxUIListeners {
		return 0, &LimitError{Resource: "native ui listeners", Limit: MaxUIListeners}
	}
	handle, err := u.handles.Issue(UIListener{Node: id, Event: kind})
	if err != nil {
		return 0, err
	}
	u.listeners[handle] = UIListener{Node: id, Event: kind}
	return handle, nil
}

func (u *UISession) listenerNodeLocked(ref domainnativeui.NodeRef, kind domainnativeui.EventKind) (domainnativeui.NodeID, error) {
	if ref == 0 {
		if !kind.SessionEvent() {
			return 0, &UIUnknownNodeError{Node: ref}
		}
		return 0, nil
	}
	id, ok := u.nodeIDForRefLocked(ref)
	if !ok {
		return 0, &UIUnknownNodeError{Node: ref}
	}
	if kind.SessionEvent() && id != 0 {
		return 0, fmt.Errorf("plugin native ui: event %s is session-wide and registers on node 0", kind)
	}
	return id, nil
}

// Unlisten releases one listener handle. An unknown or already released
// listener reports a stale handle.
func (u *UISession) Unlisten(handle Handle) error {
	if u == nil {
		return ErrUINativeClosed
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return ErrUINativeClosed
	}
	value, err := u.handles.Resolve(handle)
	if err != nil {
		return err
	}
	if _, ok := value.(UIListener); !ok {
		return &InvalidHandleError{Handle: handle}
	}
	delete(u.listeners, handle)
	return u.handles.Release(handle)
}

// Listeners returns a copy of the live registrations.
func (u *UISession) Listeners() map[Handle]UIListener {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make(map[Handle]UIListener, len(u.listeners))
	for handle, listener := range u.listeners {
		out[handle] = listener
	}
	return out
}

// Poll drains at most max queued events, oldest first. A non-positive max
// drains everything.
func (u *UISession) Poll(max int) []domainnativeui.Event {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	return u.queue.Drain(max)
}

// PendingEvents returns how many events wait for the guest to poll. A
// renderer uses it to drive the guest poll export only when there is work.
func (u *UISession) PendingEvents() int {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return 0
	}
	return u.queue.Len()
}

// DroppedEvents returns how many events overflow dropped.
func (u *UISession) DroppedEvents() int {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.queue.Dropped()
}

// Enqueue queues one event the guest registered for. The event is dropped
// when the view is closed, the node is gone, or no listener matches; the host
// never calls the guest from the UI goroutine.
func (u *UISession) Enqueue(event domainnativeui.Event) bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.enqueueLocked(event)
}

// EnqueueNode queues an event for one node by its guest id, filling the
// generation-tagged handle the guest knows.
func (u *UISession) EnqueueNode(id domainnativeui.NodeID, event domainnativeui.Event) bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	handle, ok := u.nodeHandles[id]
	if !ok {
		return false
	}
	event.Node = domainnativeui.HandleFromNode(uint64(handle))
	return u.enqueueLocked(event)
}

func (u *UISession) enqueueLocked(event domainnativeui.Event) bool {
	if u.closed {
		return false
	}
	if event.Time == 0 {
		event.Time = u.now().UnixNano()
	}
	if event.Kind.SessionEvent() {
		if event.Node != 0 || !u.hasListenerLocked(0, event.Kind) {
			return false
		}
		u.queue.Enqueue(event)
		return true
	}
	id, ok := u.nodeIDForRefLocked(event.Node)
	if !ok || !u.hasListenerLocked(id, event.Kind) {
		return false
	}
	handle, ok := u.nodeHandles[id]
	if !ok {
		return false
	}
	event.Node = domainnativeui.HandleFromNode(uint64(handle))
	event.Related = u.relatedHandleLocked(event.Related)
	u.queue.Enqueue(event)
	return true
}

// relatedHandleLocked rewrites the other node of a drag, drop, or resize event
// to its generation-tagged handle. A related node that no longer exists leaves
// the field zero, so the guest never receives a stale handle or a raw id.
func (u *UISession) relatedHandleLocked(ref domainnativeui.NodeRef) domainnativeui.NodeRef {
	if ref == 0 {
		return 0
	}
	related, ok := u.nodeIDForRefLocked(ref)
	if !ok {
		return 0
	}
	handle, ok := u.nodeHandles[related]
	if !ok {
		return 0
	}
	return domainnativeui.HandleFromNode(uint64(handle))
}

func (u *UISession) hasListenerLocked(node domainnativeui.NodeID, kind domainnativeui.EventKind) bool {
	for _, listener := range u.listeners {
		if listener.Node == node && listener.Event == kind {
			return true
		}
	}
	return false
}

// Environment returns the current host environment.
func (u *UISession) Environment() UIEnvironment {
	if u == nil {
		return UIEnvironment{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.env.clone()
}

// initEnvironment installs the environment of a freshly opened session
// without queueing a change event. The guest has not rendered yet, so there is
// no previous value to react to.
func (u *UISession) initEnvironment(env UIEnvironment) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	u.env = env.clone()
}

// SetEnvironment replaces the host environment. A locale or direction change
// queues locale_changed, and a theme change queues theme_changed, for a
// session-wide listener; the renderer re-reads the values every frame.
func (u *UISession) SetEnvironment(env UIEnvironment) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	previous := u.env
	u.env = env.clone()
	if env.Locale != previous.Locale || env.Direction != previous.Direction {
		u.enqueueLocked(domainnativeui.Event{Kind: domainnativeui.EventLocaleChanged})
	}
	if env.Dark != previous.Dark {
		u.enqueueLocked(domainnativeui.Event{Kind: domainnativeui.EventThemeChanged})
	}
}

// Focus returns the focused node id, or zero.
func (u *UISession) Focus() domainnativeui.NodeID {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.focused
}

// SetFocus moves host-owned focus and queues blur/focus for registered
// listeners. Hidden, disabled, and non-focusable nodes are rejected.
func (u *UISession) SetFocus(id domainnativeui.NodeID) bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return false
	}
	if id != 0 {
		if _, ok := u.tree.Nodes[id]; !ok {
			return false
		}
		if !domainnativeui.Focusable(u.tree, id) {
			return false
		}
	}
	if id == u.focused {
		return true
	}
	if u.focused != 0 {
		if handle, ok := u.nodeHandles[u.focused]; ok {
			u.enqueueLocked(domainnativeui.Event{
				Kind: domainnativeui.EventBlur,
				Node: domainnativeui.HandleFromNode(uint64(handle)),
			})
		}
	}
	u.focused = id
	if id != 0 {
		if handle, ok := u.nodeHandles[id]; ok {
			u.enqueueLocked(domainnativeui.Event{
				Kind: domainnativeui.EventFocus,
				Node: domainnativeui.HandleFromNode(uint64(handle)),
			})
		}
	}
	return true
}

// CapturesKeys reports whether the focused node declared key capture, so the
// renderer can decide whether Tab traverses or stays with the node.
func (u *UISession) CapturesKeys() bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.focused == 0 {
		return false
	}
	return u.tree.Nodes[u.focused].BoolProp(domainnativeui.PropKeyCapture, false)
}

// FocusOrder returns the host tab traversal order.
func (u *UISession) FocusOrder() []domainnativeui.NodeID {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	return domainnativeui.FocusOrder(u.tree)
}

// Key routes one key press. A host shortcut wins unless the focused node
// declares key capture; otherwise key_down or key_up is queued for the
// focused node and reported as delivered. The returned command, when
// non-empty, is the host command the caller must run.
func (u *UISession) Key(mods domainnativeui.Modifiers, key domainnativeui.Key, down bool) (string, bool) {
	if u == nil {
		return "", false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.focused == 0 {
		return "", false
	}
	route := domainnativeui.RouteKey(u.tree, u.focused, mods, key, u.env.Shortcuts)
	if route.HostShortcut != "" {
		return route.HostShortcut, false
	}
	handle, ok := u.nodeHandles[u.focused]
	if !ok {
		return "", false
	}
	kind := domainnativeui.EventKeyDown
	if !down {
		kind = domainnativeui.EventKeyUp
	}
	delivered := u.enqueueLocked(domainnativeui.Event{
		Kind: kind,
		Node: domainnativeui.HandleFromNode(uint64(handle)),
		Key:  key,
		Mods: mods,
	})
	return "", delivered
}

// SubscribeUpdates registers fn for every committed tree snapshot. The
// callback runs without the session lock and must not call back into the
// session synchronously; it is a host renderer notification, never a guest
// call. The returned function unsubscribes and is safe to call twice.
func (u *UISession) SubscribeUpdates(fn func(*domainnativeui.Tree)) func() {
	if u == nil || fn == nil {
		return func() {}
	}
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return func() {}
	}
	u.nextSub++
	id := u.nextSub
	u.subscribers[id] = fn
	u.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			u.mu.Lock()
			delete(u.subscribers, id)
			u.mu.Unlock()
		})
	}
}

func (u *UISession) subscriberListLocked() []func(*domainnativeui.Tree) {
	if len(u.subscribers) == 0 {
		return nil
	}
	out := make([]func(*domainnativeui.Tree), 0, len(u.subscribers))
	for _, fn := range u.subscribers {
		out = append(out, fn)
	}
	return out
}

func (u *UISession) nodeIDForRefLocked(ref domainnativeui.NodeRef) (domainnativeui.NodeID, bool) {
	if handle, ok := ref.Handle(); ok {
		value, err := u.handles.Resolve(Handle(handle))
		if err != nil {
			return 0, false
		}
		id, ok := value.(domainnativeui.NodeID)
		if !ok {
			return 0, false
		}
		if _, live := u.tree.Nodes[id]; !live {
			return 0, false
		}
		return id, true
	}
	id, _ := ref.NodeID()
	if _, live := u.tree.Nodes[id]; !live {
		return 0, false
	}
	return id, true
}

// Reset releases every node and listener handle and clears the retained tree,
// focus, and queued events. The plugin session stays usable: a view opened
// again starts from an empty tree. It is idempotent and a no-op after Close,
// which is what a host calls when the last Pane showing a view closes.
func (u *UISession) Reset() {
	if u == nil {
		return
	}
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.tree = domainnativeui.NewTree()
	u.nodeHandles = make(map[domainnativeui.NodeID]Handle)
	u.listeners = make(map[Handle]UIListener)
	u.queue.Clear()
	u.handles.RevokeAll()
	u.focused = 0
	subscribers := u.subscriberListLocked()
	u.mu.Unlock()
	for _, notify := range subscribers {
		notify(u.tree)
	}
}

// close releases the tree, every node and listener handle, and the event
// queue. It is idempotent and runs on unload, revocation, and instance
// recycling.
func (u *UISession) close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	u.closed = true
	u.tree = nil
	u.nodeHandles = make(map[domainnativeui.NodeID]Handle)
	u.listeners = make(map[Handle]UIListener)
	u.queue.Clear()
	u.handles.RevokeAll()
	u.focused = 0
	u.subscribers = nil
}

// uiHandleResolver adapts the session handle table to domainnativeui.Resolver.
// The caller holds the session lock.
type uiHandleResolver struct {
	ui *UISession
}

func (r uiHandleResolver) NodeForHandle(handle uint64) (domainnativeui.NodeID, bool) {
	value, err := r.ui.handles.Resolve(Handle(handle))
	if err != nil {
		return 0, false
	}
	id, ok := value.(domainnativeui.NodeID)
	return id, ok
}

func (r uiHandleResolver) IssueHandle(id domainnativeui.NodeID) (uint64, error) {
	handle, err := r.ui.handles.Issue(id)
	if err != nil {
		return 0, err
	}
	r.ui.nodeHandles[id] = handle
	return uint64(handle), nil
}

func (r uiHandleResolver) ReleaseHandle(handle uint64) error {
	if err := r.ui.handles.Release(Handle(handle)); err != nil {
		return err
	}
	for id, live := range r.ui.nodeHandles {
		if uint64(live) == handle {
			delete(r.ui.nodeHandles, id)
			break
		}
	}
	return nil
}

func (r uiHandleResolver) ReleaseNode(id domainnativeui.NodeID) {
	handle, ok := r.ui.nodeHandles[id]
	if !ok {
		return
	}
	delete(r.ui.nodeHandles, id)
	_ = r.ui.handles.Release(handle)
}
