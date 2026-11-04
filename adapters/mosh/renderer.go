// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"fmt"
	"os"
	"sync"

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

	// mu guards the terminal state shared by the socket reader and input
	// translation.
	mu        sync.Mutex
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
}

// defaultTerminalRendererOptions returns the production renderer settings.
func defaultTerminalRendererOptions() terminalRendererOptions {
	options := terminalRendererOptions{Display: terminal.DefaultDisplayOptions()}
	if !noTitlePrefix() {
		options.TitlePrefix = []rune("[mosh] ")
	}
	return options
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
	initial := terminal.NewComplete(cols, rows)
	overlays := terminal.NewOverlayManager(terminal.MonotonicMillis)
	if len(options.TitlePrefix) > 0 {
		overlays.SetTitlePrefix(options.TitlePrefix)
	}
	return &terminalRenderer{
		queue:        queue,
		display:      terminal.NewDisplay(options.Display),
		overlays:     overlays,
		states:       map[uint64]*terminal.Complete{0: initial},
		latest:       initial,
		lastDrawn:    terminal.NewFramebuffer(cols, rows),
		userInput:    terminal.NewUserInput(),
		handlerReady: make(chan struct{}),
		closed:       make(chan struct{}),
		screenWidth:  cols,
		screenHeight: rows,
	}
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
		}
		for num := range r.states {
			if num < state.ThrowawayNum && num != state.Num {
				delete(r.states, num)
			}
		}
	}
	return nil
}

// emitLatest draws the latest remote state through the stock overlay order and
// the Display encoder. A remote size change is signalled to the Session before
// any bytes encoded at the new size are queued.
func (r *terminalRenderer) emitLatest() error {
	r.mu.Lock()
	latest := r.latest
	if latest == nil {
		r.mu.Unlock()
		return nil
	}
	width, height := latest.Framebuffer().DS.Size()
	r.mu.Unlock()

	if width != r.screenWidth || height != r.screenHeight {
		// Every frame already queued was encoded for the old size. The
		// Session uses this offset to deliver those bytes before applying
		// the new size to MyGo.
		if !r.requestScreenSize(width, height, r.queue.pushedBytes()) {
			return nil
		}
		r.mu.Lock()
		r.screenWidth, r.screenHeight = width, height
		r.mu.Unlock()
	}

	r.mu.Lock()
	target := r.latest.CloneFramebuffer()
	r.overlays.Notifications().ServerHeard(terminal.MonotonicMillis())
	r.overlays.Apply(target)
	frame := r.display.NewFrame(r.initialized, r.lastDrawn, target)
	r.lastDrawn = target
	r.mu.Unlock()
	return r.queue.push([]byte(frame))
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
			r.overlays.Notifications().SetNotificationString(nil, false, true)
			r.overlays.Notifications().ServerHeard(terminal.MonotonicMillis())
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
