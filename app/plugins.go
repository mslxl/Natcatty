package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/command"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	appview "github.com/mslxl/natcatty/application/view"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/ui/pluginview"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// pluginSurface publishes plugin contributions into the app's command
// registry, native menu, and window shortcut table. Each registration
// returns a disposer, so unload removes every surface the plugin touched.
type pluginSurface struct {
	registry *command.Registry

	mu       sync.Mutex
	commands map[command.ID]pluginCommandInfo
	menus    []pluginMenuEntry
	keys     []pluginKeyEntry
	chords   map[pluginChord]bool
	views    *appview.Registry
	openView func(string) error
	next     uint64
	onChange func()
	// localize resolves a manifest *_key at display time. It is set after the
	// plugin manager exists, so registration never depends on it.
	localize func(appplugin.PluginRef, string, string) string
}

type pluginCommandInfo struct {
	ref      appplugin.PluginRef
	title    string
	titleKey string
	keywords []string
	chord    string
}

type pluginMenuEntry struct {
	ref      appplugin.PluginRef
	label    string
	labelKey string
	command  command.ID
	id       string
}

type pluginKeyEntry struct {
	command     command.ID
	accelerator string
	mods        ui.Modifiers
	key         ui.Key
}

type pluginChord struct {
	mods ui.Modifiers
	key  ui.Key
}

// pluginViewHost is the host-owned half a plugin View Pane runs against: the
// live UI environment (including the window shortcut table), the window
// command dispatch that runs a host shortcut the view did not capture, and
// the guest poll driver. Production wires every field to the running app; a
// test that reads the session's event queue directly may pass a nil poll.
type pluginViewHost struct {
	Environment func() appplugin.UIEnvironment
	RunCommand  func(string)
	Poll        func(appplugin.PluginRef)
	// ResetOnClose keeps the one UISession per fiber alive while another View
	// Instance of the same plugin remains. Nil preserves ticket 11's reset on
	// every surface close, which is right for a single-instance view.
	ResetOnClose func() bool
	// Window returns the native window that hosts plugin webviews. Nil keeps
	// webview surfaces unavailable, which is right for headless tests.
	Window func() any
}

// pluginViewEnvironment returns the live host environment for a plugin view.
// Locale, direction, dark theme, and reduced motion match the window's frame;
// shortcuts are the same window accelerator tables the app binds, so a view
// sees exactly the chords that can reach it.
func pluginViewEnvironment(shortcuts func() []domainnativeui.Shortcut) func() appplugin.UIEnvironment {
	return func() appplugin.UIEnvironment {
		locale := mygo.App.Locale()
		direction := appplugin.UIDirectionLTR
		if i18n.Direction(locale) == i18n.RightToLeft {
			direction = appplugin.UIDirectionRTL
		}
		var keys []domainnativeui.Shortcut
		if shortcuts != nil {
			keys = shortcuts()
		}
		return appplugin.UIEnvironment{
			Locale:       locale,
			Direction:    direction,
			Dark:         mygo.Theme.IsDark(),
			ReduceMotion: mygo.Theme.Preferences().ReduceMotion,
			Shortcuts:    keys,
		}
	}
}

// pluginViewPoll drives the guest poll export for one live View. It runs at
// the start of a frame, never inside the input callback that queued the
// event, so the guest cannot re-enter the UI.
func pluginViewPoll(manager func() *appplugin.Manager, viewID string) func(appplugin.PluginRef) {
	return func(ref appplugin.PluginRef) {
		current := manager()
		if current == nil {
			return
		}
		if err := current.Container().PollUI(context.Background(), ref); err != nil &&
			!errors.Is(err, appplugin.ErrCallInFlight) {
			fmt.Fprintf(os.Stderr, "natcatty: plugin view %s poll: %v\n", viewID, err)
		}
	}
}

