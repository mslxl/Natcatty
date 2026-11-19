package phoneshell

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// Prompt lifetimes mirror the desktop prompt: a host-key decision and a key
// passphrase expire after two minutes, a keyboard-interactive round after five.
// An unanswered prompt is resolved as rejected/cancelled so it cannot outlive
// the connection attempt that raised it.
const (
	defaultPhoneHostKeyTimeout    = 2 * time.Minute
	defaultPhonePassphraseTimeout = 2 * time.Minute
	defaultPhonePasswordTimeout   = 2 * time.Minute
	defaultPhoneKeyboardTimeout   = 5 * time.Minute
)

// Prompts is the phone's pair of blocking prompt queues. The Connection
// Manager asks them for trust and credential decisions from the dial
// goroutine; the Sessions page reads and answers them on the UI thread.
type Prompts struct {
	HostKey *HostKeyPrompt
	Auth    *AuthPrompt
}

// NewPrompts returns empty prompt queues.
func NewPrompts() *Prompts {
	return &Prompts{HostKey: NewHostKeyPrompt(), Auth: NewAuthPrompt()}
}

// promptOwnerKey carries the Sessions Tab identity through the dial context,
// so a prompt is shown on the page of the Tab that raised it and cancelling
// that Tab removes exactly its prompts.
type promptOwnerKey struct{}

func withPromptOwner(ctx context.Context, owner string) context.Context {
	if owner == "" {
		return ctx
	}
	return context.WithValue(ctx, promptOwnerKey{}, owner)
}

func promptOwner(ctx context.Context) string {
	owner, _ := ctx.Value(promptOwnerKey{}).(string)
	return owner
}

// HostKeyPrompt asks the user to trust an unknown or changed host key. Confirm
// blocks the dial goroutine; the UI calls CurrentFor and Accept or Reject. The
// queue is guarded so concurrent connections cannot interleave.
type HostKeyPrompt struct {
	mu    sync.Mutex
	queue []*hostKeyRequest
}

type hostKeyRequest struct {
	key    domainconn.Key
	owner  string
	answer chan appconnection.PromptDecision
}

// NewHostKeyPrompt returns an empty trust prompt queue.
func NewHostKeyPrompt() *HostKeyPrompt {
	return &HostKeyPrompt{}
}

var _ appconnection.Prompter = (*HostKeyPrompt)(nil)

// Confirm queues key and waits for the decision. Cancelling ctx removes the
// prompt and aborts the connection.
func (p *HostKeyPrompt) Confirm(ctx context.Context, key domainconn.Key) (appconnection.PromptDecision, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.PromptDecision{}, err
	}
	request := &hostKeyRequest{
		key:    key,
		owner:  promptOwner(ctx),
		answer: make(chan appconnection.PromptDecision, 1),
	}
	p.mu.Lock()
	p.queue = append(p.queue, request)
	p.mu.Unlock()

	timer := time.NewTimer(defaultPhoneHostKeyTimeout)
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

// CurrentFor returns the oldest prompt for one Tab. An ownerless request is
// visible everywhere, which keeps a prompt reachable if a caller did not tag
// its context.
func (p *HostKeyPrompt) CurrentFor(owner string) (domainconn.Key, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request := p.matchLocked(owner)
	if request == nil {
		return domainconn.Key{}, false
	}
	return request.key, true
}

// Pending reports how many trust decisions are waiting.
func (p *HostKeyPrompt) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// Accept trusts the Tab's current key. Persist records it as a Known Host.
func (p *HostKeyPrompt) Accept(owner string, persist bool) bool {
	return p.answer(owner, appconnection.PromptDecision{Trust: true, Persist: persist})
}

// Reject refuses the Tab's current key, aborting the connection.
func (p *HostKeyPrompt) Reject(owner string) bool {
	return p.answer(owner, appconnection.PromptDecision{})
}

