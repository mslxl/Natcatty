package session

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"image"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/domain/model"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
)

// fakeDisplay is a DisplaySession the tests drive by hand. Sends are recorded
// without taking a port lock, so the concurrent-send test proves the wrapper,
// not the fake, serializes them.
type fakeDisplay struct {
	events chan DisplayEvent

	mu         sync.Mutex
	active     int
	maxActive  int
	keys       []uint32
	physical   [][2]uint32
	pointers   [][2]int
	cutTexts   []string
	updates    []bool
	resize     int
	resizeErr  error
	closeCount int
	// resizeGate, when non-nil, blocks SetDesktopSize until it is closed.
	// resizeEntered, when non-nil, receives once when the call has started.
	resizeGate    chan struct{}
	resizeEntered chan struct{}

	closeOnce sync.Once
}

func newFakeDisplay() *fakeDisplay {
	return &fakeDisplay{events: make(chan DisplayEvent, 16)}
}

func (d *fakeDisplay) Events() <-chan DisplayEvent { return d.events }

func (d *fakeDisplay) enter() {
	d.mu.Lock()
	d.active++
	if d.active > d.maxActive {
		d.maxActive = d.active
	}
	d.mu.Unlock()
}

func (d *fakeDisplay) leave() {
	d.mu.Lock()
	d.active--
	d.mu.Unlock()
}

func (d *fakeDisplay) SendKey(keysym uint32, pressed bool) error {
	d.enter()
	defer d.leave()
	d.mu.Lock()
	d.keys = append(d.keys, keysym)
	d.mu.Unlock()
	return nil
}

func (d *fakeDisplay) SendPhysicalKey(keysym uint32, keyNumber uint32, pressed bool) error {
	d.enter()
	defer d.leave()
	d.mu.Lock()
	d.keys = append(d.keys, keysym)
	d.physical = append(d.physical, [2]uint32{keysym, keyNumber})
	d.mu.Unlock()
	return nil
}

func (d *fakeDisplay) SendPointer(x, y int, mask uint8) error {
	d.enter()
	defer d.leave()
	d.mu.Lock()
	d.pointers = append(d.pointers, [2]int{x, y})
	d.mu.Unlock()
	return nil
}

func (d *fakeDisplay) SendCutText(text string) error {
	d.enter()
	defer d.leave()
	d.mu.Lock()
	d.cutTexts = append(d.cutTexts, text)
	d.mu.Unlock()
	return nil
}

func (d *fakeDisplay) RequestUpdate(incremental bool) error {
	d.enter()
	defer d.leave()
	d.mu.Lock()
	d.updates = append(d.updates, incremental)
	d.mu.Unlock()
	return nil
}

func (d *fakeDisplay) SetDesktopSize(width, height int) error {
	d.enter()
	defer d.leave()
	d.mu.Lock()
	entered, gate := d.resizeEntered, d.resizeGate
	d.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}
	d.mu.Lock()
	d.resize++
	err := d.resizeErr
	d.mu.Unlock()
	return err
}

func (d *fakeDisplay) Close() error {
	d.mu.Lock()
	d.closeCount++
	d.mu.Unlock()
	d.closeOnce.Do(func() { close(d.events) })
	return nil
}

func (d *fakeDisplay) publish(event DisplayEvent) {
	d.mu.Lock()
	closed := d.closeCount > 0
	d.mu.Unlock()
	if closed {
		return
	}
	d.events <- event
}

// fakeDisplayTransport is a Transport that carries a DisplaySession. Its byte
// methods are never used by a VNC Session; the counters prove that.
type fakeDisplayTransport struct {
	display *fakeDisplay
	closed  chan struct{}
	once    sync.Once
	reads   int
	writes  int
	resizes int
	mu      sync.Mutex
}

func newFakeDisplayTransport(display *fakeDisplay) *fakeDisplayTransport {
	return &fakeDisplayTransport{display: display, closed: make(chan struct{})}
}

func (t *fakeDisplayTransport) Read([]byte) (int, error) {
	t.mu.Lock()
	t.reads++
	t.mu.Unlock()
	<-t.closed
	return 0, io.EOF
}

func (t *fakeDisplayTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.writes++
	t.mu.Unlock()
	return len(p), nil
}

func (t *fakeDisplayTransport) Resize(int, int) error {
	t.mu.Lock()
	t.resizes++
	t.mu.Unlock()
	return nil
}

func (t *fakeDisplayTransport) Display() DisplaySession { return t.display }

func (t *fakeDisplayTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return t.display.Close()
}

func (t *fakeDisplayTransport) counters() (reads, writes, resizes int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reads, t.writes, t.resizes
}

