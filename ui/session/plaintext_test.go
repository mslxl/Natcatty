package sessionui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

// TestVNCPlaintextPromptAnswersOnlyOnExplicitAction proves the confirmation
// blocks until a decision and returns the explicit one.
func TestVNCPlaintextPromptAnswersOnlyOnExplicitAction(t *testing.T) {
	prompt := NewVNCPlaintextPrompt()
	answers := make(chan bool, 1)
	errs := make(chan error, 1)
	go func() {
		accepted, err := prompt.ConfirmPlaintext(context.Background(), "vnc-desk", "desk.local")
		if err != nil {
			errs <- err
			return
		}
		answers <- accepted
	}()
	eventually(t, func() bool { return prompt.Pending() == 1 })

	if name, ok := prompt.Current(); !ok || name != "desk.local" {
		t.Fatalf("Current = %q, %v; want desk.local", name, ok)
	}
	select {
	case accepted := <-answers:
		t.Fatalf("ConfirmPlaintext returned %v before the user answered", accepted)
	case err := <-errs:
		t.Fatalf("ConfirmPlaintext returned %v before the user answered", err)
	default:
	}
	if !prompt.Answer(true) {
		t.Fatal("Answer reported no waiting prompt")
	}
	select {
	case accepted := <-answers:
		if !accepted {
			t.Fatal("Continue answered false")
		}
	case err := <-errs:
		t.Fatalf("ConfirmPlaintext: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("ConfirmPlaintext did not return after Continue")
	}
	if prompt.Pending() != 0 {
		t.Fatal("the answered prompt is still queued")
	}
}

// TestVNCPlaintextPromptCancelIsAnError proves Cancel aborts the caller.
func TestVNCPlaintextPromptCancelIsAnError(t *testing.T) {
	prompt := NewVNCPlaintextPrompt()
	errs := make(chan error, 1)
	go func() {
		_, err := prompt.ConfirmPlaintext(context.Background(), "vnc-desk", "desk.local")
		errs <- err
	}()
	eventually(t, func() bool { return prompt.Pending() == 1 })

	prompt.Answer(false)
	select {
	case err := <-errs:
		if !errors.Is(err, ErrPlaintextDeclined) {
			t.Fatalf("ConfirmPlaintext = %v, want ErrPlaintextDeclined", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ConfirmPlaintext did not return after Cancel")
	}
}

// TestVNCPlaintextViewRendersTheDisclosureAndButtons proves the modal names
// the Host and offers the explicit Continue and Cancel actions.
func TestVNCPlaintextViewRendersTheDisclosureAndButtons(t *testing.T) {
	for _, test := range []struct {
		name      string
		button    string
		want      bool
		wantError bool
	}{
		{name: "continue", button: "Continue anyway", want: true},
		{name: "cancel", button: "Cancel", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := locales.In("en")
			prompt := NewVNCPlaintextPrompt()
			answers := make(chan bool, 1)
			errs := make(chan error, 1)
			go func() {
				accepted, err := prompt.ConfirmPlaintext(context.Background(), "vnc-desk", "desk.local")
				if err != nil {
					errs <- err
					return
				}
				answers <- accepted
			}()
			eventually(t, func() bool { return prompt.Pending() == 1 })

			tt := ui.NewTester(VNCPlaintextView(prompt), 800, 600)
			tt.Frame()
			for _, want := range []string{
				m.ConnectionVNCPlaintextPromptTitle(),
				m.ConnectionVNCPlaintextPromptBody("desk.local"),
			} {
				if !hasText(tt.Texts(), want) {
					t.Errorf("prompt does not show %q; texts %q", want, tt.Texts())
				}
			}
			if !hasText(tt.Texts(), m.ConnectionVNCPlaintextPromptContinue()) {
				t.Errorf("prompt does not show Continue; texts %q", tt.Texts())
			}
			if err := tt.Click(test.button); err != nil {
				t.Fatalf("Click(%q): %v", test.button, err)
			}

			if test.wantError {
				select {
				case err := <-errs:
					if !errors.Is(err, ErrPlaintextDeclined) {
						t.Fatalf("Cancel = %v, want ErrPlaintextDeclined", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("the view did not answer Cancel")
				}
				return
			}
			select {
			case accepted := <-answers:
				if accepted != test.want {
					t.Fatalf("Continue = %v, want %v", accepted, test.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the view did not answer Continue")
			}
		})
	}
}

// TestVNCPlaintextPromptIsLocalizedInEveryLocale guards the five catalogs
// from silently missing the disclosure text.
func TestVNCPlaintextPromptIsLocalizedInEveryLocale(t *testing.T) {
	for _, tag := range locales.Tags() {
		m := locales.In(tag)
		for _, text := range []string{
			m.ConnectionVNCPlaintextWarning(),
			m.ConnectionVNCPlaintextPromptTitle(),
			m.ConnectionVNCPlaintextPromptBody("desk.local"),
			m.ConnectionVNCPlaintextPromptContinue(),
			m.ConnectionVNCPlaintextPromptCancel(),
		} {
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s has an empty VNC plaintext string", tag)
			}
		}
	}
}
