package app

import (
	"context"
	"testing"

	"github.com/mslxl/natcatty/application/command"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

func TestCommandsDriveTheShellAndQuit(t *testing.T) {
	var locales []string
	shellState := state.NewState(state.WithLocaleRequest(func(tag string) {
		locales = append(locales, tag)
	}))
	quits := 0
	registry := newRegistry(shellState, func() { quits++ })

	if err := registry.Dispatch(context.Background(), command.NewTab); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if shellState.TabCount() != 1 {
		t.Fatalf("tabs = %d after New Tab, want 1", shellState.TabCount())
	}
	if err := registry.Dispatch(context.Background(), command.CloseTab); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
	if shellState.TabCount() != 0 {
		t.Fatalf("tabs = %d after Close Tab, want 0", shellState.TabCount())
	}

	if err := registry.Dispatch(context.Background(), command.LocaleCommandID("zh-TW")); err != nil {
		t.Fatalf("LocaleCommandID: %v", err)
	}
	if len(locales) != 1 || locales[0] != "zh-TW" {
		t.Fatalf("requested locales = %v, want [zh-TW]", locales)
	}

	if err := registry.Dispatch(context.Background(), command.Quit); err != nil {
		t.Fatalf("Quit: %v", err)
	}
	if quits != 1 {
		t.Fatalf("quits = %d, want 1", quits)
	}
}

func TestRegistryKnowsEveryMenuCommand(t *testing.T) {
	shellState := state.NewState()
	registry := newRegistry(shellState, func() {})

	ids := []command.ID{command.NewTab, command.CloseTab, command.Quit}
	for _, tag := range locales.Tags() {
		ids = append(ids, command.LocaleCommandID(tag))
	}
	for _, id := range ids {
		if !registry.Has(id) {
			t.Errorf("command %q is not registered", id)
		}
	}
}
