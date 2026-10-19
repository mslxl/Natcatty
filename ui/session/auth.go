package sessionui

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/ui/locales"
)

// Prompt lifetimes match the original: a key passphrase request expires after
// two minutes and a keyboard-interactive round after five.
const (
	defaultPassphraseTimeout          = 2 * time.Minute
	defaultKeyboardInteractiveTimeout = 5 * time.Minute
)

// AuthPromptKind is the kind of interactive authentication request queued.
type AuthPromptKind string

const (
	// AuthPromptPassphrase asks for an encrypted SSH Key's passphrase.
	AuthPromptPassphrase AuthPromptKind = "passphrase"
	// AuthPromptKeyboardInteractive asks the server's ordered questions.
	AuthPromptKeyboardInteractive AuthPromptKind = "keyboard-interactive"
)

// AuthPromptView is the queued request the view renders.
type AuthPromptView struct {
	Kind       AuthPromptKind
	Passphrase appconnection.PassphraseRequest
	Keyboard   appconnection.KeyboardInteractiveRequest
}

// AuthPrompt is the native passphrase and keyboard-interactive prompt. The SSH
// dial goroutine blocks on the answer; the UI thread reads Current and submits
// an Answer. The queue is guarded so concurrent connections cannot interleave.
type AuthPrompt struct {
	mu                sync.Mutex
	queue             []*authPromptRequest
	redraw            func()
	passphraseTimeout time.Duration
	keyboardTimeout   time.Duration
}

// authPromptRequest keeps its own answer drafts so a queued request cannot
// overwrite the drafts of the request the user is currently editing.
type authPromptRequest struct {
	view    AuthPromptView
	answer  chan authPromptAnswer
	answers []string
}

type authPromptAnswer struct {
	passphrase appconnection.PassphraseDecision
	keyboard   appconnection.KeyboardInteractiveDecision
}

// AuthPromptOption customizes an AuthPrompt.
type AuthPromptOption func(*AuthPrompt)

// WithAuthPromptRedrawRequest sets the function called when a prompt is queued
// or answered. The app wires it to the main window's Invalidate.
func WithAuthPromptRedrawRequest(fn func()) AuthPromptOption {
	return func(p *AuthPrompt) { p.redraw = fn }
}

// WithAuthPromptTimeouts overrides prompt lifetimes. Tests use short values;
// the app keeps the original two and five minutes.
func WithAuthPromptTimeouts(passphrase, keyboard time.Duration) AuthPromptOption {
	return func(p *AuthPrompt) {
		if passphrase > 0 {
			p.passphraseTimeout = passphrase
		}
		if keyboard > 0 {
			p.keyboardTimeout = keyboard
		}
	}
}

// NewAuthPrompt returns an empty prompt queue.
func NewAuthPrompt(opts ...AuthPromptOption) *AuthPrompt {
	prompt := &AuthPrompt{
		passphraseTimeout: defaultPassphraseTimeout,
		keyboardTimeout:   defaultKeyboardInteractiveTimeout,
	}
	for _, opt := range opts {
		opt(prompt)
	}
	return prompt
}

var _ appconnection.AuthPrompter = (*AuthPrompt)(nil)

// Passphrase queues a passphrase request and waits for the user's answer.
func (p *AuthPrompt) Passphrase(ctx context.Context, request appconnection.PassphraseRequest) (appconnection.PassphraseDecision, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.PassphraseDecision{}, err
	}
	answer, err := p.wait(ctx, AuthPromptView{Kind: AuthPromptPassphrase, Passphrase: request}, p.passphraseTimeout)
	if err != nil {
		return appconnection.PassphraseDecision{}, err
	}
	return answer.passphrase, nil
}

// KeyboardInteractive queues one server round and waits for ordered answers.
func (p *AuthPrompt) KeyboardInteractive(ctx context.Context, request appconnection.KeyboardInteractiveRequest) (appconnection.KeyboardInteractiveDecision, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.KeyboardInteractiveDecision{}, err
	}
	answer, err := p.wait(ctx, AuthPromptView{Kind: AuthPromptKeyboardInteractive, Keyboard: request}, p.keyboardTimeout)
	if err != nil {
		return appconnection.KeyboardInteractiveDecision{}, err
	}
	return answer.keyboard, nil
}

func (p *AuthPrompt) wait(ctx context.Context, view AuthPromptView, timeout time.Duration) (authPromptAnswer, error) {
	size := len(view.Keyboard.Prompts)
	if size == 0 {
		size = 1
	}
	request := &authPromptRequest{
		view:    view,
		answer:  make(chan authPromptAnswer, 1),
		answers: make([]string, size),
	}

	p.mu.Lock()
	p.queue = append(p.queue, request)
	p.mu.Unlock()
	p.requestRedraw()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case answer := <-request.answer:
		return answer, nil
	case <-ctx.Done():
		p.remove(request)
		return authPromptAnswer{}, ctx.Err()
	case <-timer.C:
		p.remove(request)
		return authPromptAnswer{}, &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: authentication prompt expired", appconnection.ErrAuthTimeout),
		}
	}
}

// Current returns the oldest queued request.
func (p *AuthPrompt) Current() (AuthPromptView, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return AuthPromptView{}, false
	}
	return p.queue[0].view, true
}

// Pending reports how many authentication prompts are waiting.
func (p *AuthPrompt) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// PassphraseInput returns the current prompt's draft passphrase, or nil when
// there is no passphrase prompt.
func (p *AuthPrompt) PassphraseInput() *string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 || p.queue[0].view.Kind != AuthPromptPassphrase {
		return nil
	}
	request := p.queue[0]
	if len(request.answers) == 0 {
		request.answers = []string{""}
	}
	return &request.answers[0]
}