// newPluginViewSurface builds the native UI renderer for one live View
// Instance. It resolves the plugin's UISession through the container, so the
// renderer never owns a second registry or a second permission path. Polling
// runs at the start of the next frame, only while events are queued, and the
// guest commits patches through the same UI session the renderer reads.
func newPluginViewSurface(instance *appview.Instance, manager func() *appplugin.Manager,
	host pluginViewHost, invalidate func()) (state.Surface, error) {
	if instance == nil {
		return nil, errors.New("plugin view: no instance")
	}
	current := manager()
	if current == nil {
		return nil, errors.New("plugin view: the plugin manager is not ready")
	}
	if instance.Contribution().Surface.Webview() {
		return newPluginWebviewSurface(instance, current, host, invalidate)
	}
	ref := pluginRefForInstance(instance)
	ui, ok := current.Container().NativeUI(ref)
	if !ok {
		return nil, fmt.Errorf("plugin view %q: the plugin is not active", instance.ViewID())
	}
	options := []pluginview.Option{pluginview.WithUpdate(invalidate)}
	if host.Environment != nil {
		options = append(options, pluginview.WithEnvironment(host.Environment))
	}
	if host.RunCommand != nil {
		options = append(options, pluginview.WithCommandRunner(host.RunCommand))
	}
	if host.Poll != nil {
		poll := host.Poll
		options = append(options, pluginview.WithPoll(func() { poll(ref) }))
	}
	if host.ResetOnClose != nil {
		options = append(options, pluginview.WithResetOnClose(host.ResetOnClose))
	}
	// A native View may contain embedded webview nodes. The host gives each
	// node a sandboxed page from the same per-plugin session, keyed by node and
	// closed when the node or the view goes away.
	if host.Window != nil {
		if webviews, ok := current.Container().Webviews(ref); ok {
			if target := host.Window(); target != nil {
				contribution := instance.Contribution()
				embedded := newPluginEmbeddedWebviews(webviews, target,
					contribution.Bridge, instance.Title(), invalidate)
				options = append(options, pluginview.WithWebview(embedded.render),
					pluginview.WithOnClose(embedded.close))
				embedded.subscribe(ui)
			}
		}
	}
	// Packaged images and fonts resolve inside the plugin's own package
	// directory, never from a URL or the rest of the filesystem.
	if fiber, ok := current.Container().FiberByRef(ref); ok {
		assets := pluginview.NewPackagedAssets(fiber.Directory())
		options = append(options, pluginview.WithAssets(assets.Resolve), pluginview.WithFonts(assets.ResolveFont))
	}
	return pluginview.New(ui, options...), nil
}

// anotherInstanceOfSamePluginLive reports whether one plugin still has a live View
// Instance besides instance. A UISession is per fiber, so closing the last
// surface of a plugin may reset it; closing one of several must not.
func anotherInstanceOfSamePluginLive(registry *appview.Registry, instance *appview.Instance) bool {
	if registry == nil || instance == nil {
		return false
	}
	owner := instance.Owner().PluginID
	for _, other := range registry.Instances() {
		if other.ID() == instance.ID() || other.Closed() {
			continue
		}
		if other.Owner().PluginID == owner {
			return true
		}
	}
	return false
}

// pluginRuntimeStartupLine is the one startup-log record of the plugin engine
// mode. Support uses it to tell a compiler build from interpreter
// compatibility mode or an unsupported platform.
func pluginRuntimeStartupLine(status appplugin.RuntimeStatus) string {
	return "natcatty: " + status.StartupLogLine()
}

// newPluginSurface returns a surface over the one app command registry.
func newPluginSurface(registry *command.Registry) *pluginSurface {
	return &pluginSurface{
		registry: registry,
		commands: make(map[command.ID]pluginCommandInfo),
		chords:   make(map[pluginChord]bool),
	}
}

// SetViewRegistry gives the surface the application view registry. With it
// the surface implements appplugin.ViewRegistrar, so the container registers
// each declared View Contribution; a nil registry leaves views unregistered.
func (s *pluginSurface) SetViewRegistry(registry *appview.Registry) {
	s.mu.Lock()
	s.views = registry
	s.mu.Unlock()
}

// SetViewOpener gives the surface the host action that opens a registered
// view as a Pane. The View menu lists every registered contribution when it
// is set.
func (s *pluginSurface) SetViewOpener(fn func(string) error) {
	s.mu.Lock()
	s.openView = fn
	s.mu.Unlock()
}

// RegisterView publishes one View Contribution through the application view
// registry under its owner. The disposer removes it and closes its instances;
// the container puts it on the fiber's effect stack.
func (s *pluginSurface) RegisterView(ref appplugin.PluginRef,
	contribution domainplugin.ViewContribution) (appplugin.Disposer, error) {
	s.mu.Lock()
	registry := s.views
	s.mu.Unlock()
	if registry == nil {
		return nil, fmt.Errorf("view %q: the host has no view registry", contribution.ID)
	}
	dispose, err := registry.Register(appview.Owner{
		PluginID:    ref.ID,
		PluginName:  ref.Name,
		Version:     ref.Version,
		Development: ref.Development,
		Source:      ref.Source,
	}, contribution)
	if err != nil {
		return nil, err
	}
	s.changed()

	var once sync.Once
	return func(context.Context) error {
		once.Do(dispose)
		s.changed()
		return nil
	}, nil
}

// SetLocalizer installs the display-time contribution localizer. Without one
// every string uses its manifest literal.
func (s *pluginSurface) SetLocalizer(fn func(appplugin.PluginRef, string, string) string) {
	s.mu.Lock()
	s.localize = fn
	s.mu.Unlock()
}

