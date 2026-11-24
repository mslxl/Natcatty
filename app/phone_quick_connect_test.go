package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// phoneTargetRecorder sits between the phone shell and the real phone
// runtime. It proves the form's parsed target reaches the production opener,
// while the actual Session still opens through the real Vault, Manager, and
// fake SSH dialer.
type phoneTargetRecorder struct {
	inner   *phoneSessionRuntime
	mu      sync.Mutex
	hosts   []domainvault.Host
	secrets []string
	handles []*phoneSessionHandle
}

func (r *phoneTargetRecorder) OpenHost(ctx context.Context,
	host domainvault.Host) (phoneshell.SessionHandle, error) {

	return r.inner.OpenHost(ctx, host)
}

func (r *phoneTargetRecorder) OpenTarget(ctx context.Context, host domainvault.Host,
	secret string) (phoneshell.SessionHandle, error) {

	handle, err := r.inner.OpenTarget(ctx, host, secret)
	if err != nil {
		return nil, err
	}
	real, ok := handle.(*phoneSessionHandle)
	if !ok {
		return nil, fmt.Errorf("the phone runtime returned %T", handle)
	}
	r.mu.Lock()
	r.hosts = append(r.hosts, host)
	r.secrets = append(r.secrets, secret)
	r.handles = append(r.handles, real)
	r.mu.Unlock()
	return handle, nil
}

func (r *phoneTargetRecorder) last() (domainvault.Host, string, *phoneSessionHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.hosts) == 0 {
		return domainvault.Host{}, "", nil, false
	}
	index := len(r.hosts) - 1
	return r.hosts[index], r.secrets[index], r.handles[index], true
}

func (r *phoneTargetRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hosts)
}

