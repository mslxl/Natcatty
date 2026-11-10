// Package sample is the first-party sample plugin. It is a normal Go package
// compiled into the app: it declares the same Plugin Manifest shape a package
// uses, is loaded into the same fiber tree, and publishes a command and a View
// through the same contribution registry. Its Setup is a direct Go call, so no
// module crosses the WASM ABI.
package sample

import (
	"context"
	"fmt"
	"sync"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Manifest is the compiled-in manifest. The manager replaces Version with the
// app version when it registers the plugin, so the sample always tracks the
// app it ships with.
var Manifest = domainplugin.Manifest{
	ManifestVersion: 1,
	ID:              "natcatty.sample",
	Name:            "Sample Built-in",
	Version:         "0.0.0",
	PluginAPI:       "^0.1",
	DefaultLocale:   "en",
	Description:     "First-party sample contributing a command and a counter View.",
	Permissions: domainplugin.Permissions{
		Required: []string{
			string(domainplugin.PermissionCommands),
			string(domainplugin.PermissionViews),
			string(domainplugin.PermissionUINative),
		},
	},
	Contributions: domainplugin.Contributions{
		Commands: []domainplugin.Command{{
			ID:       "increment",
			Title:    "Sample: Increment Counter",
			Keywords: []string{"sample", "counter"},
		}},
		Views: []domainplugin.ViewContribution{{
			ID:         "counter",
			Title:      "Sample Counter",
			Placements: []domainplugin.Placement{domainplugin.PlacementPane},
			Instances:  domainplugin.InstanceSingleton,
		}},
	},
}

// Sample node identities. They are host-local to this built-in and never cross
// a wire.
const (
	rootNode   domainnativeui.NodeID = 1
	countNode  domainnativeui.NodeID = 2
	buttonNode domainnativeui.NodeID = 3
)

// Setup builds the sample's native UI tree and returns the instance the
// container dispatches commands and UI polls to.
func Setup(ctx context.Context, host *appplugin.BuiltinHost) (appplugin.BuiltinInstance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	instance := &sampleInstance{host: host, ui: host.NativeUI()}
	if instance.ui != nil {
		if _, err := instance.ui.Apply(initialBatch()); err != nil {
			return nil, fmt.Errorf("sample: build view: %w", err)
		}
		if _, err := instance.ui.Listen(
			domainnativeui.IDFromNode(buttonNode), domainnativeui.EventClick); err != nil {
			return nil, fmt.Errorf("sample: listen for click: %w", err)
		}
	}
	if err := host.Dispose(func(context.Context) error {
		host.Log(appplugin.LogInfo, "sample disposed")
		return nil
	}); err != nil {
		return nil, err
	}
	host.Log(appplugin.LogInfo, "sample ready")
	return instance, nil
}

// initialBatch creates the counter View: a column with the count text and an
// Increment button.
func initialBatch() domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(rootNode),
		Ops: []domainnativeui.Op{
			{Kind: domainnativeui.OpCreate, ID: rootNode, NodeKind: domainnativeui.NodeContainer},
			{Kind: domainnativeui.OpCreate, ID: countNode, NodeKind: domainnativeui.NodeText},
			{Kind: domainnativeui.OpCreate, ID: buttonNode, NodeKind: domainnativeui.NodeButton},
			{
				Kind: domainnativeui.OpSetProp, Node: domainnativeui.IDFromNode(rootNode),
				Prop:  domainnativeui.PropLayout,
				Value: domainnativeui.IntValue(int64(domainnativeui.LayoutColumn)),
			},
			{
				Kind: domainnativeui.OpSetProp, Node: domainnativeui.IDFromNode(rootNode),
				Prop: domainnativeui.PropGap, Value: domainnativeui.FloatValue(8),
			},
			{
				Kind: domainnativeui.OpSetText, Node: domainnativeui.IDFromNode(countNode),
				Text: "Count: 0",
			},
			{
				Kind: domainnativeui.OpSetProp, Node: domainnativeui.IDFromNode(buttonNode),
				Prop: domainnativeui.PropLabel, Value: domainnativeui.StringValue("Increment"),
			},
			{
				Kind: domainnativeui.OpSetChildren, Node: domainnativeui.IDFromNode(rootNode),
				Children: []domainnativeui.NodeRef{
					domainnativeui.IDFromNode(countNode),
					domainnativeui.IDFromNode(buttonNode),
				},
			},
		},
	}
}

// sampleInstance owns the counter. Container calls are serialized, so the
// mutex only protects a reader that bypasses the container in a test.
type sampleInstance struct {
	mu    sync.Mutex
	ui    *appplugin.UISession
	host  *appplugin.BuiltinHost
	count int
}

// Dispatch handles the qualified command identity the contribution registry
// published.
func (s *sampleInstance) Dispatch(ctx context.Context, command string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if command != domainplugin.QualifiedCommandID(Manifest.ID, "increment") {
		return fmt.Errorf("sample: unknown command %q", command)
	}
	return s.increment()
}

// Poll drains click events queued by the View renderer and applies the same
// increment path a command uses.
func (s *sampleInstance) Poll(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ui == nil {
		return nil
	}
	for _, event := range s.ui.Poll(0) {
		if event.Kind != domainnativeui.EventClick {
			continue
		}
		if err := s.increment(); err != nil {
			return err
		}
	}
	return nil
}

// Shutdown runs within the container's unload deadline. The sample owns no
// resource the fiber effect stack does not already release.
func (s *sampleInstance) Shutdown(ctx context.Context) error {
	s.host.Log(appplugin.LogInfo, "sample shutdown")
	return ctx.Err()
}

func (s *sampleInstance) increment() error {
	s.mu.Lock()
	s.count++
	count := s.count
	s.mu.Unlock()
	text := fmt.Sprintf("Count: %d", count)
	if s.ui != nil {
		if _, err := s.ui.Apply(domainnativeui.Batch{Ops: []domainnativeui.Op{{
			Kind: domainnativeui.OpSetText,
			Node: domainnativeui.IDFromNode(countNode),
			Text: text,
		}}}); err != nil {
			return fmt.Errorf("sample: update count: %w", err)
		}
	}
	s.host.Log(appplugin.LogInfo, "sample increment "+text)
	s.host.Output([]byte("sample " + text))
	return nil
}
