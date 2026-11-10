package app

import (
	"errors"
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	appview "github.com/mslxl/natcatty/application/view"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/pluginwebview"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// pluginWebviewStateSurface adapts one sandboxed page to the shell's Surface
// contract. Closing it destroys the native page and closes every pending
// bridge request; backend state stays with the plugin.
type pluginWebviewStateSurface struct {
	renderer *pluginwebview.Surface
	surface  *appplugin.WebviewSurface
}

func (s *pluginWebviewStateSurface) View(c *ui.Context) ui.Element {
	return s.renderer.View(c)
}

func (s *pluginWebviewStateSurface) Close() {
	if s == nil {
		return
	}
	if s.renderer != nil {
		s.renderer.Close()
	}
	if s.surface != nil {
		_ = s.surface.Close()
	}
}

// newPluginWebviewSurface builds the standalone webview View. The page is
// served from the plugin package in the plugin's own origin; the contribution
// supplies the entry and the fixed bridge allowlist.
func newPluginWebviewSurface(instance *appview.Instance, manager *appplugin.Manager,
	host pluginViewHost, invalidate func()) (state.Surface, error) {
	if instance == nil {
		return nil, errors.New("plugin webview: no instance")
	}
	ref := pluginRefForInstance(instance)
	webviews, ok := manager.Container().Webviews(ref)
	if !ok {
		return nil, fmt.Errorf("plugin webview %q: the host webview port is not available",
			instance.ViewID())
	}
	target := any(nil)
	if host.Window != nil {
		target = host.Window()
	}
	if target == nil {
		return nil, fmt.Errorf("plugin webview %q: the main window is not ready", instance.ViewID())
	}
	contribution := instance.Contribution()
	var renderer *pluginwebview.Surface
	surface, err := webviews.OpenWebview(appplugin.WebviewSurfaceRequest{
		Entry:   contribution.Entry,
		Methods: contribution.Bridge,
		Target:  target,
		Title:   instance.Title(),
		OnCrash: func(string) {
			if renderer != nil {
				renderer.SetProblem(pluginwebview.ProblemCrashed)
			}
			if invalidate != nil {
				invalidate()
			}
		},
		OnRequest: webviewWake(target, invalidate),
	})
	if err != nil {
		return nil, err
	}
	var optionList []pluginwebview.Option
	if session, ok := manager.Container().NativeUI(ref); ok {
		poll := pluginViewPoll(func() *appplugin.Manager { return manager }, instance.ViewID())
		optionList = append(optionList, pluginwebview.WithPoll(func() {
			if session.PendingEvents() > 0 {
				poll(ref)
			}
		}))
	}
	renderer = pluginwebview.New(func() ui.NativeWebView {
		native, _ := surface.Native().(ui.NativeWebView)
		return native
	}, optionList...)
	return &pluginWebviewStateSurface{renderer: renderer, surface: surface}, nil
}

// webviewWake asks for a host frame when a page bridge request is queued. The
// bridge caller is not the UI thread, so the real window posts through Update;
// a test target without that method falls back to the redraw callback.
func webviewWake(target any, invalidate func()) func() {
	return func() {
		if updater, ok := target.(interface{ Update(func()) }); ok {
			updater.Update(func() {})
			return
		}
		if invalidate != nil {
			invalidate()
		}
	}
}

// pluginRefForInstance converts a view instance owner to the plugin identity.
func pluginRefForInstance(instance *appview.Instance) appplugin.PluginRef {
	owner := instance.Owner()
	return appplugin.PluginRef{
		ID:          owner.PluginID,
		Name:        owner.PluginName,
		Version:     owner.Version,
		Development: owner.Development,
		Source:      owner.Source,
	}
}

// embeddedWebview is one webview node's native page and renderer.
type embeddedWebview struct {
	surface  *appplugin.WebviewSurface
	renderer *pluginwebview.Surface
	entry    string
	problem  pluginwebview.Problem
}

// pluginEmbeddedWebviews owns the native pages of the webview nodes in one
// native View. The application UISession is per fiber, so every node is keyed
// by its guest node id and closed when the node disappears or the native view
// closes; a page crash touches only that node.
type pluginEmbeddedWebviews struct {
	webviews   *appplugin.WebviewSession
	target     any
	methods    []string
	title      string
	invalidate func()

	mu          sync.Mutex
	entries     map[domainnativeui.NodeID]*embeddedWebview
	closed      bool
	unsubscribe func()
}

func newPluginEmbeddedWebviews(webviews *appplugin.WebviewSession, target any,
	methods []string, title string, invalidate func()) *pluginEmbeddedWebviews {
	return &pluginEmbeddedWebviews{
		webviews:   webviews,
		target:     target,
		methods:    append([]string(nil), methods...),
		title:      title,
		invalidate: invalidate,
		entries:    make(map[domainnativeui.NodeID]*embeddedWebview),
	}
}

// subscribe starts following the retained tree. Creation and destruction of
// webview nodes then mirror page creation and destruction.
func (m *pluginEmbeddedWebviews) subscribe(session *appplugin.UISession) {
	if m == nil || session == nil {
		return
	}
	m.mu.Lock()
	m.unsubscribe = session.SubscribeUpdates(m.reconcile)
	m.mu.Unlock()
	m.reconcile(session.Tree())
}

// reconcile opens pages for new webview nodes, closes pages for nodes that
// disappeared or changed entry, and leaves a matching page untouched.
func (m *pluginEmbeddedWebviews) reconcile(tree *domainnativeui.Tree) {
	if m == nil || tree == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	desired := make(map[domainnativeui.NodeID]string)
	for id, node := range tree.Nodes {
		if node != nil && node.Kind == domainnativeui.NodeWebview {
			desired[id] = node.StringProp(domainnativeui.PropSrc)
		}
	}
	var closing []*embeddedWebview
	for id, entry := range m.entries {
		if src, ok := desired[id]; !ok || src != entry.entry {
			if entry.renderer != nil {
				entry.renderer.SetProblem(pluginwebview.ProblemUnavailable)
			}
			closing = append(closing, entry)
			delete(m.entries, id)
		}
	}
	m.mu.Unlock()
	for _, entry := range closing {
		if entry.surface != nil {
			_ = entry.surface.Close()
		}
	}

	for id, src := range desired {
		m.mu.Lock()
		_, exists := m.entries[id]
		closed := m.closed
		m.mu.Unlock()
		if exists || closed {
			continue
		}
		entry := &embeddedWebview{entry: src}
		entry.renderer = pluginwebview.New(func() ui.NativeWebView {
			if entry.surface == nil {
				return nil
			}
			native, _ := entry.surface.Native().(ui.NativeWebView)
			return native
		})
		surface, err := m.webviews.OpenWebview(appplugin.WebviewSurfaceRequest{
			Entry:    src,
			Methods:  m.methods,
			Target:   m.target,
			Embedded: true,
			Title:    m.title,
			OnCrash: func(string) {
				entry.renderer.SetProblem(pluginwebview.ProblemCrashed)
				if m.invalidate != nil {
					m.invalidate()
				}
			},
			OnRequest: webviewWake(m.target, m.invalidate),
		})
		if err != nil {
			entry.problem = pluginwebview.ProblemUnavailable
		} else {
			entry.surface = surface
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			if entry.surface != nil {
				_ = entry.surface.Close()
			}
			continue
		}
		m.entries[id] = entry
		m.mu.Unlock()
	}
}

// render places one webview node's page. A node whose page could not open
// shows a readable line instead of an empty hole.
func (m *pluginEmbeddedWebviews) render(c *ui.Context, node *domainnativeui.Node) ui.Element {
	messages := locales.In(c.Locale())
	if m == nil || node == nil {
		return ui.Text(c, messages.PluginWebviewEmbeddedUnavailable())
	}
	m.mu.Lock()
	entry := m.entries[node.ID]
	m.mu.Unlock()
	if entry == nil {
		return ui.Text(c, messages.PluginWebviewEmbeddedUnavailable())
	}
	if entry.problem != pluginwebview.ProblemNone {
		entry.renderer.SetProblem(entry.problem)
	}
	return entry.renderer.View(c)
}

func (m *pluginEmbeddedWebviews) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	unsubscribe := m.unsubscribe
	m.unsubscribe = nil
	entries := make([]*embeddedWebview, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	m.entries = make(map[domainnativeui.NodeID]*embeddedWebview)
	m.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
	for _, entry := range entries {
		if entry.surface != nil {
			_ = entry.surface.Close()
		}
	}
}