type displayFactory struct {
	mu         sync.Mutex
	transports []*fakeDisplayTransport
	// errs supplies an error for a given Open, so a reconnect can fail with a
	// typed transport failure instead of handing back another display.
	errs  []error
	calls int
	spec  StartSpec
}

func (f *displayFactory) Open(_ context.Context, spec StartSpec) (Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.spec = spec
	index := f.calls - 1
	if index < len(f.errs) && f.errs[index] != nil {
		return nil, f.errs[index]
	}
	if index >= len(f.transports) {
		index = len(f.transports) - 1
	}
	if index < 0 {
		return nil, errors.New("no fake display transport")
	}
	return f.transports[index], nil
}

func (f *displayFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// countingCaptureHook records whether Open tried to install a shell hook. A
// VNC Session must never plan a terminal hook.
type countingCaptureHook struct {
	mu    sync.Mutex
	calls int
}

func (h *countingCaptureHook) Plan(spec StartSpec) (CaptureHookPlan, bool) {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	return CaptureHookPlan{Spec: spec}, false
}

func (h *countingCaptureHook) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func openVNC(t *testing.T, factory Factory, opts ...Option) *Session {
	t.Helper()
	configuration := modelvnc.SessionConfig{
		Hostname:      "desk.local",
		Port:          5901,
		Username:      "viewer",
		Password:      "secret",
		Configuration: modelvnc.DefaultConfig(),
		Route:         modelvnc.Route{Jumps: []model.HostID{"jump-1"}},
	}
	session, err := NewService(factory, opts...).Open(context.Background(), StartSpec{
		Kind:      KindVNC,
		Label:     "desk",
		VNC:       &configuration,
		Reconnect: &ReconnectPolicy{Delay: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Open VNC: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestStartSpecCarriesTheVNCStartSection proves the kind-selected section
// reaches the factory normalized, with every field the ticket names.
func TestStartSpecCarriesTheVNCStartSection(t *testing.T) {
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(newFakeDisplay()),
	}}
	_ = openVNC(t, factory)

	spec := factory.spec
	if spec.Kind != KindVNC || spec.VNC == nil {
		t.Fatalf("factory spec = %+v, want a VNC section", spec)
	}
	if spec.VNC.Hostname != "desk.local" || spec.VNC.Port != 5901 ||
		spec.VNC.Username != "viewer" || spec.VNC.Password != "secret" {
		t.Fatalf("VNC endpoint = %+v, want the complete endpoint", spec.VNC)
	}
	if spec.VNC.Configuration.FitMode != modelvnc.Fit ||
		!spec.VNC.Configuration.Shared {
		t.Fatalf("VNC configuration = %+v, want normalized defaults", spec.VNC.Configuration)
	}
	if len(spec.VNC.Route.Jumps) != 1 || spec.VNC.Route.Jumps[0] != "jump-1" {
		t.Fatalf("VNC route = %+v, want the Host chain", spec.VNC.Route)
	}
}

// TestFakeDisplayDrivesStartEventsAndStop covers the port's lifecycle: the
// Session starts the display, forwards events to one consumer, and closes the
// Session event channel exactly once when it ends.
func TestFakeDisplayDrivesStartEventsAndStop(t *testing.T) {
	display := newFakeDisplay()
	transport := newFakeDisplayTransport(display)
	factory := &displayFactory{transports: []*fakeDisplayTransport{transport}}
	session := openVNC(t, factory)

	display.publish(DisplayEvent{Kind: DisplayEventResize, Width: 1920, Height: 1080})
	display.publish(DisplayEvent{Kind: DisplayEventDesktopName, Text: "lab-desktop"})
	display.publish(DisplayEvent{Kind: DisplayEventBell})

	var got []DisplayEventKind
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < 3 && time.Now().Before(deadline) {
		select {
		case event := <-session.DisplayEvents():
			got = append(got, event.Kind)
			if event.Kind == DisplayEventDesktopName && session.Snapshot().Title != "lab-desktop" {
				t.Fatalf("snapshot title = %q, want the desktop name", session.Snapshot().Title)
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(got) != 3 {
		t.Fatalf("received %d events (%v), want 3", len(got), got)
	}
	if snapshot := session.Snapshot(); snapshot.DisplayWidth != 1920 || snapshot.DisplayHeight != 1080 {
		t.Fatalf("display size = %dx%d, want 1920x1080",
			snapshot.DisplayWidth, snapshot.DisplayHeight)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForDone(t, session)

	// Draining after Done proves the Session stream closed, and the fake's
	// once-guarded channel proves Close ran exactly once.
	for range session.DisplayEvents() {
		t.Fatal("a display event arrived after the Session ended")
	}
	display.mu.Lock()
	closeCount := display.closeCount
	display.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("display Close ran %d times, want once", closeCount)
	}
	if reads, writes, resizes := transport.counters(); reads != 0 || writes != 0 || resizes != 0 {
		t.Fatalf("VNC used the byte stream: reads=%d writes=%d resizes=%d", reads, writes, resizes)
	}
}

// TestConcurrentDisplaySendsAreSerialized proves the port's threading
// contract: many goroutines may send, no send overlaps another, and every
// call arrives. Run under -race.
func TestConcurrentDisplaySendsAreSerialized(t *testing.T) {
	display := newFakeDisplay()
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(display),
	}}
	session := openVNC(t, factory)

	const senders = 32
	const calls = 20
	var wg sync.WaitGroup
	for sender := 0; sender < senders; sender++ {
		wg.Add(1)
		go func(sender int) {
			defer wg.Done()
			for call := 0; call < calls; call++ {
				_ = session.SendKey(uint32(sender*100+call), true)
				_ = session.SendPointer(sender, call, 1)
			}
		}(sender)
	}
	wg.Wait()

	display.mu.Lock()
	defer display.mu.Unlock()
	if display.maxActive != 1 {
		t.Fatalf("max concurrent sends = %d, want 1", display.maxActive)
	}
	if len(display.keys) != senders*calls {
		t.Fatalf("received %d keys, want %d", len(display.keys), senders*calls)
	}
	if len(display.pointers) != senders*calls {
		t.Fatalf("received %d pointers, want %d", len(display.pointers), senders*calls)
	}
	// Serialization must preserve each sender's own call order: one goroutine's
	// sends may interleave with other senders', never reorder among themselves.
	nextKey := make([]int, senders)
	for _, keysym := range display.keys {
		sender, call := int(keysym)/100, int(keysym)%100
		if sender >= senders || call != nextKey[sender] {
			t.Fatalf("sender %d key %d arrived out of order (next %d)", sender, call, nextKey[sender])
		}
		nextKey[sender]++
	}
	nextPointer := make([]int, senders)
	for _, pointer := range display.pointers {
		sender, call := pointer[0], pointer[1]
		if sender >= senders || call != nextPointer[sender] {
			t.Fatalf("sender %d pointer %d arrived out of order (next %d)", sender, call, nextPointer[sender])
		}
		nextPointer[sender]++
	}
}

// TestDisplayFrameIsAnImmutableSnapshot proves a frame never aliases the
// decoder buffer.
func TestDisplayFrameIsAnImmutableSnapshot(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Pix[0], source.Pix[1], source.Pix[2], source.Pix[3] = 1, 2, 3, 4
	frame := NewDisplayFrame(source, source.Bounds())
	source.Pix[0] = 99
	source.Pix[1] = 98
	if frame.Image.Pix[0] != 1 || frame.Image.Pix[1] != 2 {
		t.Fatalf("frame pixels = %d,%d, want the original 1,2", frame.Image.Pix[0], frame.Image.Pix[1])
	}

	clone := frame.Clone()
	frame.Image.Pix[0] = 77
	if clone.Image.Pix[0] != 1 {
		t.Fatalf("clone pixel = %d, want an independent 1", clone.Image.Pix[0])
	}
}

// TestDisplayEmitterBoundsFramesToThirtyPerSecond proves frames coalesce while
// the newest frame is eventually delivered.
func TestDisplayEmitterBoundsFramesToThirtyPerSecond(t *testing.T) {
	emitter := NewDisplayEmitter()
	source := image.NewRGBA(image.Rect(0, 0, 1, 1))

	type arrival struct {
		at  time.Time
		seq uint32
	}
	received := make(chan arrival, 256)
	done := make(chan struct{})
	go func() {
		for event := range emitter.Events() {
			if event.Kind != DisplayEventFrame {
				continue
			}
			received <- arrival{at: time.Now(), seq: uint32(event.Frame.Image.Pix[0])}
		}
		close(done)
	}()

	const total = 300
	last := uint32(0)
	for seq := uint32(1); seq <= total; seq++ {
		source.Pix[0] = byte(seq)
		emitter.Publish(DisplayEvent{Kind: DisplayEventFrame, Frame: NewDisplayFrame(source, source.Bounds())})
		last = seq
		if seq%10 == 0 {
			time.Sleep(time.Millisecond)
		}
	}

	// Give the ticks a fixed measurement window; the poll checks a real
	// condition (elapsed time) with a hard deadline.
	measureUntil := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(measureUntil) {
		time.Sleep(5 * time.Millisecond)
	}
	emitter.Close()
	<-done
	close(received)

	var frames []arrival
	for arrival := range received {
		frames = append(frames, arrival)
	}
	if len(frames) == 0 {
		t.Fatal("no frames were delivered")
	}
	first := frames[0].at
	lastFrame := frames[len(frames)-1]
	elapsed := lastFrame.at.Sub(first).Seconds()
	if elapsed > 0 {
		rate := float64(len(frames)-1) / elapsed
		if rate > float64(MaxFramesPerSecond)+2 {
			t.Fatalf("frame rate = %.1f/s, want at most %d/s", rate, MaxFramesPerSecond)
		}
	}
	if lastFrame.seq != uint32(uint8(last)) {
		t.Fatalf("newest frame = %d, want %d; frames must not be lost permanently", lastFrame.seq, uint8(last))
	}
}

// TestDisplayFrameRateCapsABurstWithoutLoss covers the slow-consumer case:
// publishing far faster than 30 fps still delivers the latest state.
func TestDisplayFrameRateCapsABurstWithoutLoss(t *testing.T) {
	emitter := NewDisplayEmitter()
	defer emitter.Close()
	source := image.NewRGBA(image.Rect(0, 0, 1, 1))
	for seq := 0; seq < 1000; seq++ {
		source.Pix[0] = byte(seq)
		emitter.Publish(DisplayEvent{Kind: DisplayEventFrame, Frame: NewDisplayFrame(source, source.Bounds())})
	}
	emitter.Close()
	var last uint8
	var count int
	for event := range emitter.Events() {
		if event.Kind == DisplayEventFrame {
			count++
			last = event.Frame.Image.Pix[0]
		}
	}
	if count > MaxFramesPerSecond {
		t.Fatalf("delivered %d frames from one burst, want at most %d", count, MaxFramesPerSecond)
	}
	if want := byte(999 % 256); last != want {
		t.Fatalf("latest frame = %d, want %d", last, want)
	}
}

// TestDesktopResizeUnsupportedIsExplicit proves the port's resize contract.
func TestDesktopResizeUnsupportedIsExplicit(t *testing.T) {
	display := newFakeDisplay()
	display.resizeErr = ErrDesktopResizeUnsupported
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(display),
	}}
	session := openVNC(t, factory)

	if err := session.SetDesktopSize(1280, 720); !errors.Is(err, ErrDesktopResizeUnsupported) {
		t.Fatalf("SetDesktopSize = %v, want ErrDesktopResizeUnsupported", err)
	}

	display.mu.Lock()
	display.resizeErr = nil
	display.mu.Unlock()
	if err := session.SetDesktopSize(1280, 720); err != nil {
		t.Fatalf("supported SetDesktopSize = %v, want nil", err)
	}
}

// TestVNCSessionHidesByteStreamBehaviour proves a pixel Session exposes none
// of the terminal-only behaviours through its API.
func TestVNCSessionHidesByteStreamBehaviour(t *testing.T) {
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(newFakeDisplay()),
	}}
	hook := &countingCaptureHook{}
	session := openVNC(t, factory, WithCaptureHook(hook))

	if session.ByteStream() {
		t.Fatal("VNC ByteStream = true, want false")
	}
	if calls := hook.callCount(); calls != 0 {
		t.Fatalf("capture hook planned %d times for VNC, want 0", calls)
	}
	before := session.Snapshot()
	if err := session.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	after := session.Snapshot()
	if after.Cols != before.Cols || after.Rows != before.Rows {
		t.Fatalf("terminal resize changed the VNC snapshot: %dx%d -> %dx%d",
			before.Cols, before.Rows, after.Cols, after.Rows)
	}
	if session.HistoryScope() != "" {
		t.Fatalf("HistoryScope = %q, want empty for VNC", session.HistoryScope())
	}
	if session.InsertInput([]byte("ls\n")) {
		t.Fatal("InsertInput accepted bytes for a VNC Session")
	}
	if err := session.StartLogging(LogOptions{Directory: t.TempDir()}); !errors.Is(err, ErrNoByteStream) {
		t.Fatalf("StartLogging = %v, want ErrNoByteStream", err)
	}
	if _, err := session.Command(context.Background(), "ls", CommandOptions{}); !errors.Is(err, ErrNoByteStream) {
		t.Fatalf("Command = %v, want ErrNoByteStream", err)
	}
	if _, err := session.RawExec(context.Background(), "ls", RawExecOptions{}); !errors.Is(err, ErrNoByteStream) {
		t.Fatalf("RawExec = %v, want ErrNoByteStream", err)
	}
	if _, err := session.AttachView(); !errors.Is(err, ErrNoByteStream) {
		t.Fatalf("AttachView = %v, want ErrNoByteStream", err)
	}
	if _, err := session.Observe(); !errors.Is(err, ErrNoByteStream) {
		t.Fatalf("Observe = %v, want ErrNoByteStream", err)
	}
	if text := session.ScreenText(); text.Text != "" {
		t.Fatalf("ScreenText = %q, want no scrollback", text.Text)
	}
	if session.SetEncoding(session.Encoding()) {
		t.Fatal("SetEncoding accepted a change for a VNC Session")
	}
}

