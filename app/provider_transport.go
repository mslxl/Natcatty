package app

import (
	"context"
	"errors"
	"sync"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	appsession "github.com/mslxl/natcatty/application/session"
	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// connectionProviderRegistrar publishes activated connection protocols into
// the Session provider registry. The registry is the single resolution point:
// the transport factory asks it before any built-in Kind routing, so a missing
// provider fails closed instead of falling back.
type connectionProviderRegistrar struct {
	registry *appsession.ProviderRegistry
}

var _ appplugin.ConnectionProviderRegistrar = (*connectionProviderRegistrar)(nil)

// RegisterConnectionProvider maps one provider-owned protocol onto the factory
// that opens its raw stream. The disposer is idempotent: an unload, a recycle,
// and a failed activation can each remove the same mapping once.
func (r *connectionProviderRegistrar) RegisterConnectionProvider(ref appplugin.PluginRef,
	contribution domainplugin.ConnectionProvider, provider *appplugin.Provider) (appplugin.Disposer, error) {

	if r == nil || r.registry == nil {
		return nil, errors.New("app: no Session provider registry")
	}
	id := protocolmodel.ID(contribution.Protocol)
	if err := r.registry.Register(id, &providerTransportFactory{provider: provider}); err != nil {
		return nil, err
	}
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() { r.registry.Unregister(id) })
		return nil
	}, nil
}

// providerTransportFactory opens one provider stream for a Session.
type providerTransportFactory struct {
	provider *appplugin.Provider
}

var _ appsession.Factory = (*providerTransportFactory)(nil)

func (f *providerTransportFactory) Open(ctx context.Context,
	spec appsession.StartSpec) (appsession.Transport, error) {

	stream, err := f.provider.Open(ctx, appplugin.ProviderOpenRequest{
		HostID:   string(spec.HostID),
		Hostname: spec.Hostname,
		Port:     spec.Port,
		Username: spec.Username,
		Label:    spec.Label,
		Cols:     spec.Cols,
		Rows:     spec.Rows,
		Config:   spec.ProviderConfig,
		Restore:  spec.Restore,
	})
	if err != nil {
		return nil, err
	}
	return &providerTransport{stream: stream}, nil
}

// providerTransport adapts a provider's raw byte stream to the Session
// transport port. Terminal emulation, dimensions, and rendering stay in the
// host; this adapter only moves bytes, resizes, signals, and classifies the
// typed end.
type providerTransport struct {
	stream *appplugin.ProviderStream
}

var (
	_ appsession.Transport        = (*providerTransport)(nil)
	_ appsession.EndReporter      = (*providerTransport)(nil)
	_ appsession.InitialSizer     = (*providerTransport)(nil)
	_ appsession.Signaler         = (*providerTransport)(nil)
	_ appsession.ProviderMetadata = (*providerTransport)(nil)
)

func (t *providerTransport) Read(p []byte) (int, error) { return t.stream.Read(p) }

func (t *providerTransport) Write(p []byte) (int, error) { return t.stream.Write(p) }

// Resize forwards the host terminal size when the provider declared resize. A
// provider without it still gets the host-side size; only the remote window is
// left unchanged, so an unsupported call is not a Session error.
func (t *providerTransport) Resize(cols, rows int) error {
	if err := t.stream.Resize(cols, rows); err != nil {
		if errors.Is(err, domainplugin.ErrProviderUnsupported) {
			return nil
		}
		return err
	}
	return nil
}

func (t *providerTransport) Close() error { return t.stream.Close() }

// End classifies the stream's end. A typed provider failure keeps its status
// and never carries stream or credential content.
func (t *providerTransport) End() appsession.End {
	if err := t.stream.Failure(); err != nil {
		end := appsession.End{Reason: appsession.EndReasonError}
		end.ProviderFailure = providerFailure(err)
		return end
	}
	return appsession.End{Reason: appsession.EndReasonClosed}
}

// InitialSize returns the dimensions the provider reported at open. The
// Session publishes them for the first Snapshot.
func (t *providerTransport) InitialSize() (int, int) { return t.stream.InitialSize() }

// ProviderID returns the owning plugin id.
func (t *providerTransport) ProviderID() string { return t.stream.ProviderID() }

// Protocol returns the provider-owned protocol id.
func (t *providerTransport) Protocol() string { return t.stream.Protocol() }

// RestoreDescriptor returns a copy of the bounded opaque restore state.
func (t *providerTransport) RestoreDescriptor() []byte {
	return t.stream.RestoreDescriptor()
}

// Signal delivers one declared signal to the provider.
func (t *providerTransport) Signal(name string) error { return t.stream.Signal(name) }

// providerFailure maps the plugin-level typed error onto the Session's closed
// status vocabulary. An unrecognized kind becomes protocol-error rather than
// inventing a status the UI cannot localize.
func providerFailure(err error) *appsession.ProviderFailure {
	var typed *domainplugin.ProviderError
	if !errors.As(err, &typed) {
		return &appsession.ProviderFailure{
			Status:  appsession.ProviderStatusFaulted,
			Message: "provider failed",
		}
	}
	status := appsession.ProviderStatus(typed.Kind)
	switch status {
	case appsession.ProviderStatusAuthFailed,
		appsession.ProviderStatusUnreachable,
		appsession.ProviderStatusHostKeyChanged,
		appsession.ProviderStatusProtocol,
		appsession.ProviderStatusTimeout,
		appsession.ProviderStatusCancelled,
		appsession.ProviderStatusFaulted:
	default:
		status = appsession.ProviderStatusProtocol
	}
	return &appsession.ProviderFailure{
		Status:   status,
		Provider: typed.Provider,
		Protocol: typed.Protocol,
		Message:  typed.Message,
	}
}