func (p *HostKeyPrompt) answer(owner string, decision appconnection.PromptDecision) bool {
	p.mu.Lock()
	request := p.matchLocked(owner)
	if request == nil {
		p.mu.Unlock()
		return false
	}
	p.removeLocked(request)
	p.mu.Unlock()
	request.answer <- decision
	return true
}

func (p *HostKeyPrompt) matchLocked(owner string) *hostKeyRequest {
	if len(p.queue) == 0 {
		return nil
	}
	if owner == "" {
		return p.queue[0]
	}
	for _, request := range p.queue {
		if request.owner == owner || request.owner == "" {
			return request
		}
	}
	return nil
}

func (p *HostKeyPrompt) remove(request *hostKeyRequest) {
	p.mu.Lock()
	p.removeLocked(request)
	p.mu.Unlock()
}

func (p *HostKeyPrompt) removeLocked(request *hostKeyRequest) {
	for index, queued := range p.queue {
		if queued == request {
			p.queue = append(p.queue[:index], p.queue[index+1:]...)
			return
		}
	}
}

// drawHostKey renders the queued trust decision for one Tab. reject closes the
// Tab after refusing the key, so a rejected connection leaves nothing open. It
// reports whether it drew anything so the Session page can keep polling while
// the dial goroutine waits.
func (p *HostKeyPrompt) drawHostKey(c *ui.Context, owner string, reject func()) bool {
	key, ok := p.CurrentFor(owner)
	if !ok {
		return false
	}
	t := c.Theme()
	changed := key.Status == domainconn.StatusChanged

	ui.Column(c).FillWidth().Padding(16).Gap(10).Background(t.Surface).Radius(12).Children(func() {
		title := "Unknown host key"
		body := fmt.Sprintf("The host %s is not trusted yet. Verify its fingerprint before connecting.", key.Hostname)
		if changed {
			title = "Host key changed"
			body = fmt.Sprintf("The host key for %s changed.", key.Hostname)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Lucide(icons.TriangleAlert)).TextColor(c.Theme().Danger)
			ui.Text(c, title).FontWeight(600)
		})
		ui.Text(c, body).FontSize(13).TextColor(t.TextMuted)
		ui.Text(c, fmt.Sprintf("%s fingerprint: SHA256:%s", key.KeyType, key.Fingerprint)).
			FontSize(12)
		if changed && key.ExpectedFingerprint != "" {
			ui.Text(c, "Previously trusted fingerprint: SHA256:"+key.ExpectedFingerprint).
				FontSize(12).TextColor(c.Theme().Danger)
			ui.Text(c, "Do not continue unless you know why the key changed.").
				FontSize(12).TextColor(c.Theme().Danger)
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "Trust and save").Clicked() {
				p.Accept(owner, true)
				c.Invalidate()
			}
			if ui.Button(c, "Reject").Clicked() {
				p.Reject(owner)
				if reject != nil {
					reject()
				}
				c.Invalidate()
			}
		})
	})
	return true
}

// AuthPromptKind is the kind of interactive authentication request queued.
type AuthPromptKind string

const (
	// AuthPromptPassphrase asks for an encrypted SSH Key's passphrase.
	AuthPromptPassphrase AuthPromptKind = "passphrase"
	// AuthPromptPassword asks for a password when the Host stores none.
	AuthPromptPassword AuthPromptKind = "password"
	// AuthPromptKeyboardInteractive asks the server's ordered questions.
	AuthPromptKeyboardInteractive AuthPromptKind = "keyboard-interactive"
)

// AuthPromptView is the queued request the phone page renders.
type AuthPromptView struct {
	Kind       AuthPromptKind
	Passphrase appconnection.PassphraseRequest
	Password   appconnection.PasswordRequest
	Keyboard   appconnection.KeyboardInteractiveRequest
}

// AuthPrompt is the phone's passphrase, password, and keyboard-interactive
// prompt. The dial goroutine blocks on the answer; the Sessions page reads the
// current request and submits it. A password answer is recorded through the
// caller's credential-store saver, never in settings or logs.
type AuthPrompt struct {
	mu           sync.Mutex
	queue        []*authRequest
	savePassword func(ctx context.Context, hostID model.HostID, password string) error
}