func (d *fakeDisplay) sends() (keys int, pointers int, cutTexts int, updates int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.keys), len(d.pointers), len(d.cutTexts), len(d.updates)
}

// TestVNCSendMethodsReachTheDisplay proves the Session's send facade forwards
// every operation the port defines.
func TestVNCSendMethodsReachTheDisplay(t *testing.T) {
	display := newFakeDisplay()
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(display),
	}}
	session := openVNC(t, factory)

	if err := session.SendKey(0xff0d, true); err != nil {
		t.Fatalf("SendKey: %v", err)
	}
	if err := session.SendPointer(10, 20, 1); err != nil {
		t.Fatalf("SendPointer: %v", err)
	}
	if err := session.SendCutText("clip"); err != nil {
		t.Fatalf("SendCutText: %v", err)
	}
	if err := session.RequestUpdate(false); err != nil {
		t.Fatalf("RequestUpdate: %v", err)
	}
	keys, pointers, cutTexts, updates := display.sends()
	if keys != 1 || pointers != 1 || cutTexts != 1 || updates != 1 {
		t.Fatalf("display sends = %d/%d/%d/%d, want 1 each", keys, pointers, cutTexts, updates)
	}
}

// TestVNCSessionReconnectsAfterANetworkDrop proves the SSH-style policy.
func TestVNCSessionReconnectsAfterANetworkDrop(t *testing.T) {
	first := newFakeDisplay()
	second := newFakeDisplay()
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(first),
		newFakeDisplayTransport(second),
	}}
	session := openVNC(t, factory)

	first.publish(DisplayEvent{Kind: DisplayEventError, Err: errors.New("network dropped")})
	waitForCondition(t, "the VNC reconnect", func() bool { return factory.callCount() >= 2 })
	waitForCondition(t, "the reconnected Session", func() bool {
		return session.Snapshot().Connection == ConnectionStatusActive
	})
	if factory.spec.Kind != KindVNC {
		t.Fatalf("reconnect spec kind = %q, want VNC", factory.spec.Kind)
	}
}

