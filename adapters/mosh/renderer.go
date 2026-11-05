// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"fmt"
	"os"
	"sync"
	"time"

	moshcore "github.com/mslxl/natcatty/internal/mosh"
	"github.com/mslxl/natcatty/internal/mosh/terminal"
)

// noTitlePrefix mirrors mosh-client's MOSH_TITLE_NOPREFIX handling: stock
// checks getenv for presence, so an empty value still suppresses the prefix.
func noTitlePrefix() bool {
	_, set := os.LookupEnv("MOSH_TITLE_NOPREFIX")
	return set
}

// screenSizeHandler receives the remote terminal's authoritative size and the
// output offset before it. It is called before the display bytes encoded for
// that size are queued, and it must not return until the renderer has applied
// the size and every earlier output byte reached the screen at the old size.
type screenSizeHandler func(cols, rows int, position uint64)

// terminalRenderer is the client-side half of mosh's terminal model. It owns
// the remote Complete snapshots, the Display encoder, and the stock overlay
// order. It is the only path from HostInstruction bytes to the Session's
// output queue: raw host bytes are parsed and rendered here, never forwarded.
type terminalRenderer struct {
	queue *outputQueue

	display  *terminal.Display
	overlays *terminal.OverlayManager
	clock    terminal.Clock

	// mu guards the terminal state shared by the socket reader, input
	// translation, and the overlay timer. drawMu serializes frame emission so a
	// timer redraw cannot encode a size that changes while a resize signal is
	// in flight.
	mu        sync.Mutex
	drawMu    sync.Mutex
	states    map[uint64]*terminal.Complete
	latest    *terminal.Complete
	lastDrawn *terminal.Framebuffer
	// initialized reports that the initial blank frame has been written; it is
	// Display::new_frame's "initialized" argument after that.
	initialized bool
	// screenWidth and screenHeight are the remote sizes already handed to the
	// screen-size handler.
	screenWidth, screenHeight int

	userInput *terminal.UserInput

	// Notification state mirrors STMClient's main loop. remoteStateNum is
	// Network::get_remote_state_num(); zero means "still connecting".
	startedAt          uint64
	udpPort            int
	remoteStateNum     uint64
	connectingTimedOut bool
	lastOverlayDraw    uint64
	nextOverlayDraw    uint64
	// lastServerAck is the renderer clock value of the most recent
	// server_acked() call; tests use it to observe the transport wiring.
	lastServerAck uint64
	// ackTimestamp maps a transport send timestamp onto the renderer clock.
	// The production clock is process-relative, while Client timestamps are
	// wall-clock time.Time values; a test clock replaces both.
	ackTimestamp func(time.Time) uint64

	// handlerReady closes when a Session registers its inverse resize signal.
	// Requesting a size blocks until then so display bytes for that size can
	// never overtake the resize.
	handlerMu    sync.Mutex
	handler      screenSizeHandler
	handlerReady chan struct{}
	handlerOnce  sync.Once

	closed    chan struct{}
	closeOnce sync.Once
}

// terminalRendererOptions selects the display capabilities and title prefix.
// The production defaults mirror mosh-client with MOSH_NO_TERM_INIT unset in
// the desktop path (no alternate-screen entry) and stock's title prefix.
type terminalRendererOptions struct {
	Display     terminal.DisplayOptions
	TitlePrefix []rune
	// Clock supplies overlay timestamps. Nil uses terminal.MonotonicMillis and
	// converts transport wall-clock timestamps with terminal.MonotonicMillisAt;
	// tests substitute a manually advanced clock, which is then also read
	// directly for acknowledgements.
	Clock terminal.Clock
}

// defaultTerminalRendererOptions returns the production renderer settings.
func defaultTerminalRendererOptions() terminalRendererOptions {
	options := terminalRendererOptions{Display: terminal.DefaultDisplayOptions()}
	if !noTitlePrefix() {
		options.TitlePrefix = []rune("[mosh] ")
	}
	return options
}

