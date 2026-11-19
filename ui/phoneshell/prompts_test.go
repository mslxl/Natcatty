package phoneshell

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// promptOpener runs a fake dial that blocks on the shared prompt queues, which
// is what the real Connection Manager does. The Shell tags the context with the
// Tab identity before calling it.
type promptOpener struct {
	run func(ctx context.Context) (SessionHandle, error)
}

func (o promptOpener) OpenHost(ctx context.Context, _ domainvault.Host) (SessionHandle, error) {
	return o.run(ctx)
}

// promptFixture is the phone shell over one Host and one prompt-driven opener.
type promptFixture struct {
	shell   *Shell
	prompts *Prompts
	tester  *ui.Tester
}

func newPromptFixture(t *testing.T, run func(ctx context.Context, prompts *Prompts) (SessionHandle, error)) *promptFixture {
	t.Helper()
	host := domainvault.Host{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com", Port: 2222,
		Username: "alice", Protocol: domainvault.ProtocolSSH,
	}
	prompts := NewPrompts()
	shell := New(
		WithHosts(&fakeHostStore{hosts: []domainvault.Host{host}}),
		WithSessionOpener(promptOpener{run: func(ctx context.Context) (SessionHandle, error) {
			return run(ctx, prompts)
		}}),
		WithPrompts(prompts),
		WithScreenFactory(fakeScreens),
	)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(Hosts)
	tester := ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	tester.Frame()
	return &promptFixture{shell: shell, prompts: prompts, tester: tester}
}

func (f *promptFixture) open(t *testing.T) {
	t.Helper()
	if err := f.tester.Click("Open prod box"); err != nil {
		t.Fatalf("open Host: %v", err)
	}
	f.tester.Frame()
}

func (f *promptFixture) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; texts: %q", what, f.tester.Texts())
		}
		f.tester.Frame()
		f.tester.Advance(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	f.tester.Frame()
}

func (f *promptFixture) awaitText(t *testing.T, text string) {
	t.Helper()
	f.await(t, text, func() bool { return f.tester.HasText(text) })
}

func (f *promptFixture) click(t *testing.T, label string) {
	t.Helper()
	if err := f.tester.Click(label); err != nil {
		t.Fatalf("click %q: %v; texts: %q", label, err, f.tester.Texts())
	}
	f.tester.Frame()
}

// assertNoAttemptLeft checks the cancellation contract: no open Tab and no
// prompt still queued in either prompt.
func (f *promptFixture) assertNoAttemptLeft(t *testing.T) {
	t.Helper()
	if tabs := f.shell.sessions.list(); len(tabs) != 0 {
		t.Fatalf("cancelling left %d open Tab(s): %+v", len(tabs), tabs)
	}
	if pending := f.prompts.HostKey.Pending(); pending != 0 {
		t.Fatalf("host-key prompt still pending: %d", pending)
	}
	if pending := f.prompts.Auth.Pending(); pending != 0 {
		t.Fatalf("credential prompt still pending: %d", pending)
	}
}

func unknownKey() domainconn.Key {
	return domainconn.Key{
		Hostname: "prod.example.com", Port: 2222, KeyType: "ssh-ed25519",
		Fingerprint: "AAAABBBBCCCCDDDDEEEEFFFF", Status: domainconn.StatusUnknown,
	}
}

func TestPhoneUnknownHostKeyShowsFingerprintAndTrusts(t *testing.T) {
	decided := make(chan appconnection.PromptDecision, 1)
	fixture := newPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
		decision, err := prompts.HostKey.Confirm(ctx, unknownKey())
		decided <- decision
		if err != nil {
			return nil, err
		}
		if !decision.Trust {
			return nil, errors.New("host key rejected")
		}
		return newFakeHandle("remote ready\r\n"), nil
	})
	fixture.open(t)

	fixture.awaitText(t, "Unknown host key")
	if !fixture.tester.HasText("ssh-ed25519 fingerprint: SHA256:AAAABBBBCCCCDDDDEEEEFFFF") {
		t.Fatalf("fingerprint is not shown; texts: %q", fixture.tester.Texts())
	}
	fixture.click(t, "Trust and save")

	fixture.awaitText(t, "remote ready")
	decision := <-decided
	if !decision.Trust || !decision.Persist {
		t.Fatalf("decision = %+v, want trust and persist", decision)
	}
	if pending := fixture.prompts.HostKey.Pending(); pending != 0 {
		t.Fatalf("prompt still pending after accept: %d", pending)
	}
}