// RefreshLocale re-resolves every contributed surface after a host locale
// change. No plugin instance reloads; menu labels are rebuilt by the change
// hook and command/view titles are read live on the next render.
func (s *pluginSurface) RefreshLocale() {
	s.changed()
}

// SetChangeHook observes menu or shortcut changes. The app rebuilds the menu
// and shortcut tables; a command-only change needs no rebuild because the
// command palette reads the registry live.
func (s *pluginSurface) SetChangeHook(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

func (s *pluginSurface) changed() {
	s.mu.Lock()
	fn := s.onChange
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// RegisterCommand adds one command to the app registry and palette.
func (s *pluginSurface) RegisterCommand(ref appplugin.PluginRef,
	contribution domainplugin.Command, run appplugin.Handler) (appplugin.Disposer, error) {
	qualified := command.ID(domainplugin.QualifiedCommandID(ref.ID, contribution.ID))
	if err := s.registry.Register(qualified, func(ctx context.Context) error {
		return run(ctx)
	}); err != nil {
		return nil, fmt.Errorf("command %q: %w", qualified, err)
	}

	s.mu.Lock()
	s.commands[qualified] = pluginCommandInfo{
		ref:      ref,
		title:    contribution.Title,
		titleKey: contribution.TitleKey,
		keywords: append([]string(nil), contribution.Keywords...),
	}
	s.mu.Unlock()

	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			s.mu.Lock()
			delete(s.commands, qualified)
			s.mu.Unlock()
			_ = s.registry.Unregister(qualified)
		})
		return nil
	}, nil
}

// RegisterMenu adds one native menu item that runs a contributed command.
func (s *pluginSurface) RegisterMenu(ref appplugin.PluginRef,
	contribution domainplugin.Menu) (appplugin.Disposer, error) {
	qualified := command.ID(domainplugin.QualifiedCommandID(ref.ID, contribution.Command))
	s.mu.Lock()
	if _, ok := s.commands[qualified]; !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("menu item references unregistered command %q", qualified)
	}
	s.next++
	entry := pluginMenuEntry{
		ref:      ref,
		label:    contribution.Label,
		labelKey: contribution.LabelKey,
		command:  qualified,
		id:       "plugin-menu-" + strconv.FormatUint(s.next, 10),
	}
	s.menus = append(s.menus, entry)
	s.mu.Unlock()
	s.changed()

	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			s.mu.Lock()
			for i := range s.menus {
				if s.menus[i].id == entry.id {
					s.menus = append(s.menus[:i], s.menus[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
			s.changed()
		})
		return nil
	}, nil
}

// RegisterKeybinding adds one window-level accelerator. Invalid or duplicate
// accelerators fail the registration, which makes the plugin FAILED instead
// of silently binding the wrong action.
func (s *pluginSurface) RegisterKeybinding(ref appplugin.PluginRef,
	contribution domainplugin.Keybinding) (appplugin.Disposer, error) {
	qualified := command.ID(domainplugin.QualifiedCommandID(ref.ID, contribution.Command))
	mods, key, ok := parseAccelerator(contribution.Accelerator)
	if !ok {
		return nil, fmt.Errorf("keybinding %q is not a supported accelerator", contribution.Accelerator)
	}
	chord := pluginChord{mods: mods, key: key}

	s.mu.Lock()
	if _, ok := s.commands[qualified]; !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("keybinding references unregistered command %q", qualified)
	}
	if s.chords[chord] {
		s.mu.Unlock()
		return nil, fmt.Errorf("keybinding %q is already bound", contribution.Accelerator)
	}
	s.chords[chord] = true
	entry := pluginKeyEntry{
		command:     qualified,
		accelerator: contribution.Accelerator,
		mods:        mods,
		key:         key,
	}
	s.keys = append(s.keys, entry)
	if info, ok := s.commands[qualified]; ok {
		info.chord = contribution.Accelerator
		s.commands[qualified] = info
	}
	s.mu.Unlock()
	s.changed()

	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			s.mu.Lock()
			delete(s.chords, chord)
			for i := range s.keys {
				if s.keys[i].mods == mods && s.keys[i].key == key {
					s.keys = append(s.keys[:i], s.keys[i+1:]...)
					break
				}
			}
			// Keep the palette shortcut if another binding for this command
			// survives this disposer.
			chord := ""
			for _, remaining := range s.keys {
				if remaining.command == qualified {
					chord = remaining.accelerator
				}
			}
			if info, ok := s.commands[qualified]; ok {
				info.chord = chord
				s.commands[qualified] = info
			}
			s.mu.Unlock()
			s.changed()
		})
		return nil
	}, nil
}