// escapeKeyName returns the name stock mosh-client puts in its notification
// bar for MOSH_ESCAPE_KEY. An empty value disables the hint; an invalid or
// length-other-than-one value falls back to Ctrl-^, and the five keys stock
// refuses to use also fall back.
func escapeKeyName() string {
	const defaultName = "Ctrl-^"
	value, set := os.LookupEnv("MOSH_ESCAPE_KEY")
	if !set {
		return defaultName
	}
	if value == "" {
		return ""
	}
	if len(value) != 1 {
		return defaultName
	}
	key := value[0]
	switch key {
	case 0x03, 0x04, 0x0a, 0x0c, 0x0d:
		return defaultName
	}
	if key < 32 {
		return "Ctrl-" + string(rune(key+'@'))
	}
	return `"` + string(rune(key)) + `"`
}

// connectingNotification is STMClient's connecting_notification string,
// formatted once at startup from the announced UDP port.
func connectingNotification(port int) string {
	return fmt.Sprintf("Nothing received from server on UDP port %d.", port)
}

// newTerminalRenderer creates the initial remote state exactly as stock does:
// a Complete of the client's starting size, with a blank local framebuffer of
// that same size. The state-0 snapshot is the reference for every first diff.
func newTerminalRenderer(queue *outputQueue, cols, rows int) *terminalRenderer {
	return newTerminalRendererWithOptions(queue, cols, rows, defaultTerminalRendererOptions())
}

func newTerminalRendererWithOptions(queue *outputQueue, cols, rows int,
	options terminalRendererOptions) *terminalRenderer {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	clock := options.Clock
	ackTimestamp := terminal.MonotonicMillisAt
	if clock == nil {
		clock = terminal.MonotonicMillis
	} else {
		ackTimestamp = func(time.Time) uint64 { return clock() }
	}
	initial := terminal.NewComplete(cols, rows)
	overlays := terminal.NewOverlayManager(clock)
	if len(options.TitlePrefix) > 0 {
		overlays.SetTitlePrefix(options.TitlePrefix)
	}
	// Stock never calls set_escape_key_string when MOSH_ESCAPE_KEY is empty, so
	// an empty name must leave escape_key_string empty: no quit hint is shown.
	if name := escapeKeyName(); name != "" {
		overlays.Notifications().SetEscapeKeyString(name)
	}
	now := clock()
	return &terminalRenderer{
		queue:           queue,
		display:         terminal.NewDisplay(options.Display),
		overlays:        overlays,
		clock:           clock,
		ackTimestamp:    ackTimestamp,
		states:          map[uint64]*terminal.Complete{0: initial},
		latest:          initial,
		lastDrawn:       terminal.NewFramebuffer(cols, rows),
		userInput:       terminal.NewUserInput(),
		handlerReady:    make(chan struct{}),
		closed:          make(chan struct{}),
		screenWidth:     cols,
		screenHeight:    rows,
		startedAt:       now,
		lastOverlayDraw: now,
		// STMClient wakes at least every 250ms while still connecting.
		nextOverlayDraw: now + 250,
	}
}

// setUDPPort records the announced UDP port for the connecting notification,
// which stock formats as "Nothing received from server on UDP port N.".
func (r *terminalRenderer) setUDPPort(port int) {
	r.mu.Lock()
	r.udpPort = port
	r.mu.Unlock()
}

// serverAcked records the server's latest acknowledgement of our sent state,
// as STMClient::process_network_input does. sentAt is the send time of the
// oldest retained sent state; the renderer's clock decides how to convert it.
func (r *terminalRenderer) serverAcked(sentAt time.Time) {
	r.mu.Lock()
	stamp := r.ackTimestamp(sentAt)
	r.overlays.Notifications().ServerAcked(stamp)
	r.lastServerAck = stamp
	r.mu.Unlock()
}

// setNetworkError shows stock's network-error notification.
func (r *terminalRenderer) setNetworkError(message string) {
	r.mu.Lock()
	now := r.clock()
	r.overlays.Notifications().SetNetworkError(message)
	r.nextOverlayDraw = now
	r.mu.Unlock()
}

// clearNetworkError shortens a network-error notification to stock's one-second
// grace period.
func (r *terminalRenderer) clearNetworkError() {
	r.mu.Lock()
	r.overlays.Notifications().ClearNetworkError()
	r.mu.Unlock()
}

// setScreenSizeHandler installs the inverse resize signal. It may be called
// once; later calls replace the handler for subsequent sizes.
func (r *terminalRenderer) setScreenSizeHandler(handler screenSizeHandler) {
	if handler == nil {
		return
	}
	r.handlerMu.Lock()
	r.handler = handler
	r.handlerMu.Unlock()
	r.handlerOnce.Do(func() { close(r.handlerReady) })
}