type authRequest struct {
	view   AuthPromptView
	owner  string
	answer chan authAnswer
	drafts []string
}

type authAnswer struct {
	passphrase appconnection.PassphraseDecision
	password   appconnection.PasswordDecision
	keyboard   appconnection.KeyboardInteractiveDecision
}

// NewAuthPrompt returns an empty authentication prompt queue.
func NewAuthPrompt() *AuthPrompt {
	return &AuthPrompt{}
}

var _ appconnection.AuthPrompter = (*AuthPrompt)(nil)
var _ appconnection.PasswordPrompter = (*AuthPrompt)(nil)

// SetPasswordSaver sets where an answered password is recorded. The
// application points this at the Vault's field-secret store, so the value is
// encrypted under the Host identifier and never written to settings or logs.
// A nil saver keeps the password in memory for this attempt only.
func (p *AuthPrompt) SetPasswordSaver(saver func(ctx context.Context, hostID model.HostID, password string) error) {
	p.mu.Lock()
	p.savePassword = saver
	p.mu.Unlock()
}

// Passphrase queues a passphrase request and waits for the answer.
func (p *AuthPrompt) Passphrase(ctx context.Context, request appconnection.PassphraseRequest) (appconnection.PassphraseDecision, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.PassphraseDecision{}, err
	}
	answer, err := p.wait(ctx, AuthPromptView{Kind: AuthPromptPassphrase, Passphrase: request}, defaultPhonePassphraseTimeout)
	if err != nil {
		return appconnection.PassphraseDecision{}, err
	}
	return answer.passphrase, nil
}

// Password queues a password request and waits for the answer. A non-cancelled
// answer is handed to the configured credential-store saver on the dial
// goroutine, after the prompt has been resolved.
func (p *AuthPrompt) Password(ctx context.Context, request appconnection.PasswordRequest) (appconnection.PasswordDecision, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.PasswordDecision{}, err
	}
	answer, err := p.wait(ctx, AuthPromptView{Kind: AuthPromptPassword, Password: request}, defaultPhonePasswordTimeout)
	if err != nil {
		return appconnection.PasswordDecision{}, err
	}
	if !answer.password.Cancelled && answer.password.Password != "" {
		p.mu.Lock()
		saver := p.savePassword
		p.mu.Unlock()
		if saver != nil {
			// The attempt proceeds even when persistence fails: the secret is
			// still used in memory only and is never written anywhere else.
			_ = saver(ctx, request.HostID, answer.password.Password)
		}
	}
	return answer.password, nil
}

// KeyboardInteractive queues one server round and waits for ordered answers.
func (p *AuthPrompt) KeyboardInteractive(ctx context.Context, request appconnection.KeyboardInteractiveRequest) (appconnection.KeyboardInteractiveDecision, error) {
	if err := ctx.Err(); err != nil {
		return appconnection.KeyboardInteractiveDecision{}, err
	}
	answer, err := p.wait(ctx, AuthPromptView{Kind: AuthPromptKeyboardInteractive, Keyboard: request}, defaultPhoneKeyboardTimeout)
	if err != nil {
		return appconnection.KeyboardInteractiveDecision{}, err
	}
	return answer.keyboard, nil
}

func (p *AuthPrompt) wait(ctx context.Context, view AuthPromptView, timeout time.Duration) (authAnswer, error) {
	size := len(view.Keyboard.Prompts)
	if size == 0 {
		size = 1
	}
	request := &authRequest{
		view:   view,
		owner:  promptOwner(ctx),
		answer: make(chan authAnswer, 1),
		drafts: make([]string, size),
	}
	p.mu.Lock()
	p.queue = append(p.queue, request)
	p.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case answer := <-request.answer:
		return answer, nil
	case <-ctx.Done():
		p.remove(request)
		return authAnswer{}, ctx.Err()
	case <-timer.C:
		p.remove(request)
		return authAnswer{}, &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: authentication prompt expired", appconnection.ErrAuthTimeout),
		}
	}
}