// TestVNCSessionRefusesToRetryAnAuthenticationFailure proves a permanent error
// stops the reconnect loop.
func TestVNCSessionRefusesToRetryAnAuthenticationFailure(t *testing.T) {
	first := newFakeDisplay()
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(first),
		newFakeDisplayTransport(newFakeDisplay()),
	}}
	session := openVNC(t, factory)

	first.publish(DisplayEvent{Kind: DisplayEventError, Err: permanentTestError{}})
	waitForDone(t, session)

	if calls := factory.callCount(); calls != 1 {
		t.Fatalf("factory opened %d transports, want no retry after auth failure", calls)
	}
	snapshot := session.Snapshot()
	if snapshot.Status != StatusExited || snapshot.Connection != ConnectionStatusError ||
		snapshot.EndReason != EndReasonAuthFailed {
		t.Fatalf("final snapshot = %+v, want an authentication failure", snapshot)
	}

	// A retry would land after the policy's 10ms delay. Check the real
	// condition with a hard deadline instead of assuming one sleep is enough.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if factory.callCount() != 1 {
			t.Fatal("a retry happened after an authentication failure")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestVNCSessionStopsWhenAReconnectIsRejected proves the adapter's real
// authentication error type (appconnection.Error with KindAuth) ends the
// reconnect loop instead of retrying a rejected VNC password.
func TestVNCSessionStopsWhenAReconnectIsRejected(t *testing.T) {
	first := newFakeDisplay()
	authFailure := &appconnection.Error{
		Kind: appconnection.KindAuth, Cause: errors.New("wrong VNC password"),
	}
	factory := &displayFactory{
		transports: []*fakeDisplayTransport{newFakeDisplayTransport(first)},
		errs:       []error{nil, authFailure},
	}
	session := openVNC(t, factory)

	first.publish(DisplayEvent{Kind: DisplayEventError, Err: errors.New("network dropped")})
	waitForCondition(t, "the rejected reconnect", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
	if calls := factory.callCount(); calls != 2 {
		t.Fatalf("factory opened %d transports, want one reconnect then a permanent stop", calls)
	}
	if snapshot := session.Snapshot(); snapshot.Status != StatusExited ||
		snapshot.Connection != ConnectionStatusError {
		t.Fatalf("final snapshot = %+v, want an exited Session with a connection error", snapshot)
	}

	// The retry delay is 10ms; a repeat would land inside this window.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if factory.callCount() != 2 {
			t.Fatal("the Session retried a rejected VNC password")
		}
		time.Sleep(time.Millisecond)
	}
}

