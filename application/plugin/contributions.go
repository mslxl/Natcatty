package plugin

import (
	"context"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// PluginRef identifies the plugin behind a contribution. Source labels where
// the package came from; Development marks an unpacked directory loaded in
// developer mode and is never reported as an installed package.
type PluginRef struct {
	ID          string
	Name        string
	Version     string
	Development bool
	Source      domainplugin.Source
}

// Handler runs one plugin command. It is invoked on the caller's goroutine;
// the container serializes it with the instance lifecycle.
type Handler func(context.Context) error

// Disposer removes one host-side effect. Disposers are idempotent and run in
// reverse registration order on unload.
type Disposer func(context.Context) error

// ContributionRegistrar is the consumer-owned port the container uses to
// publish contributions. The app implements it with the command registry,
// native menu, and window shortcut table; tests use a recorder. Registering
// anything creates a disposer on the fiber's effect stack.
type ContributionRegistrar interface {
	RegisterCommand(ref PluginRef, command domainplugin.Command, run Handler) (Disposer, error)
	RegisterMenu(ref PluginRef, menu domainplugin.Menu) (Disposer, error)
	RegisterKeybinding(ref PluginRef, binding domainplugin.Keybinding) (Disposer, error)
}

// ViewRegistrar is the optional consumer-owned port for View Contributions.
// A ContributionRegistrar that also implements it receives every declared
// view; the app implements it with the application/view registry. Hosts
// without a view surface (tests, headless runtimes) simply omit it, and the
// container registers no views.
type ViewRegistrar interface {
	RegisterView(ref PluginRef, view domainplugin.ViewContribution) (Disposer, error)
}