// pluginRefFromOwner reconstructs the plugin identity used by contribution
// localization from a view owner.
func pluginRefFromOwner(owner appview.Owner) appplugin.PluginRef {
	return appplugin.PluginRef{
		ID:          owner.PluginID,
		Name:        owner.PluginName,
		Version:     owner.Version,
		Development: owner.Development,
		Source:      owner.Source,
	}
}

// pluginCommandPresentation is the palette information for a plugin command.
type pluginCommandPresentation struct {
	title    string
	owner    string
	keywords []string
	chord    string
}

// Presentation returns palette metadata for a contributed command.
func (s *pluginSurface) Presentation(id command.ID) (pluginCommandPresentation, bool) {
	if s == nil {
		return pluginCommandPresentation{}, false
	}
	s.mu.Lock()
	info, ok := s.commands[id]
	localize := s.localize
	s.mu.Unlock()
	if !ok {
		return pluginCommandPresentation{}, false
	}
	title := info.title
	if localize != nil && info.titleKey != "" {
		title = localize(info.ref, info.titleKey, info.title)
	}
	return pluginCommandPresentation{
		title:    title,
		owner:    info.ref.ID,
		keywords: append([]string(nil), info.keywords...),
		chord:    info.chord,
	}, true
}

// MenuItems returns the contributed menu items in registration order. The
// app appends them to the View menu, after a separator.
func (s *pluginSurface) MenuItems() []*mygo.MenuItem {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	entries := append([]pluginMenuEntry(nil), s.menus...)
	registry := s.views
	openView := s.openView
	localize := s.localize
	s.mu.Unlock()

	commands := make([]*mygo.MenuItem, 0, len(entries))
	for _, entry := range entries {
		entry := entry
		label := entry.label
		if localize != nil && entry.labelKey != "" {
			label = localize(entry.ref, entry.labelKey, entry.label)
		}
		commands = append(commands, &mygo.MenuItem{
			ID:    entry.id,
			Label: label,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				if err := s.registry.Dispatch(context.Background(), entry.command); err != nil {
					mygo.Dialog.Error(entry.ref.Name, err.Error())
				}
			},
		})
	}
	var viewItems []*mygo.MenuItem
	if registry != nil && openView != nil {
		for _, contribution := range registry.Contributions() {
			contribution := contribution
			label := contribution.Title
			if localize != nil && contribution.TitleKey != "" {
				label = localize(pluginRefFromOwner(contribution.Owner),
					contribution.TitleKey, contribution.Title)
			}
			if label == "" {
				label = contribution.LocalID
			}
			viewItems = append(viewItems, &mygo.MenuItem{
				ID:    "plugin-view-" + contribution.ID,
				Label: label,
				Click: func(*mygo.MenuItem, *mygo.Window) {
					if err := openView(contribution.ID); err != nil {
						mygo.Dialog.Error(contribution.Owner.PluginName, err.Error())
					}
				},
			})
		}
	}
	// One separator introduces the plugin section, and a second divides the
	// contributed commands from the registered views. An empty group adds
	// neither, so a configured view registry with no contributions does not
	// leave a lone separator in the View menu.
	items := make([]*mygo.MenuItem, 0, len(commands)+len(viewItems)+2)
	groups := [][]*mygo.MenuItem{commands, viewItems}
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		if len(items) > 0 {
			items = append(items, mygo.Separator())
		}
		items = append(items, group...)
	}
	if len(items) == 0 {
		return nil
	}
	return append([]*mygo.MenuItem{mygo.Separator()}, items...)
}

// Shortcuts returns the contributed accelerators in registration order.
func (s *pluginSurface) Shortcuts() []keyShortcut {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	entries := append([]pluginKeyEntry(nil), s.keys...)
	s.mu.Unlock()
	shortcuts := make([]keyShortcut, 0, len(entries))
	for _, entry := range entries {
		entry := entry
		shortcuts = append(shortcuts, keyShortcut{
			mods: entry.mods,
			key:  entry.key,
			run: func() {
				_ = s.registry.Dispatch(context.Background(), entry.command)
			},
		})
	}
	return shortcuts
}

// maxFeedbackBytes bounds one visible guest output at a dialog-friendly size.
const maxFeedbackBytes = 4000

// truncateUTF8 cuts text to at most limit bytes without splitting a rune.
func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// ShowFeedback renders the guest's visible output. A development plugin is
// labelled in the dialog title.
func (s *pluginSurface) ShowFeedback(ref appplugin.PluginRef, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(text) > maxFeedbackBytes {
		text = truncateUTF8(text, maxFeedbackBytes) + "…"
	}
	title := ref.Name
	if ref.Development {
		title += " (development)"
	}
	mygo.RunOnMain(func() {
		_, _ = mygo.Dialog.Message(mygo.MessageOptions{
			Type:    mygo.MessageInfo,
			Title:   title,
			Message: text,
		})
	})
}