// notifyingDisplayTransport is a pixel transport that also reports non-fatal
// notices, as the app's plaintext-warning wrapper does.
type notifyingDisplayTransport struct {
	*fakeDisplayTransport
	notices chan Notice
}

func (t *notifyingDisplayTransport) Notices() <-chan Notice { return t.notices }

// fixedDisplayFactory hands one transport to every Open.
type fixedDisplayFactory struct{ transport Transport }

func (f fixedDisplayFactory) Open(context.Context, StartSpec) (Transport, error) {
	return f.transport, nil
}

// TestVNCSessionSurfacesThePlaintextNotice proves a pixel transport's notices
// reach the Session snapshot.
func TestVNCSessionSurfacesThePlaintextNotice(t *testing.T) {
	display := newFakeDisplay()
	notices := make(chan Notice, 1)
	notices <- Notice{Code: NoticeVNCPlaintext}
	close(notices)
	transport := &notifyingDisplayTransport{
		fakeDisplayTransport: newFakeDisplayTransport(display), notices: notices,
	}
	session := openVNC(t, fixedDisplayFactory{transport: transport})
	t.Cleanup(func() { _ = session.Close() })
	waitForCondition(t, "the plaintext notice", func() bool {
		notice := session.Snapshot().Notice
		return notice != nil && notice.Code == NoticeVNCPlaintext
	})
}

