package sessionui

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// defaultPlaintextTimeout bounds how long an unanswered unencrypted-connection
// confirmation holds the dial goroutine. It matches the host-key prompt: an
// abandoned modal must not keep a connection attempt alive forever.
const defaultPlaintextTimeout = 2 * time.Minute

// ErrPlaintextDeclined reports that the user rejected an unencrypted VNC
// connection. The stream is already open and the security type is known, but
// the caller aborts before any credential is sent and without recording an
// acknowledgement.
var ErrPlaintextDeclined = errors.New("session: unencrypted VNC connection declined")

// VNCPlaintextPrompt is the post-negotiation confirmation for an unrouted VNC
// Host whose plaintext warning was never acknowledged. ConfirmPlaintext is
// called from the transport's open goroutine after the security type is chosen
// and blocks until the user continues or cancels. The UI reads Current on the
// UI thread and calls Answer.
type VNCPlaintextPrompt struct {
	mu      sync.Mutex
	queue   []*plaintextRequest
	redraw  func()
	timeout time.Duration
}

type plaintextRequest struct {
	hostID model.HostID
	name   string
	answer chan bool
}

// VNCPlaintextPromptOption customizes a VNCPlaintextPrompt.
type VNCPlaintextPromptOption func(*VNCPlaintextPrompt)

// WithVNCPlaintextRedrawRequest sets the function called when a prompt is
// queued or answered. The app wires it to the main window's Invalidate.
func WithVNCPlaintextRedrawRequest(fn func()) VNCPlaintextPromptOption {
	return func(p *VNCPlaintextPrompt) { p.redraw = fn }
}

// WithVNCPlaintextTimeout overrides how long an unanswered prompt waits. Tests
// use a short value; the app keeps the two-minute default.
func WithVNCPlaintextTimeout(timeout time.Duration) VNCPlaintextPromptOption {
	return func(p *VNCPlaintextPrompt) {
		if timeout > 0 {
			p.timeout = timeout
		}
	}
}

// NewVNCPlaintextPrompt returns an empty prompt queue.
func NewVNCPlaintextPrompt(opts ...VNCPlaintextPromptOption) *VNCPlaintextPrompt {
	prompt := &VNCPlaintextPrompt{timeout: defaultPlaintextTimeout}
	for _, opt := range opts {
		opt(prompt)
	}
	return prompt
}

// ConfirmPlaintext queues a confirmation for the Host and waits for the
// user's decision. A false answer is ErrPlaintextDeclined; cancelling ctx
// removes the prompt and unblocks the caller.
func (p *VNCPlaintextPrompt) ConfirmPlaintext(ctx context.Context, hostID model.HostID, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	request := &plaintextRequest{hostID: hostID, name: name, answer: make(chan bool, 1)}
	p.mu.Lock()
	p.queue = append(p.queue, request)
	p.mu.Unlock()
	p.requestRedraw()

	timer := time.NewTimer(p.timeout)
	defer timer.Stop()

	select {
	case accepted := <-request.answer:
		if !accepted {
			return false, ErrPlaintextDeclined
		}
		return true, nil
	case <-ctx.Done():
		p.remove(request)
		return false, ctx.Err()
	case <-timer.C:
		p.remove(request)
		return false, errors.New("session: unencrypted VNC confirmation expired")
	}
}

// Current returns the Host name the user must decide about. It reports false
// when no prompt is queued.
func (p *VNCPlaintextPrompt) Current() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return "", false
	}
	return p.queue[0].name, true
}

// Pending reports how many confirmations are waiting.
func (p *VNCPlaintextPrompt) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// Answer resolves the oldest prompt and reports whether one was waiting.
func (p *VNCPlaintextPrompt) Answer(accepted bool) bool {
	p.mu.Lock()
	if len(p.queue) == 0 {
		p.mu.Unlock()
		return false
	}
	request := p.queue[0]
	p.queue = p.queue[1:]
	p.mu.Unlock()

	request.answer <- accepted
	p.requestRedraw()
	return true
}

func (p *VNCPlaintextPrompt) remove(request *plaintextRequest) {
	p.mu.Lock()
	for index, queued := range p.queue {
		if queued == request {
			p.queue = append(p.queue[:index], p.queue[index+1:]...)
			break
		}
	}
	p.mu.Unlock()
	p.requestRedraw()
}

func (p *VNCPlaintextPrompt) requestRedraw() {
	if p.redraw != nil {
		p.redraw()
	}
}

// VNCPlaintextView renders the queued confirmation as a modal. Continue is the
// explicit acknowledgement; Cancel aborts the connection without recording it.
func VNCPlaintextView(p *VNCPlaintextPrompt) func(*ui.Context) {
	return func(c *ui.Context) {
		name, ok := p.Current()
		if !ok {
			return
		}
		m := locales.In(c.Locale())
		t := c.Theme()

		open := true
		kit.DialogWith(c, &open, kit.DialogOptions{Role: kit.DialogSheet}, func() {
			ui.Column(c).Width(520).Padding(20).Gap(10).Background(t.Surface).Children(func() {
				ui.Text(c, m.ConnectionVNCPlaintextPromptTitle()).
					FontSize(kit.M(c).Font.Title).Bold().TextColor(uitheme.DangerText(t))
				ui.Text(c, m.ConnectionVNCPlaintextPromptBody(name)).TextColor(t.TextMuted)

				ui.Row(c).Gap(8).Children(func() {
					if ui.PrimaryButton(c, m.ConnectionVNCPlaintextPromptContinue()).Clicked() {
						p.Answer(true)
						c.Invalidate()
					}
					if ui.Button(c, m.ConnectionVNCPlaintextPromptCancel()).Clicked() {
						p.Answer(false)
						c.Invalidate()
					}
				})
			})
		}, nil)
	}
}