// requestScreenSize hands the remote size and its stream position to the
// Session and waits until it has been applied. If no Session registered a
// handler, the request waits until one does or the transport closes.
func (r *terminalRenderer) requestScreenSize(cols, rows int, position uint64) bool {
	select {
	case <-r.handlerReady:
	case <-r.closed:
		return false
	}
	r.handlerMu.Lock()
	handler := r.handler
	r.handlerMu.Unlock()
	if handler == nil {
		return false
	}
	handler(cols, rows, position)
	return true
}

// emitInitial writes Display::open and the first blank frame, matching
// STMClient::init followed by STMClient::main_init. The frame clears the
// bootstrap banner because the first new_frame starts with a full clear.
func (r *terminalRenderer) emitInitial() error {
	r.mu.Lock()
	if r.initialized {
		r.mu.Unlock()
		return nil
	}
	blank := terminal.NewFramebuffer(r.lastDrawn.DS.Width(), r.lastDrawn.DS.Height())
	frame := r.display.Open() + r.display.NewFrame(false, blank, blank)
	r.lastDrawn = blank
	r.initialized = true
	r.mu.Unlock()
	return r.queue.push([]byte(frame))
}

// applyUpdate reconstructs every accepted remote state. Out-of-order states
// are kept as references for a later diff but never drawn; only the appended
// newest state becomes the renderer's target.
func (r *terminalRenderer) applyUpdate(update moshcore.Update) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, state := range update.HostStates {
		reference, ok := r.states[state.OldNum]
		if !ok {
			return fmt.Errorf("%w: host state %d references unavailable state %d",
				moshcore.ErrProtocol, state.Num, state.OldNum)
		}
		next := reference.Clone()
		for _, instruction := range state.Instructions {
			if len(instruction.HostString) > 0 {
				// mosh-server never interrogates the client terminal. Stock
				// asserts the reply is empty; discard it rather than letting a
				// server inject terminal-to-host bytes into the input stream.
				_ = next.Act(string(instruction.HostString))
			}
			if instruction.Resize {
				next.Resize(int(instruction.Width), int(instruction.Height))
			}
			if instruction.EchoAckPresent {
				next.ApplyEchoAck(instruction.EchoAckNum)
			}
		}
		r.states[state.Num] = next
		if state.Newest {
			r.latest = next
			if state.Num > r.remoteStateNum {
				r.remoteStateNum = state.Num
			}
			// STMClient calls server_heard() from process_network_input, using
			// the latest remote state's arrival timestamp.
			r.overlays.Notifications().ServerHeard(r.clock())
			if r.remoteStateNum != 0 &&
				string(r.overlays.Notifications().NotificationString()) == connectingNotification(r.udpPort) {
				// The connection is live; drop the connecting bar exactly as the
				// stock main loop does once remote_state_num is nonzero and the
				// current message is still the connecting notification.
				r.overlays.Notifications().SetNotificationString(nil, false, true)
				r.nextOverlayDraw = r.clock()
			}
		}
		for num := range r.states {
			if num < state.ThrowawayNum && num != state.Num {
				delete(r.states, num)
			}
		}
	}
	return nil
}

// emitLatest encodes the latest remote state plus the current overlays through
// the stock overlay order and Display. A remote size change is signalled to the
// Session before any bytes encoded at the new size are queued.
func (r *terminalRenderer) emitLatest() error {
	r.drawMu.Lock()
	defer r.drawMu.Unlock()
	for {
		r.mu.Lock()
		if !r.initialized || r.latest == nil {
			r.mu.Unlock()
			return nil
		}
		width, height := r.latest.Framebuffer().DS.Size()
		if width == r.screenWidth && height == r.screenHeight {
			// Clone and encode under the lock that captured the size, so the
			// reader cannot substitute a newer, differently sized state between
			// the check and the encoder call.
			target := r.latest.CloneFramebuffer()
			r.overlays.Apply(target)
			frame := r.display.NewFrame(true, r.lastDrawn, target)
			r.lastDrawn = target
			r.mu.Unlock()
			return r.queue.push([]byte(frame))
		}
		// Every frame already queued was encoded for the old size. The Session
		// uses this offset to deliver those bytes before applying the new size
		// to MyGo.
		position := r.queue.pushedBytes()
		r.mu.Unlock()
		if !r.requestScreenSize(width, height, position) {
			return nil
		}
		r.mu.Lock()
		r.screenWidth, r.screenHeight = width, height
		r.mu.Unlock()
		// The reader may have applied another size while the signal was in
		// flight; loop so only the newest size is encoded.
	}
}

