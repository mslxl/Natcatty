package sessionui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/ui/locales"
)

// defaultTrustTimeout matches the original host-key request TTL. An unanswered
// prompt is rejected so a stale modal cannot outlive its connection attempt.
const defaultTrustTimeout = 2 * time.Minute

// TrustPrompt is the native host-key confirmation. Confirm is called from the
// SSH dial goroutine and blocks until the user trusts or rejects the key. The
// UI reads Current on the UI thread and calls Answer. The queue is guarded so
// concurrent connections cannot corrupt it.
type TrustPrompt struct {
	mu      sync.Mutex
	queue   []*trustRequest
	redraw  func()
	timeout time.Duration
}

type trustRequest struct {
	key    domainconn.Key
	answer chan appconnection.PromptDecision
}

// TrustOption customizes a TrustPrompt.
type TrustOption func(*TrustPrompt)

// WithTrustRedrawRequest sets the function called when a prompt is queued or
// answered. The app wires it to the main window's Invalidate.
func WithTrustRedrawRequest(fn func()) TrustOption {
	return func(p *TrustPrompt) { p.redraw = fn }
}

// WithTrustTimeout overrides how long an unanswered prompt waits. Tests use a
// short value; the app keeps the original two minutes.
func WithTrustTimeout(timeout time.Duration) TrustOption {
	return func(p *TrustPrompt) {
		if timeout > 0 {
			p.timeout = timeout
		}
	}
}

// NewTrustPrompt returns an empty prompt queue.
func NewTrustPrompt(opts ...TrustOption) *TrustPrompt {
	prompt := &TrustPrompt{timeout: defaultTrustTimeout}
	for _, opt := range opts {
		opt(prompt)
	}
	return prompt
}

var _ appconnection.Prompter = (*TrustPrompt)(nil)

// Confirm queues key and waits for the user's decision. Cancelling ctx removes
// the prompt and unblocks the caller, which aborts the connection.
func (p *TrustPrompt) Confirm(ctx context.Context, key domainconn.Key) (appconnection.PromptDecision, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.PromptDecision{}, err
	}

	request := &trustRequest{key: key, answer: make(chan appconnection.PromptDecision, 1)}
	p.mu.Lock()
	p.queue = append(p.queue, request)
	p.mu.Unlock()
	p.requestRedraw()

	timer := time.NewTimer(p.timeout)
	defer timer.Stop()

	select {
	case decision := <-request.answer:
		return decision, nil
	case <-ctx.Done():
		p.remove(request)
		return appconnection.PromptDecision{}, ctx.Err()
	case <-timer.C:
		p.remove(request)
		return appconnection.PromptDecision{}, &appconnection.Error{
			Kind:  appconnection.KindHostKey,
			Cause: fmt.Errorf("%w: trust prompt expired", appconnection.ErrHostKeyRejected),
		}
	}
}

// Current returns the key the user must decide about. It reports false when no
// prompt is queued.
func (p *TrustPrompt) Current() (domainconn.Key, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return domainconn.Key{}, false
	}
	return p.queue[0].key, true
}

// Pending reports how many trust decisions are waiting.
func (p *TrustPrompt) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// Answer resolves the oldest prompt and returns whether one was waiting.
func (p *TrustPrompt) Answer(decision appconnection.PromptDecision) bool {
	p.mu.Lock()
	if len(p.queue) == 0 {
		p.mu.Unlock()
		return false
	}
	request := p.queue[0]
	p.queue = p.queue[1:]
	p.mu.Unlock()

	request.answer <- decision
	p.requestRedraw()
	return true
}

func (p *TrustPrompt) remove(request *trustRequest) {
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

func (p *TrustPrompt) requestRedraw() {
	if p.redraw != nil {
		p.redraw()
	}
}

// TrustView renders the queued host-key decision as a modal. An unknown key and
// a changed key have distinct titles and bodies; a changed key also shows the
// fingerprint the user trusted before, so the two cases cannot be confused.
func TrustView(p *TrustPrompt) func(*ui.Context) {
	return func(c *ui.Context) {
		key, ok := p.Current()
		if !ok {
			return
		}
		m := locales.In(c.Locale())
		t := c.Theme()

		open := true
		ui.Modal(c, &open, func() {
			ui.Column(c).Width(520).Padding(20).Gap(10).Background(t.Surface).Children(func() {
				title := m.HostKeyPromptUnknownTitle()
				body := m.HostKeyPromptUnknownBody(key.Hostname)
				if key.Status == domainconn.StatusChanged {
					title = m.HostKeyPromptChangedTitle()
					body = m.HostKeyPromptChangedBody(key.Hostname)
				}
				ui.Text(c, title).FontSize(18).Bold().TextColor(t.Danger)
				ui.Text(c, body).TextColor(t.TextMuted)
				ui.Text(c, m.HostKeyPromptFingerprint(key.KeyType, key.Fingerprint)).FontSize(12)
				if key.Status == domainconn.StatusChanged && key.ExpectedFingerprint != "" {
					ui.Text(c, m.HostKeyPromptExpected(key.ExpectedFingerprint)).FontSize(12).TextColor(t.Danger)
					ui.Text(c, m.HostKeyPromptChangedHint()).FontSize(12).TextColor(t.Danger)
				}

				ui.Row(c).Gap(8).Children(func() {
					if ui.PrimaryButton(c, m.HostKeyPromptTrust()).Clicked() {
						p.Answer(appconnection.PromptDecision{Trust: true, Persist: true})
						c.Invalidate()
					}
					if ui.Button(c, m.HostKeyPromptReject()).Clicked() {
						p.Answer(appconnection.PromptDecision{})
						c.Invalidate()
					}
				})
			})
		})
	}
}
