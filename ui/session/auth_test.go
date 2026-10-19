package sessionui

import (
	"context"
	"errors"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

func TestAuthPromptReturnsTheSubmittedPassphrase(t *testing.T) {
	prompt := NewAuthPrompt()
	answers := make(chan appconnection.PassphraseDecision, 1)
	go func() {
		decision, err := prompt.Passphrase(context.Background(), appconnection.PassphraseRequest{
			Hostname: "example.com", Username: "alice", KeyLabel: "work", Invalid: true,
		})
		if err != nil {
			t.Errorf("Passphrase: %v", err)
		}
		answers <- decision
	}()

	eventually(t, func() bool { return prompt.Pending() == 1 })
	view, ok := prompt.Current()
	if !ok || view.Kind != AuthPromptPassphrase {
		t.Fatalf("current = %+v, %v", view, ok)
	}
	if view.Passphrase.KeyLabel != "work" || !view.Passphrase.Invalid {
		t.Fatalf("passphrase request = %+v", view.Passphrase)
	}
	if !prompt.SubmitPassphrase("key-passphrase") {
		t.Fatal("SubmitPassphrase reported no passphrase prompt")
	}
	select {
	case decision := <-answers:
		if decision.Cancelled || decision.Passphrase != "key-passphrase" {
			t.Fatalf("decision = %+v", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Passphrase did not return after SubmitPassphrase")
	}
	if prompt.Pending() != 0 {
		t.Fatalf("pending = %d, want 0", prompt.Pending())
	}
}

func TestAuthPromptReturnsOrderedKeyboardInteractiveAnswers(t *testing.T) {
	prompt := NewAuthPrompt()
	answers := make(chan appconnection.KeyboardInteractiveDecision, 1)
	go func() {
		decision, err := prompt.KeyboardInteractive(context.Background(), appconnection.KeyboardInteractiveRequest{
			Hostname: "example.com", Username: "alice", Name: "login", Instruction: "Two factors",
			Prompts: []appconnection.KeyboardInteractivePrompt{
				{Text: "Password:", Echo: false},
				{Text: "Verification code:", Echo: false},
			},
		})
		if err != nil {
			t.Errorf("KeyboardInteractive: %v", err)
		}
		answers <- decision
	}()

	eventually(t, func() bool { return prompt.Pending() == 1 })
	*prompt.AnswerInput(0) = "first"
	*prompt.AnswerInput(1) = "123456"
	if !prompt.SubmitKeyboardInteractive() {
		t.Fatal("SubmitKeyboardInteractive reported no keyboard prompt")
	}
	select {
	case decision := <-answers:
		want := []string{"first", "123456"}
		if decision.Cancelled || len(decision.Answers) != len(want) {
			t.Fatalf("decision = %+v, want %v", decision, want)
		}
		for i := range want {
			if decision.Answers[i] != want[i] {
				t.Fatalf("answers = %v, want %v", decision.Answers, want)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("KeyboardInteractive did not return after submit")
	}
}

func TestAuthPromptKeepsAnswerDraftsPerQueuedRequest(t *testing.T) {
	prompt := NewAuthPrompt()
	firstAnswer := make(chan appconnection.PassphraseDecision, 1)
	secondAnswer := make(chan appconnection.PassphraseDecision, 1)
	go func() {
		decision, _ := prompt.Passphrase(context.Background(), appconnection.PassphraseRequest{KeyLabel: "first"})
		firstAnswer <- decision
	}()
	eventually(t, func() bool { return prompt.Pending() == 1 })
	*prompt.PassphraseInput() = "first-draft"

	go func() {
		decision, _ := prompt.Passphrase(context.Background(), appconnection.PassphraseRequest{KeyLabel: "second"})
		secondAnswer <- decision
	}()
	eventually(t, func() bool { return prompt.Pending() == 2 })
	if draft := *prompt.PassphraseInput(); draft != "first-draft" {
		t.Fatalf("first draft = %q after a second request queued, want first-draft", draft)
	}

	if !prompt.SubmitPassphrase("first-draft") {
		t.Fatal("SubmitPassphrase reported no prompt")
	}
	if decision := <-firstAnswer; decision.Passphrase != "first-draft" {
		t.Fatalf("first decision = %+v", decision)
	}
	eventually(t, func() bool { return prompt.Pending() == 1 })
	if view, _ := prompt.Current(); view.Passphrase.KeyLabel != "second" {
		t.Fatalf("current = %+v, want the second request", view)
	}
	if draft := *prompt.PassphraseInput(); draft != "" {
		t.Fatalf("second draft = %q, want its own empty draft", draft)
	}
}

func TestAuthPromptCancellationAndExpiryAreTyped(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		prompt := NewAuthPrompt()
		answers := make(chan appconnection.PassphraseDecision, 1)
		go func() {
			decision, _ := prompt.Passphrase(context.Background(), appconnection.PassphraseRequest{})
			answers <- decision
		}()
		eventually(t, func() bool { return prompt.Pending() == 1 })
		if !prompt.Cancel() {
			t.Fatal("Cancel reported no prompt")
		}
		select {
		case decision := <-answers:
			if !decision.Cancelled {
				t.Fatalf("decision = %+v, want cancelled", decision)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Passphrase did not return after Cancel")
		}
	})

	t.Run("expiry", func(t *testing.T) {
		prompt := NewAuthPrompt(WithAuthPromptTimeouts(20*time.Millisecond, 20*time.Millisecond))
		_, err := prompt.Passphrase(context.Background(), appconnection.PassphraseRequest{})
		var typed *appconnection.Error
		if !errors.As(err, &typed) || typed.Kind != appconnection.KindTimeout {
			t.Fatalf("error = %v, want a typed timeout", err)
		}
		if prompt.Pending() != 0 {
			t.Fatalf("pending = %d after expiry, want 0", prompt.Pending())
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		prompt := NewAuthPrompt()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := prompt.KeyboardInteractive(ctx, appconnection.KeyboardInteractiveRequest{})
			done <- err
		}()
		eventually(t, func() bool { return prompt.Pending() == 1 })
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("KeyboardInteractive did not return after cancellation")
		}
		eventually(t, func() bool { return prompt.Pending() == 0 })
	})
}