// maintainOverlays is the timer-driven half of STMClient::main. It advances the
// connecting notification, expires messages, and redraws when stock's select
// loop would have computed a new frame from network->wait_time() and
// overlays.wait_time(). It is called from the transport's tick loop.
func (r *terminalRenderer) maintainOverlays() error {
	r.mu.Lock()
	if !r.initialized {
		r.mu.Unlock()
		return nil
	}
	select {
	case <-r.closed:
		r.mu.Unlock()
		return nil
	default:
	}

	now := r.clock()
	notifications := r.overlays.Notifications()
	changed := false
	if r.remoteStateNum == 0 {
		sinceStart := now - r.startedAt
		switch {
		case sinceStart > 15000 && !r.connectingTimedOut:
			// Stock shows this permanent message and calls
			// network->start_shutdown(). This ticket deliberately wires only the
			// visible notification: a renderer-initiated transport end would
			// misclassify a connection that never established as a clean exit,
			// so shutdown escalation stays with the Session owner.
			notifications.SetNotificationString([]rune("Timed out waiting for server..."), true, true)
			r.connectingTimedOut = true
			changed = true
		case sinceStart > 250 && !r.connectingTimedOut:
			// Stock re-sets the non-permanent connecting message on every main
			// loop wake while still connecting. Re-setting also extends its
			// one-second expiration, so it cannot vanish mid-connection.
			message := connectingNotification(r.udpPort)
			changed = string(notifications.NotificationString()) != message
			notifications.SetNotificationString([]rune(message), false, true)
		}
	}

	waitTime := r.overlays.WaitTime()
	due := changed || now >= r.nextOverlayDraw
	// Once a count-up is active, stock wakes and redraws every count-up
	// interval even though wait_time() reports the interval, not the time
	// remaining until the next one.
	if !due && waitTime <= 1000 && now-r.lastOverlayDraw >= uint64(waitTime) {
		due = true
	}
	if !due {
		r.mu.Unlock()
		return nil
	}
	if waitTime < 1 {
		waitTime = 1
	}
	r.lastOverlayDraw = now
	r.nextOverlayDraw = now + uint64(waitTime)
	r.mu.Unlock()
	return r.emitLatest()
}

// translateInput applies the client's forced DECCKM state and the remote
// terminal's actual cursor-key mode to user bytes, exactly as
// Terminal::UserInput does.
func (r *terminalRenderer) translateInput(p []byte) []byte {
	r.mu.Lock()
	applicationMode := false
	if r.latest != nil {
		applicationMode = r.latest.Framebuffer().DS.ApplicationModeCursorKeys
	}
	r.mu.Unlock()

	out := make([]byte, 0, len(p))
	for _, b := range p {
		out = append(out, r.userInput.Input(b, applicationMode)...)
	}
	return out
}

// close mirrors STMClient::shutdown: clear the notification and title overlay,
// draw one last frame so removing the "[mosh] " prefix reaches the terminal,
// then queue Display::close. It also releases a reader blocked waiting for a
// size handler. The queue's final push is not bounded, so finishing a Session
// cannot block behind an unread terminal.
func (r *terminalRenderer) close() {
	r.closeOnce.Do(func() {
		close(r.closed)

		r.mu.Lock()
		initialized := r.initialized
		var shutdownFrame []byte
		if initialized && r.latest != nil {
			r.connectingTimedOut = false
			r.overlays.Notifications().SetNotificationString(nil, false, true)
			r.overlays.Notifications().ServerHeard(r.clock())
			r.overlays.SetTitlePrefix(nil)
			target := r.latest.CloneFramebuffer()
			r.overlays.Apply(target)
			shutdownFrame = []byte(r.display.NewFrame(true, r.lastDrawn, target))
			r.lastDrawn = target
		}
		r.mu.Unlock()

		if initialized {
			r.queue.pushFinal(shutdownFrame)
			r.queue.pushFinal([]byte(r.display.Close()))
		}
	})
}
