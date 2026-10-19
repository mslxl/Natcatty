package sessionui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

func TestTrustPromptWaitsForTheUserDecision(t *testing.T) {
	prompt := NewTrustPrompt()
	key := domainconn.Key{
		Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519",
		Fingerprint: "live", Status: domainconn.StatusChanged,
		ExpectedFingerprint: "saved",
	}
	answers := make(chan appconnection.PromptDecision, 1)
	go func() {
		decision, err := prompt.Confirm(context.Background(), key)
		if err != nil {
			t.Errorf("Confirm: %v", err)
		}
		answers <- decision
	}()

	eventually(t, func() bool { return prompt.Pending() == 1 })
	current, ok := prompt.Current()
	if !ok || current.Status != domainconn.StatusChanged || current.ExpectedFingerprint != "saved" {
		t.Fatalf("current prompt = %+v, %v; want the changed key with its saved fingerprint", current, ok)
	}

	if !prompt.Answer(appconnection.PromptDecision{Trust: true, Persist: true}) {
		t.Fatal("Answer reported no queued prompt")
	}
	select {
	case decision := <-answers:
		if !decision.Trust || !decision.Persist {
			t.Fatalf("decision = %+v, want trust and persist", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after Answer")
	}
	if prompt.Pending() != 0 {
		t.Fatalf("pending = %d after an answer, want 0", prompt.Pending())
	}
}

func TestTrustPromptAnswersPromptsInOrder(t *testing.T) {
	prompt := NewTrustPrompt()
	type result struct {
		host     string
		decision appconnection.PromptDecision
	}
	results := make(chan result, 2)
	confirm := func(host string) {
		decision, err := prompt.Confirm(context.Background(), domainconn.Key{Hostname: host})
		if err != nil {
			t.Errorf("Confirm(%s): %v", host, err)
		}
		results <- result{host: host, decision: decision}
	}

	go confirm("first")
	eventually(t, func() bool { return prompt.Pending() == 1 })
	go confirm("second")
	eventually(t, func() bool { return prompt.Pending() == 2 })

	if current, _ := prompt.Current(); current.Hostname != "first" {
		t.Fatalf("current = %q, want the first prompt", current.Hostname)
	}
	prompt.Answer(appconnection.PromptDecision{Trust: true})
	first := <-results
	if first.host != "first" || !first.decision.Trust {
		t.Fatalf("first result = %+v, want first trusted", first)
	}
	if current, _ := prompt.Current(); current.Hostname != "second" {
		t.Fatalf("current = %q, want the second prompt", current.Hostname)
	}

	prompt.Answer(appconnection.PromptDecision{Trust: false})
	second := <-results
	if second.host != "second" || second.decision.Trust {
		t.Fatalf("second result = %+v, want second rejected", second)
	}
	if prompt.Pending() != 0 {
		t.Fatalf("pending = %d after both answers, want 0", prompt.Pending())
	}
}

func TestTrustPromptExpiresAnUnansweredDecision(t *testing.T) {
	prompt := NewTrustPrompt(WithTrustTimeout(25 * time.Millisecond))
	_, err := prompt.Confirm(context.Background(), domainconn.Key{Hostname: "example.com"})
	if err == nil {
		t.Fatal("Confirm succeeded, want the prompt to expire")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindHostKey {
		t.Fatalf("error = %v, want a host-key error", err)
	}
	if !errors.Is(err, appconnection.ErrHostKeyRejected) {
		t.Fatalf("error = %v, want ErrHostKeyRejected", err)
	}
	if prompt.Pending() != 0 {
		t.Fatalf("pending = %d after expiry, want 0", prompt.Pending())
	}
}

func TestTrustPromptCancellationRemovesThePrompt(t *testing.T) {
	prompt := NewTrustPrompt()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := prompt.Confirm(ctx, domainconn.Key{Hostname: "example.com"})
		done <- err
	}()
	eventually(t, func() bool { return prompt.Pending() == 1 })

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Confirm error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after cancellation")
	}
	eventually(t, func() bool { return prompt.Pending() == 0 })
	if _, ok := prompt.Current(); ok {
		t.Fatal("a cancelled prompt is still current")
	}
}

func TestTrustViewShowsDistinctUnknownAndChangedPrompts(t *testing.T) {
	tests := []struct {
		name     string
		key      domainconn.Key
		wants    []string
		rejects  []string
		decision appconnection.PromptDecision
	}{
		{
			name: "unknown",
			key: domainconn.Key{
				Hostname: "example.com", KeyType: "ssh-ed25519", Fingerprint: "live",
				Status: domainconn.StatusUnknown,
			},
			wants: []string{
				"Unknown host key",
				"The host example.com is not trusted yet. Verify its fingerprint before connecting.",
				"ssh-ed25519 fingerprint: live",
			},
			rejects:  []string{"Host key has changed", "Previously trusted fingerprint: saved"},
			decision: appconnection.PromptDecision{Trust: true, Persist: true},
		},
		{
			name: "changed",
			key: domainconn.Key{
				Hostname: "example.com", KeyType: "ssh-ed25519", Fingerprint: "live",
				Status: domainconn.StatusChanged, ExpectedFingerprint: "saved",
			},
			wants: []string{
				"Host key has changed",
				"The host key for example.com has changed.",
				"Previously trusted fingerprint: saved",
				"Do not continue unless you know why the key changed.",
			},
			rejects:  []string{"Unknown host key"},
			decision: appconnection.PromptDecision{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt := NewTrustPrompt()
			answers := make(chan appconnection.PromptDecision, 1)
			go func() {
				decision, err := prompt.Confirm(context.Background(), test.key)
				if err != nil {
					t.Errorf("Confirm: %v", err)
				}
				answers <- decision
			}()
			eventually(t, func() bool { return prompt.Pending() == 1 })

			tt := ui.NewTester(TrustView(prompt), 800, 600)
			tt.Frame()
			for _, want := range test.wants {
				if !hasText(tt.Texts(), want) {
					t.Errorf("prompt does not show %q, texts %q", want, tt.Texts())
				}
			}
			for _, unwanted := range test.rejects {
				if hasText(tt.Texts(), unwanted) {
					t.Errorf("prompt unexpectedly shows %q, texts %q", unwanted, tt.Texts())
				}
			}

			button := "Reject"
			if test.decision.Trust {
				button = "Trust and connect"
			}
			if err := tt.Click(button); err != nil {
				t.Fatalf("Click(%q): %v", button, err)
			}
			select {
			case decision := <-answers:
				if decision != test.decision {
					t.Fatalf("decision = %+v, want %+v", decision, test.decision)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Confirm did not return after the button click")
			}
		})
	}
}

func hasText(texts []string, want string) bool {
	for _, text := range texts {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

func eventually(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