// TestPhoneQuickConnectReachesTheRealOpenerAndBehavesLikeAnyTab drives the
// Sessions home's Quick Connect form through the real phone runtime and a fake
// SSH dialer. It proves the parsed target and one-shot credential reach the
// production opener, the new Tab is selected, and that Tab then closes,
// hibernates, and resumes exactly like a saved Host's Tab.
func TestPhoneQuickConnectReachesTheRealOpenerAndBehavesLikeAnyTab(t *testing.T) {
	signer := fixtureSigner(t)
	env := newPhoneTransportEnv(t, func() fakessh.Options {
		return fakessh.Options{Password: phoneTestPassword, HostKey: signer}
	})
	// Trust the fixture's host key, as a user who accepted it once would. No
	// saved Host is added for the Quick Connect endpoint itself.
	env.addHost(t, domainvault.Host{
		ID: "trust-anchor", Label: "trust anchor", Hostname: "127.0.0.1", Port: 2222,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	}, signer)

	opener := &phoneTargetRecorder{inner: env.runtime}
	shell := phoneshell.New(
		phoneshell.WithSessionOpener(opener),
		phoneshell.WithScreenFactory(nopScreens),
	)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tester := ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	tester.Frame()

	settle := func() {
		tester.Advance(600 * time.Millisecond)
		tester.Frame()
	}
	await := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; texts: %q", what, tester.Texts())
			}
			tester.Frame()
			tester.Advance(20 * time.Millisecond)
			time.Sleep(time.Millisecond)
		}
		tester.Frame()
	}

	// Enter the form from the Sessions home.
	if err := tester.Click("Quick Connect"); err != nil {
		t.Fatalf("click Quick Connect: %v (texts %q)", err, tester.Texts())
	}
	settle()

	// The shared parser turns this URL into deploy@127.0.0.1:2222 with the
	// URL's credential as the one-shot password.
	target := "ssh://deploy:" + phoneTestPassword + "@127.0.0.1:2222"
	if err := tester.Click("Target"); err != nil {
		t.Fatalf("focus Target: %v", err)
	}
	tester.Type(target)
	tester.Frame()
	if err := tester.Click("Connect"); err != nil {
		t.Fatalf("click Connect: %v (texts %q)", err, tester.Texts())
	}

	await("the Quick Connect Session to dial and attach", func() bool {
		return opener.count() == 1 && env.dialer.count() == 1 &&
			lastLiveViews(opener) == 1
	})

	host, secret, handle, ok := opener.last()
	if !ok {
		t.Fatal("the Quick Connect target never reached the real opener")
	}
	if host.ID != "" {
		t.Fatalf("Quick Connect Host ID = %q, want an Unsaved Host", host.ID)
	}
	if host.Hostname != "127.0.0.1" || host.Port != 2222 || host.Username != "deploy" {
		t.Fatalf("opened target = %+v, want the parsed deploy@127.0.0.1:2222", host)
	}
	if secret != phoneTestPassword {
		t.Fatalf("one-shot credential = %q, want the URL credential", secret)
	}
	if got := shell.Router(phoneshell.Sessions).Path(); !strings.HasPrefix(got, "/session/") {
		t.Fatalf("Sessions path = %q, want the Quick Connect Tab's page", got)
	}
	if !tester.HasText("Connected · deploy@127.0.0.1:2222") {
		t.Fatalf("the Quick Connect Tab's page is not showing; texts: %q", tester.Texts())
	}

	// Leaving the foreground hibernates the Session without ending it.
	shell.Background()
	tester.Frame()
	await("the Quick Connect Session to hibernate", func() bool {
		return handle.session.LiveViews() == 0 && handle.session.Snapshot().Hibernated
	})
	select {
	case <-handle.session.Done():
		t.Fatal("backgrounding ended the Quick Connect Session")
	default:
	}

	// Returning resumes the same Session; no second open is made.
	shell.Foreground()
	await("the Quick Connect Session to resume", func() bool {
		return handle.session.LiveViews() == 1 && !handle.session.Snapshot().Hibernated
	})
	if opener.count() != 1 {
		t.Fatalf("foregrounding opened %d Quick Connect Sessions, want the original", opener.count())
	}

	// Back to the list: it is a normal row, still alive. Tapping it restores
	// the same Session.
	settle()
	if err := tester.Click("Back"); err != nil {
		t.Fatalf("back to Sessions: %v", err)
	}
	settle()
	await("the row to report hibernation", func() bool {
		return handle.session.LiveViews() == 0 && tester.HasText("Hibernated · deploy@127.0.0.1:2222")
	})
	if err := tester.Click("Open session deploy@127.0.0.1"); err != nil {
		t.Fatalf("restore the Quick Connect Tab: %v (texts %q)", err, tester.Texts())
	}
	settle()
	await("the restored Quick Connect Session to attach", func() bool {
		return handle.session.LiveViews() == 1 && !handle.session.Snapshot().Hibernated
	})
	if opener.count() != 1 {
		t.Fatalf("restoring opened %d Quick Connect Sessions, want the original", opener.count())
	}

	// The explicit close ends it after the live-session confirmation.
	settle()
	if err := tester.Click("Close session deploy@127.0.0.1"); err != nil {
		t.Fatalf("request close: %v (texts %q)", err, tester.Texts())
	}
	tester.Frame()
	if !tester.HasText("Close deploy@127.0.0.1?") {
		t.Fatalf("the live Quick Connect Tab did not ask before closing; texts: %q", tester.Texts())
	}
	if err := tester.Click("Close"); err != nil {
		t.Fatalf("confirm close: %v", err)
	}
	settle()
	await("the Quick Connect Session to end", func() bool {
		select {
		case <-handle.session.Done():
			return true
		default:
			return false
		}
	})
}

// lastLiveViews reads the recorder's newest handle's live view count without
// holding the recorder lock across Session calls.
func lastLiveViews(r *phoneTargetRecorder) int {
	_, _, handle, ok := r.last()
	if !ok {
		return 0
	}
	return handle.session.LiveViews()
}