// vncStartSpec returns the minimal VNC StartSpec the lifecycle tests open.
func vncStartSpec() StartSpec {
	configuration := modelvnc.SessionConfig{
		Hostname:      "desk.local",
		Port:          5901,
		Username:      "viewer",
		Password:      "secret",
		Configuration: modelvnc.DefaultConfig(),
	}
	return StartSpec{
		Kind:      KindVNC,
		Label:     "desk",
		VNC:       &configuration,
		Reconnect: &ReconnectPolicy{Delay: 10 * time.Millisecond},
	}
}

// TestExplicitCloseClearsDisplay proves an explicit Close ends exactly like a
// natural transport end: Display() reports nil after Done, so every send
// reports ErrNoDisplay instead of reaching the closed adapter.
func TestExplicitCloseClearsDisplay(t *testing.T) {
	display := newFakeDisplay()
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(display),
	}}
	session := openVNC(t, factory)
	if session.Display() == nil {
		t.Fatal("Display() = nil while the Session is live")
	}
	keysBefore, pointersBefore, cutTextsBefore, updatesBefore := display.sends()

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForDone(t, session)

	if display := session.Display(); display != nil {
		t.Fatalf("Display() = %v after an explicit Close, want nil", display)
	}
	if err := session.SendKey(0xff0d, true); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SendKey after Close = %v, want ErrNoDisplay", err)
	}
	if err := session.SendPointer(1, 2, 1); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SendPointer after Close = %v, want ErrNoDisplay", err)
	}
	if err := session.SendCutText("x"); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SendCutText after Close = %v, want ErrNoDisplay", err)
	}
	if err := session.RequestUpdate(false); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("RequestUpdate after Close = %v, want ErrNoDisplay", err)
	}
	if err := session.SetDesktopSize(800, 600); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SetDesktopSize after Close = %v, want ErrNoDisplay", err)
	}
	keys, pointers, cutTexts, updates := display.sends()
	if keys != keysBefore || pointers != pointersBefore ||
		cutTexts != cutTextsBefore || updates != updatesBefore {
		t.Fatalf("sends after Close reached the adapter: %d/%d/%d/%d -> %d/%d/%d/%d",
			keysBefore, pointersBefore, cutTextsBefore, updatesBefore,
			keys, pointers, cutTexts, updates)
	}
}

// TestCloseAllClearsDisplay covers the app-shutdown path through Service.
func TestCloseAllClearsDisplay(t *testing.T) {
	display := newFakeDisplay()
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(display),
	}}
	service := NewService(factory)
	session, err := service.Open(context.Background(), vncStartSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	service.CloseAll()
	waitForDone(t, session)

	if display := session.Display(); display != nil {
		t.Fatalf("Display() = %v after CloseAll, want nil", display)
	}
	if err := session.SendKey(0xff0d, true); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SendKey after CloseAll = %v, want ErrNoDisplay", err)
	}
}

// TestSerializedDisplayRejectsSendsAfterClose proves the wrapper itself stops
// reaching the adapter once closed, independent of the Session clearing it.
func TestSerializedDisplayRejectsSendsAfterClose(t *testing.T) {
	inner := newFakeDisplay()
	display := NewSerializedDisplay(inner)
	if err := display.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := display.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if err := display.SendKey(0xff0d, true); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SendKey = %v, want ErrNoDisplay", err)
	}
	if err := display.SendPointer(1, 2, 1); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SendPointer = %v, want ErrNoDisplay", err)
	}
	if err := display.SendCutText("clip"); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SendCutText = %v, want ErrNoDisplay", err)
	}
	if err := display.RequestUpdate(true); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("RequestUpdate = %v, want ErrNoDisplay", err)
	}
	if err := display.SetDesktopSize(640, 480); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("SetDesktopSize = %v, want ErrNoDisplay", err)
	}
	keys, pointers, cutTexts, updates := inner.sends()
	if keys != 0 || pointers != 0 || cutTexts != 0 || updates != 0 {
		t.Fatalf("post-close sends reached the adapter: %d/%d/%d/%d", keys, pointers, cutTexts, updates)
	}
	inner.mu.Lock()
	closeCount := inner.closeCount
	inner.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("adapter Close ran %d times, want once", closeCount)
	}
}

