// Package app is the composition root: it constructs the windows, the shell
// state, the command registry, and the native menu, and wires MyGo's
// application lifecycle to them. It is the only package besides main that
// knows the whole application graph.
package app

import (
	"context"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/application/command"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/settings"
	"github.com/mslxl/natcatty/ui/shell"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// Run starts Natcatty and blocks until the application quits.
func Run() error { return run(nil) }

// run is the composition root proper. afterReady, when non-nil, runs on the UI
// thread after the main window exists; the launch smoke build uses it to quit.
func run(afterReady func(*mygo.Window)) error {
	var settingsWindow *mygo.Window
	var mainWindow *mygo.Window

	// openSettings keeps one Settings window per app, as ADR 0006 requires.
	// Shell callbacks run on the UI thread, so creating and focusing the
	// window here is safe.
	openSettings := func() {
		if settingsWindow != nil && !settingsWindow.IsDestroyed() {
			settingsWindow.Show()
			settingsWindow.Focus()
			return
		}

		settingsWindow = mygo.NewWindow(mygo.WindowOptions{
			Title:     locales.In(mygo.App.Locale()).NavSettings(),
			Width:     640,
			Height:    480,
			MinWidth:  480,
			MinHeight: 360,
			StateKey:  "settings",
			Content:   ui.View(settings.View),
		})
		settingsWindow.OnClosed(func() { settingsWindow = nil })
	}

	sessions := appsession.NewService(localpty.Factory{})
	shellState := state.NewState(
		state.WithLocaleRequest(func(tag string) {
			mygo.App.SetLocale(tag)
		}),
		state.WithSettingsRequest(openSettings),
		// Opening and closing a Tab changes the shell's structure; ask for the
		// frame that shows the new tab strip or Vault placeholder.
		state.WithRedrawRequest(func() {
			if mainWindow != nil && !mainWindow.IsDestroyed() {
				mainWindow.Invalidate()
			}
		}),
		state.WithSessions(sessions, func(s *appsession.Session) (state.Surface, error) {
			return sessionui.New(s)
		}),
	)
	registry := newRegistry(shellState, mygo.App.Quit)

	// A Session's end may run on any goroutine; Invalidate is safe from
	// any goroutine and asks for the frame that shows the new status.
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { sessions.CloseAll() })

	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(BuildMenu(registry, mygo.App.Locale()))

		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     locales.In(mygo.App.Locale()).AppName(),
			Width:     1100,
			Height:    720,
			MinWidth:  720,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(shell.View(shellState)),
		})
		mainWindow = win
		sessions.SetChangeHook(func(appsession.Snapshot) { win.Invalidate() })
		win.OnClosed(func() { sessions.CloseAll() })

		mygo.App.OnLocaleChanged(func(tag string) {
			win.SetTitle(locales.In(tag).AppName())
			if settingsWindow != nil && !settingsWindow.IsDestroyed() {
				settingsWindow.SetTitle(locales.In(tag).NavSettings())
				settingsWindow.Invalidate()
			}
			mygo.App.SetMenu(BuildMenu(registry, tag))
			win.Invalidate()
		})

		if afterReady != nil {
			afterReady(win)
		}
	})

	return mygo.App.Run()
}

// newRegistry wires every command the shell exposes. Menu items and any later
// key binding or command palette dispatch through this one registry.
func newRegistry(shellState *state.State, quit func()) *command.Registry {
	registry := command.NewRegistry()
	register := func(id command.ID, handler command.Handler) {
		if err := registry.Register(id, handler); err != nil {
			panic(err)
		}
	}

	register(command.NewTab, func(context.Context) error {
		return shellState.NewTab()
	})
	register(command.CloseTab, func(context.Context) error {
		shellState.CloseActiveTab()
		return nil
	})
	register(command.Quit, func(context.Context) error {
		quit()
		return nil
	})
	for _, tag := range locales.Tags() {
		tag := tag
		register(command.LocaleCommandID(tag), func(context.Context) error {
			shellState.RequestLocale(tag)
			return nil
		})
	}

	return registry
}