func TestPhoneChangedHostKeyWarnsDistinctlyAndRejects(t *testing.T) {
	changed := unknownKey()
	changed.Status = domainconn.StatusChanged
	changed.ExpectedFingerprint = "ZZZZYYYYXXXXWWWW"

	fixture := newPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
		decision, err := prompts.HostKey.Confirm(ctx, changed)
		if err != nil {
			return nil, err
		}
		if decision.Trust {
			return newFakeHandle("should not connect"), nil
		}
		return nil, errors.New("host key rejected")
	})
	fixture.open(t)

	fixture.awaitText(t, "Host key changed")
	if fixture.tester.HasText("Unknown host key") {
		t.Fatalf("a changed key used the unknown-key title; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("Previously trusted fingerprint: SHA256:ZZZZYYYYXXXXWWWW") {
		t.Fatalf("the old fingerprint is not shown; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("Do not continue unless you know why the key changed.") {
		t.Fatalf("the changed-key warning is missing; texts: %q", fixture.tester.Texts())
	}
	fixture.click(t, "Reject")

	fixture.awaitText(t, "No sessions yet.")
	fixture.assertNoAttemptLeft(t)
	if got := fixture.shell.Router(Sessions).Path(); got != "/" {
		t.Fatalf("Sessions path = %q, want its root after rejecting", got)
	}
}

func TestPhoneHostKeyRejectAbortsAndLeavesNoTab(t *testing.T) {
	decided := make(chan appconnection.PromptDecision, 1)
	fixture := newPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
		decision, err := prompts.HostKey.Confirm(ctx, unknownKey())
		decided <- decision
		if err != nil {
			return nil, err
		}
		if !decision.Trust {
			return nil, errors.New("host key rejected")
		}
		return newFakeHandle("should not connect"), nil
	})
	fixture.open(t)
	fixture.awaitText(t, "Unknown host key")
	fixture.click(t, "Reject")

	fixture.awaitText(t, "No sessions yet.")
	fixture.assertNoAttemptLeft(t)
	if decision := <-decided; decision.Trust {
		t.Fatalf("reject resolved as trust: %+v", decision)
	}
	if fixture.tester.HasText("should not connect") {
		t.Fatal("a rejected host key still opened a terminal")
	}
}

func TestPhonePasswordPromptAnswersTheChallenge(t *testing.T) {
	answered := make(chan appconnection.PasswordDecision, 1)
	fixture := newPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
		decision, err := prompts.Auth.Password(ctx, appconnection.PasswordRequest{
			HostID: "prod", Hostname: "prod.example.com", Username: "alice",
		})
		answered <- decision
		if err != nil {
			return nil, err
		}
		if decision.Cancelled {
			return nil, errors.New("cancelled")
		}
		return newFakeHandle("signed in\r\n"), nil
	})
	fixture.open(t)

	fixture.awaitText(t, "Password required")
	fixture.awaitText(t, "Sign in as alice@prod.example.com.")
	fixture.tester.Type("hunter2")
	fixture.tester.Frame()
	fixture.click(t, "Continue")

	fixture.awaitText(t, "signed in")
	if decision := <-answered; decision.Password != "hunter2" {
		t.Fatalf("password answer = %q, want hunter2", decision.Password)
	}
	if pending := fixture.prompts.Auth.Pending(); pending != 0 {
		t.Fatalf("password prompt still pending: %d", pending)
	}
}

func TestPhonePassphrasePromptAnswersTheChallenge(t *testing.T) {
	answered := make(chan appconnection.PassphraseDecision, 1)
	fixture := newPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
		decision, err := prompts.Auth.Passphrase(ctx, appconnection.PassphraseRequest{
			Hostname: "prod.example.com", Username: "alice", KeyLabel: "work",
		})
		answered <- decision
		if err != nil {
			return nil, err
		}
		if decision.Cancelled {
			return nil, errors.New("cancelled")
		}
		return newFakeHandle("unlocked\r\n"), nil
	})
	fixture.open(t)

	fixture.awaitText(t, "Key passphrase required")
	if !fixture.tester.HasText("Enter the passphrase for SSH Key “work”.") {
		t.Fatalf("key label is not named; texts: %q", fixture.tester.Texts())
	}
	fixture.tester.Type("open-sesame")
	fixture.tester.Frame()
	fixture.click(t, "Continue")

	fixture.awaitText(t, "unlocked")
	if decision := <-answered; decision.Passphrase != "open-sesame" {
		t.Fatalf("passphrase answer = %q, want open-sesame", decision.Passphrase)
	}
}