// TestSendKeyIsNotBlockedByAnOutstandingResize proves the wrapper does not hold
// its send mutex across the adapter's blocking resize wait: input keeps
// flowing while a slow server's reply is outstanding.
func TestSendKeyIsNotBlockedByAnOutstandingResize(t *testing.T) {
	inner := newFakeDisplay()
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	inner.resizeGate = gate
	inner.resizeEntered = entered
	display := NewSerializedDisplay(inner)

	resizeDone := make(chan error, 1)
	go func() { resizeDone <- display.SetDesktopSize(800, 600) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SetDesktopSize never reached the adapter")
	}

	keyDone := make(chan error, 1)
	go func() { keyDone <- display.SendKey(0xff0d, true) }()
	select {
	case err := <-keyDone:
		if err != nil {
			t.Fatalf("SendKey: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SendKey blocked while a desktop resize wait was outstanding")
	}
	inner.mu.Lock()
	keys := len(inner.keys)
	inner.mu.Unlock()
	if keys != 1 {
		t.Fatalf("adapter received %d keys, want 1 during the resize wait", keys)
	}

	close(gate)
	select {
	case err := <-resizeDone:
		if err != nil {
			t.Fatalf("SetDesktopSize: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetDesktopSize did not return after the gate opened")
	}
}

// TestCloseWaitsForAnInFlightResize proves Close does not return while a
// SetDesktopSize that already passed the open check is still inside the
// adapter, and that a resize starting afterwards never reaches the adapter.
func TestCloseWaitsForAnInFlightResize(t *testing.T) {
	inner := newFakeDisplay()
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	inner.resizeGate = gate
	inner.resizeEntered = entered
	display := NewSerializedDisplay(inner)

	resizeDone := make(chan error, 1)
	go func() { resizeDone <- display.SetDesktopSize(800, 600) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SetDesktopSize never reached the adapter")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- display.Close() }()
	select {
	case <-closeDone:
		t.Fatal("Close returned while a resize was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(gate)
	select {
	case err := <-resizeDone:
		if err != nil {
			t.Fatalf("SetDesktopSize: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetDesktopSize did not return")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the resize finished")
	}

	inner.mu.Lock()
	resizes, closes := inner.resize, inner.closeCount
	inner.mu.Unlock()
	if resizes != 1 {
		t.Fatalf("adapter resize calls = %d, want 1", resizes)
	}
	if closes != 1 {
		t.Fatalf("adapter Close calls = %d, want 1", closes)
	}

	// A resize that starts after Close must not reach the closed adapter.
	if err := display.SetDesktopSize(640, 480); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("post-close SetDesktopSize = %v, want ErrNoDisplay", err)
	}
	inner.mu.Lock()
	resizes = inner.resize
	inner.mu.Unlock()
	if resizes != 1 {
		t.Fatal("a post-close resize reached the adapter")
	}
}

// TestNaturalDisplayEndClosesTransportOnce proves teardown closes a transport
// (and therefore its DisplayProvider) exactly once even though the end handler
// and the final path both reach it.
func TestNaturalDisplayEndClosesTransportOnce(t *testing.T) {
	display := newFakeDisplay()
	factory := &displayFactory{transports: []*fakeDisplayTransport{
		newFakeDisplayTransport(display),
	}}
	session := openVNC(t, factory)

	display.publish(DisplayEvent{Kind: DisplayEventError, Err: permanentTestError{}})
	waitForDone(t, session)

	display.mu.Lock()
	closeCount := display.closeCount
	display.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("display Close ran %d times, want exactly once", closeCount)
	}
}

// reconnectRaceFactory blocks the second Open so a Close can land while the
// dial is in flight.
type reconnectRaceFactory struct {
	first  *fakeDisplayTransport
	second *fakeDisplayTransport

	dialing chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (f *reconnectRaceFactory) Open(context.Context, StartSpec) (Transport, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if call == 1 {
		return f.first, nil
	}
	f.once.Do(func() { close(f.dialing) })
	<-f.release
	return f.second, nil
}

// TestReconnectClosesTransportOpenedAfterClose proves a dial that completes
// after Close does not leak the freshly opened transport or its display.
func TestReconnectClosesTransportOpenedAfterClose(t *testing.T) {
	first := newFakeDisplay()
	second := newFakeDisplay()
	factory := &reconnectRaceFactory{
		first:   newFakeDisplayTransport(first),
		second:  newFakeDisplayTransport(second),
		dialing: make(chan struct{}),
		release: make(chan struct{}),
	}
	session := openVNC(t, factory)

	first.publish(DisplayEvent{Kind: DisplayEventError, Err: errors.New("network dropped")})
	waitForCondition(t, "the reconnect dial", func() bool {
		select {
		case <-factory.dialing:
			return true
		default:
			return false
		}
	})

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(factory.release)
	waitForDone(t, session)

	select {
	case <-factory.second.closed:
	default:
		t.Fatal("the transport opened after Close was never closed")
	}
	second.mu.Lock()
	closeCount := second.closeCount
	second.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("second display Close ran %d times, want once", closeCount)
	}
	if display := session.Display(); display != nil {
		t.Fatalf("Display() = %v after Close, want nil", display)
	}
}

// TestDisplayEmitterFinalFlushDeliversNewestFrameWhenBufferFull proves the
// final frame is not dropped when Close begins with a full output buffer.
func TestDisplayEmitterFinalFlushDeliversNewestFrameWhenBufferFull(t *testing.T) {
	emitter := NewDisplayEmitter()
	for i := 0; i < displayEventBuffer*2; i++ {
		emitter.Publish(DisplayEvent{Kind: DisplayEventBell})
	}
	waitForCondition(t, "a full emitter buffer", func() bool {
		return len(emitter.out) >= displayEventBuffer
	})

	source := image.NewRGBA(image.Rect(0, 0, 1, 1))
	source.Pix[0] = 42
	emitter.Publish(DisplayEvent{Kind: DisplayEventFrame, Frame: NewDisplayFrame(source, source.Bounds())})

	// Hold the consumer back so Close starts with the buffer still full, then
	// drain slowly enough to require the bounded wait, not an immediate slot.
	start := make(chan struct{})
	got := make(chan uint8, 1)
	go func() {
		<-start
		for event := range emitter.Events() {
			if event.Kind == DisplayEventFrame {
				got <- event.Frame.Image.Pix[0]
			}
			time.Sleep(time.Millisecond)
		}
	}()
	closed := make(chan struct{})
	go func() {
		emitter.Close()
		close(closed)
	}()
	time.Sleep(10 * time.Millisecond)
	close(start)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}

	select {
	case seq := <-got:
		if seq != 42 {
			t.Fatalf("final frame = %d, want 42", seq)
		}
	case <-time.After(time.Second):
		t.Fatal("the newest frame was dropped at Close with a full buffer")
	}
}

// TestDisplayEmitterCloseDoesNotDeadlockWithoutConsumer proves the bounded
// final flush still lets Close return when nothing drains the stream.
func TestDisplayEmitterCloseDoesNotDeadlockWithoutConsumer(t *testing.T) {
	emitter := NewDisplayEmitter()
	source := image.NewRGBA(image.Rect(0, 0, 1, 1))
	for i := 0; i < displayEventBuffer*2; i++ {
		emitter.Publish(DisplayEvent{Kind: DisplayEventBell})
	}
	emitter.Publish(DisplayEvent{Kind: DisplayEventFrame, Frame: NewDisplayFrame(source, source.Bounds())})

	done := make(chan struct{})
	go func() {
		emitter.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked waiting for a consumer that never drains")
	}
}

// TestDisplayEmitterPublishRacingCloseIsRejected stresses the Close/Publish
// interleaving: no event may be appended after the final flush captured the
// queue, where it would sit undrained forever.
func TestDisplayEmitterPublishRacingCloseIsRejected(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		emitter := NewDisplayEmitter()
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				emitter.Publish(DisplayEvent{Kind: DisplayEventBell})
			}
		}()
		emitter.Close()
		wg.Wait()
		for range emitter.Events() {
		}

		emitter.mu.Lock()
		leftoverEvents := len(emitter.events)
		leftoverFrame := emitter.frame != nil
		closed := emitter.closed
		emitter.mu.Unlock()
		if !closed {
			t.Fatal("Close did not mark the emitter closed")
		}
		if leftoverEvents != 0 || leftoverFrame {
			t.Fatalf("Publish left %d events (frame=%t) after Close",
				leftoverEvents, leftoverFrame)
		}
	}
}

// TestDisplayPortImportsNoUITypes enforces the port boundary mechanically.
func TestDisplayPortImportsNoUITypes(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "display.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse display.go: %v", err)
	}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if strings.HasPrefix(path, "github.com/mslxl/natcatty/ui") {
			t.Fatalf("display-session port imports UI package %q", path)
		}
	}
}