// CurrentFor returns the oldest request for one Tab.
func (p *AuthPrompt) CurrentFor(owner string) (AuthPromptView, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request := p.matchLocked(owner)
	if request == nil {
		return AuthPromptView{}, false
	}
	return request.view, true
}

// Pending reports how many authentication prompts are waiting.
func (p *AuthPrompt) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// PassphraseInput returns the current passphrase draft, or nil when there is no
// passphrase prompt.
func (p *AuthPrompt) PassphraseInput(owner string) *string {
	return p.draft(owner, AuthPromptPassphrase, 0)
}

// PasswordInput returns the current password draft, or nil when there is no
// password prompt.
func (p *AuthPrompt) PasswordInput(owner string) *string {
	return p.draft(owner, AuthPromptPassword, 0)
}

// AnswerInput returns the draft answer for one keyboard-interactive prompt.
func (p *AuthPrompt) AnswerInput(owner string, index int) *string {
	return p.draft(owner, AuthPromptKeyboardInteractive, index)
}

func (p *AuthPrompt) draft(owner string, kind AuthPromptKind, index int) *string {
	p.mu.Lock()
	defer p.mu.Unlock()
	request := p.matchLocked(owner)
	if request == nil || request.view.Kind != kind {
		return nil
	}
	if index < 0 || index >= len(request.drafts) {
		index = 0
	}
	return &request.drafts[index]
}

// SubmitPassphrase resolves the current passphrase prompt.
func (p *AuthPrompt) SubmitPassphrase(owner, value string) bool {
	p.mu.Lock()
	request := p.matchLocked(owner)
	if request == nil || request.view.Kind != AuthPromptPassphrase {
		p.mu.Unlock()
		return false
	}
	p.removeLocked(request)
	p.mu.Unlock()
	request.answer <- authAnswer{passphrase: appconnection.PassphraseDecision{Passphrase: value}}
	return true
}

// SubmitPassword resolves the current password prompt.
func (p *AuthPrompt) SubmitPassword(owner, value string) bool {
	p.mu.Lock()
	request := p.matchLocked(owner)
	if request == nil || request.view.Kind != AuthPromptPassword {
		p.mu.Unlock()
		return false
	}
	p.removeLocked(request)
	p.mu.Unlock()
	request.answer <- authAnswer{password: appconnection.PasswordDecision{Password: value}}
	return true
}

// SubmitKeyboardInteractive resolves the current keyboard round with its
// drafts.
func (p *AuthPrompt) SubmitKeyboardInteractive(owner string) bool {
	p.mu.Lock()
	request := p.matchLocked(owner)
	if request == nil || request.view.Kind != AuthPromptKeyboardInteractive {
		p.mu.Unlock()
		return false
	}
	answers := append([]string(nil), request.drafts...)
	want := len(request.view.Keyboard.Prompts)
	p.removeLocked(request)
	p.mu.Unlock()

	if len(answers) < want {
		answers = append(answers, make([]string, want-len(answers))...)
	}
	request.answer <- authAnswer{keyboard: appconnection.KeyboardInteractiveDecision{Answers: answers[:want]}}
	return true
}

// Cancel resolves the Tab's current prompt as cancelled, aborting
// authentication.
func (p *AuthPrompt) Cancel(owner string) bool {
	p.mu.Lock()
	request := p.matchLocked(owner)
	if request == nil {
		p.mu.Unlock()
		return false
	}
	p.removeLocked(request)
	p.mu.Unlock()

	answer := authAnswer{}
	switch request.view.Kind {
	case AuthPromptKeyboardInteractive:
		answer.keyboard.Cancelled = true
	case AuthPromptPassword:
		answer.password.Cancelled = true
	default:
		answer.passphrase.Cancelled = true
	}
	request.answer <- answer
	return true
}

func (p *AuthPrompt) matchLocked(owner string) *authRequest {
	if len(p.queue) == 0 {
		return nil
	}
	if owner == "" {
		return p.queue[0]
	}
	for _, request := range p.queue {
		if request.owner == owner || request.owner == "" {
			return request
		}
	}
	return nil
}

