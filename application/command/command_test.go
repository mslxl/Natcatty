package command

import (
	"context"
	"errors"
	"testing"
)

func TestDispatchRunsRegisteredHandler(t *testing.T) {
	registry := NewRegistry()
	var got []string
	if err := registry.Register("tab.new", func(context.Context) error {
		got = append(got, "new")
		return nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := registry.Dispatch(context.Background(), "tab.new"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(got) != 1 || got[0] != "new" {
		t.Fatalf("handler calls = %v, want [new]", got)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	registry := NewRegistry()

	err := registry.Dispatch(context.Background(), "tab.close")

	var unknown *UnknownCommandError
	if !errors.As(err, &unknown) {
		t.Fatalf("Dispatch error = %v, want UnknownCommandError", err)
	}
	if unknown.ID != "tab.close" {
		t.Fatalf("unknown command ID = %q, want %q", unknown.ID, "tab.close")
	}
}

func TestRegisterRejectsDuplicateAndEmptyCommands(t *testing.T) {
	registry := NewRegistry()
	handler := func(context.Context) error { return nil }

	if err := registry.Register("", handler); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("Register empty ID error = %v, want ErrInvalidCommand", err)
	}
	if err := registry.Register("tab.new", nil); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("Register nil handler error = %v, want ErrInvalidCommand", err)
	}
	if err := registry.Register("tab.new", handler); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	if err := registry.Register("tab.new", handler); !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("Register duplicate error = %v, want ErrDuplicateCommand", err)
	}
}

func TestDispatchPropagatesHandlerError(t *testing.T) {
	registry := NewRegistry()
	want := errors.New("boom")
	if err := registry.Register("app.quit", func(context.Context) error { return want }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := registry.Dispatch(context.Background(), "app.quit"); !errors.Is(err, want) {
		t.Fatalf("Dispatch error = %v, want %v", err, want)
	}
}