// AnswerInput returns the draft answer for one keyboard-interactive prompt, or
// nil when there is no keyboard-interactive prompt.
func (p *AuthPrompt) AnswerInput(index int) *string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 || p.queue[0].view.Kind != AuthPromptKeyboardInteractive {
		return nil
	}
	request := p.queue[0]
	if index < 0 || index >= len(request.answers) {
		index = 0
	}
	for len(request.answers) <= index {
		request.answers = append(request.answers, "")
	}
	return &request.answers[index]
}

// SubmitPassphrase resolves the current passphrase prompt. It reports false
// when the current prompt is not a passphrase request.
func (p *AuthPrompt) SubmitPassphrase(value string) bool {
	p.mu.Lock()
	if len(p.queue) == 0 || p.queue[0].view.Kind != AuthPromptPassphrase {
		p.mu.Unlock()
		return false
	}
	request := p.pop()
	p.mu.Unlock()

	request.answer <- authPromptAnswer{passphrase: appconnection.PassphraseDecision{Passphrase: value}}
	p.requestRedraw()
	return true
}

// SubmitKeyboardInteractive resolves the current keyboard-interactive prompt
// with the drafts the view bound.
func (p *AuthPrompt) SubmitKeyboardInteractive() bool {
	p.mu.Lock()
	if len(p.queue) == 0 || p.queue[0].view.Kind != AuthPromptKeyboardInteractive {
		p.mu.Unlock()
		return false
	}
	request := p.queue[0]
	answers := append([]string(nil), request.answers...)
	request = p.pop()
	p.mu.Unlock()

	want := len(request.view.Keyboard.Prompts)
	if len(answers) < want {
		answers = append(answers, make([]string, want-len(answers))...)
	}
	request.answer <- authPromptAnswer{keyboard: appconnection.KeyboardInteractiveDecision{
		Answers: answers[:want],
	}}
	p.requestRedraw()
	return true
}

// Cancel resolves the current prompt as cancelled, aborting authentication.
func (p *AuthPrompt) Cancel() bool {
	p.mu.Lock()
	if len(p.queue) == 0 {
		p.mu.Unlock()
		return false
	}
	request := p.pop()
	p.mu.Unlock()

	answer := authPromptAnswer{}
	if request.view.Kind == AuthPromptKeyboardInteractive {
		answer.keyboard.Cancelled = true
	} else {
		answer.passphrase.Cancelled = true
	}
	request.answer <- answer
	p.requestRedraw()
	return true
}

// pop removes and returns the oldest request. The caller holds p.mu.
func (p *AuthPrompt) pop() *authPromptRequest {
	request := p.queue[0]
	p.queue = p.queue[1:]
	return request
}

func (p *AuthPrompt) remove(request *authPromptRequest) {
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

func (p *AuthPrompt) requestRedraw() {
	if p.redraw != nil {
		p.redraw()
	}
}

// AuthPromptModal renders the queued authentication request as a modal.
func AuthPromptModal(prompt *AuthPrompt) func(*ui.Context) {
	return func(c *ui.Context) {
		view, ok := prompt.Current()
		if !ok {
			return
		}
		m := locales.In(c.Locale())
		t := c.Theme()

		open := true
		ui.Modal(c, &open, func() {
			ui.Column(c).Width(520).Padding(20).Gap(10).Background(t.Surface).Children(func() {
				switch view.Kind {
				case AuthPromptPassphrase:
					title := m.AuthPromptPassphraseTitle(view.Passphrase.Username, view.Passphrase.Hostname)
					ui.Text(c, title).FontSize(18).Bold()
					body := m.AuthPromptPassphraseBody(view.Passphrase.KeyLabel)
					if view.Passphrase.Invalid {
						body = m.AuthPromptPassphraseInvalid(view.Passphrase.KeyLabel)
					}
					ui.Text(c, body).TextColor(t.TextMuted)
					ui.Field(c, m.AuthPromptPassphraseLabel(), func() {
						if input := prompt.PassphraseInput(); input != nil {
							ui.TextInput(c, input).Password().Width(300)
						}
					})
				case AuthPromptKeyboardInteractive:
					ui.Text(c, m.AuthPromptKeyboardTitle(view.Keyboard.Username, view.Keyboard.Hostname)).FontSize(18).Bold()
					if view.Keyboard.Instruction != "" {
						ui.Text(c, view.Keyboard.Instruction).TextColor(t.TextMuted)
					}
					for index, question := range view.Keyboard.Prompts {
						index := index
						ui.Field(c, question.Text, func() {
							input := prompt.AnswerInput(index)
							if input == nil {
								return
							}
							if question.Echo {
								ui.TextInput(c, input).Width(300)
								return
							}
							ui.TextInput(c, input).Password().Width(300)
						})
					}
				}

				ui.Row(c).Gap(8).Children(func() {
					if ui.PrimaryButton(c, m.AuthPromptContinue()).Clicked() {
						if view.Kind == AuthPromptPassphrase {
							if input := prompt.PassphraseInput(); input != nil {
								prompt.SubmitPassphrase(*input)
							}
						} else {
							prompt.SubmitKeyboardInteractive()
						}
						c.Invalidate()
					}
					if ui.Button(c, m.AuthPromptCancel()).Clicked() {
						prompt.Cancel()
						c.Invalidate()
					}
				})
			})
		})
	}
}