func (p *AuthPrompt) remove(request *authRequest) {
	p.mu.Lock()
	p.removeLocked(request)
	p.mu.Unlock()
}

func (p *AuthPrompt) removeLocked(request *authRequest) {
	for index, queued := range p.queue {
		if queued == request {
			p.queue = append(p.queue[:index], p.queue[index+1:]...)
			return
		}
	}
}

// drawAuth renders the queued authentication request for one Tab. cancel
// closes the Tab and its in-flight connection; it is the prompt's Cancel
// action, which leaves neither an open Tab nor a stale prompt.
func (p *AuthPrompt) drawAuth(c *ui.Context, owner string, cancel func()) bool {
	view, ok := p.CurrentFor(owner)
	if !ok {
		return false
	}
	t := c.Theme()

	ui.Column(c).FillWidth().Padding(16).Gap(10).Background(t.Surface).Radius(12).Children(func() {
		switch view.Kind {
		case AuthPromptPassphrase:
			ui.Text(c, "Key passphrase required").FontWeight(600)
			body := fmt.Sprintf("Enter the passphrase for SSH Key “%s”.", view.Passphrase.KeyLabel)
			if view.Passphrase.Invalid {
				body = fmt.Sprintf("That passphrase did not unlock “%s”. Try again.", view.Passphrase.KeyLabel)
				ui.Text(c, body).FontSize(13).TextColor(c.Theme().Danger)
			} else {
				ui.Text(c, body).FontSize(13).TextColor(t.TextMuted)
			}
			kit.FormRow(c, "Passphrase", func() {
				if input := p.PassphraseInput(owner); input != nil {
					field := ui.TextInput(c, input).Password()
					field.AutoFocus()
				}
			})
		case AuthPromptPassword:
			ui.Text(c, "Password required").FontWeight(600)
			body := fmt.Sprintf("Sign in as %s@%s.", view.Password.Username, view.Password.Hostname)
			if view.Password.Invalid {
				body = "That password was rejected. Try again."
				ui.Text(c, body).FontSize(13).TextColor(c.Theme().Danger)
			} else {
				ui.Text(c, body).FontSize(13).TextColor(t.TextMuted)
			}
			kit.FormRow(c, "Password", func() {
				if input := p.PasswordInput(owner); input != nil {
					field := ui.TextInput(c, input).Password()
					field.AutoFocus()
				}
			})
		case AuthPromptKeyboardInteractive:
			ui.Text(c, fmt.Sprintf("Sign in as %s@%s", view.Keyboard.Username, view.Keyboard.Hostname)).FontWeight(600)
			if view.Keyboard.Instruction != "" {
				ui.Text(c, view.Keyboard.Instruction).FontSize(13).TextColor(t.TextMuted)
			}
			for index, question := range view.Keyboard.Prompts {
				index := index
				kit.FormRow(c, question.Text, func() {
					input := p.AnswerInput(owner, index)
					if input == nil {
						return
					}
					if question.Echo {
						field := ui.TextInput(c, input)
						if index == 0 {
							field.AutoFocus()
						}
						return
					}
					field := ui.TextInput(c, input).Password()
					if index == 0 {
						field.AutoFocus()
					}
				})
			}
		}

		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "Continue").Clicked() {
				switch view.Kind {
				case AuthPromptPassphrase:
					if input := p.PassphraseInput(owner); input != nil {
						p.SubmitPassphrase(owner, *input)
					}
				case AuthPromptPassword:
					if input := p.PasswordInput(owner); input != nil {
						p.SubmitPassword(owner, *input)
					}
				default:
					p.SubmitKeyboardInteractive(owner)
				}
				c.Invalidate()
			}
			if ui.Button(c, "Cancel").Clicked() {
				p.Cancel(owner)
				if cancel != nil {
					cancel()
				}
				c.Invalidate()
			}
		})
	})
	return true
}