func TestPhoneKeyboardInteractivePromptAnswersMultipleRounds(t *testing.T) {
	type roundResult struct {
		first, second []string
	}
	done := make(chan roundResult, 1)
	fixture := newPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
		first, err := prompts.Auth.KeyboardInteractive(ctx, appconnection.KeyboardInteractiveRequest{
			Hostname: "prod.example.com", Username: "alice",
			Name: "Authentication", Instruction: "First factor",
			Prompts: []appconnection.KeyboardInteractivePrompt{{Text: "Verification code"}},
		})
		if err != nil {
			return nil, err
		}
		second, err := prompts.Auth.KeyboardInteractive(ctx, appconnection.KeyboardInteractiveRequest{
			Hostname: "prod.example.com", Username: "alice",
			Name: "Authentication", Instruction: "Second factor",
			Prompts: []appconnection.KeyboardInteractivePrompt{{Text: "One-time password"}},
		})
		if err != nil {
			return nil, err
		}
		done <- roundResult{first: first.Answers, second: second.Answers}
		if first.Cancelled || second.Cancelled {
			return nil, errors.New("cancelled")
		}
		return newFakeHandle("second factor accepted\r\n"), nil
	})
	fixture.open(t)

	fixture.awaitText(t, "Verification code")
	fixture.tester.Type("123456")
	fixture.tester.Frame()
	fixture.click(t, "Continue")

	fixture.awaitText(t, "One-time password")
	fixture.tester.Type("654321")
	fixture.tester.Frame()
	fixture.click(t, "Continue")

	fixture.awaitText(t, "second factor accepted")
	result := <-done
	if strings.Join(result.first, ",") != "123456" || strings.Join(result.second, ",") != "654321" {
		t.Fatalf("round answers = %v then %v", result.first, result.second)
	}
	if pending := fixture.prompts.Auth.Pending(); pending != 0 {
		t.Fatalf("keyboard prompt still pending: %d", pending)
	}
}

func TestPhoneCancellingACredentialPromptClosesTheTab(t *testing.T) {
	cases := []struct {
		name  string
		title string
		run   func(prompts *Prompts, ctx context.Context) (SessionHandle, error)
	}{
		{
			name: "password", title: "Password required",
			run: func(prompts *Prompts, ctx context.Context) (SessionHandle, error) {
				decision, err := prompts.Auth.Password(ctx, appconnection.PasswordRequest{
					HostID: "prod", Hostname: "prod.example.com", Username: "alice",
				})
				if err != nil {
					return nil, err
				}
				if decision.Cancelled {
					return nil, errors.New("cancelled")
				}
				return newFakeHandle("answered"), nil
			},
		},
		{
			name: "passphrase", title: "Key passphrase required",
			run: func(prompts *Prompts, ctx context.Context) (SessionHandle, error) {
				decision, err := prompts.Auth.Passphrase(ctx, appconnection.PassphraseRequest{
					Hostname: "prod.example.com", Username: "alice", KeyLabel: "work",
				})
				if err != nil {
					return nil, err
				}
				if decision.Cancelled {
					return nil, errors.New("cancelled")
				}
				return newFakeHandle("answered"), nil
			},
		},
		{
			name: "keyboard-interactive", title: "First factor",
			run: func(prompts *Prompts, ctx context.Context) (SessionHandle, error) {
				decision, err := prompts.Auth.KeyboardInteractive(ctx, appconnection.KeyboardInteractiveRequest{
					Hostname: "prod.example.com", Username: "alice", Instruction: "First factor",
					Prompts: []appconnection.KeyboardInteractivePrompt{{Text: "Code"}},
				})
				if err != nil {
					return nil, err
				}
				if decision.Cancelled {
					return nil, errors.New("cancelled")
				}
				return newFakeHandle("answered"), nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			finished := make(chan error, 1)
			fixture := newPromptFixture(t, func(ctx context.Context, prompts *Prompts) (SessionHandle, error) {
				handle, err := tc.run(prompts, ctx)
				finished <- err
				return handle, err
			})
			fixture.open(t)
			fixture.awaitText(t, tc.title)
			fixture.click(t, "Cancel")

			fixture.awaitText(t, "No sessions yet.")
			fixture.assertNoAttemptLeft(t)
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("the dial returned without an error after cancel")
				}
			case <-time.After(time.Second):
				t.Fatal("the dial goroutine did not return after cancel")
			}
		})
	}
}
