package app

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// predictionFactory records the Mosh configuration each Open received and
// hands back a transport that reports live display changes.
type predictionFactory struct {
	mu          sync.Mutex
	predictions []moshmodel.PredictionDisplay
	transports  []*predictionTransport
}

func (f *predictionFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	prediction := moshmodel.PredictionAdaptive
	if spec.Mosh != nil {
		prediction = spec.Mosh.PredictionDisplay.Normalized()
	}
	transport := &predictionTransport{
		done:    make(chan struct{}),
		updates: make(chan moshmodel.PredictionDisplay, 4),
	}
	f.mu.Lock()
	f.predictions = append(f.predictions, prediction)
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

func (f *predictionFactory) opened() []moshmodel.PredictionDisplay {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]moshmodel.PredictionDisplay(nil), f.predictions...)
}

func (f *predictionFactory) transport(index int) *predictionTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transports[index]
}

// predictionTransport is a Mosh-shaped Session transport. Read blocks until
// Close, so the Session's reader behaves as it does on a quiet peer.
type predictionTransport struct {
	closeOnce sync.Once
	done      chan struct{}
	updates   chan moshmodel.PredictionDisplay
}

func (t *predictionTransport) Read([]byte) (int, error) {
	<-t.done
	return 0, io.EOF
}

func (t *predictionTransport) Write(p []byte) (int, error) { return len(p), nil }

func (t *predictionTransport) Resize(int, int) error { return nil }

func (t *predictionTransport) SetPredictionDisplay(mode moshmodel.PredictionDisplay) {
	select {
	case t.updates <- mode:
	default:
	}
}

func (t *predictionTransport) Close() error {
	t.closeOnce.Do(func() { close(t.done) })
	return nil
}

func openMoshSession(t *testing.T, service *appsession.Service, label string) *appsession.Session {
	t.Helper()
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:  appsession.KindMosh,
		Label: label,
		Cols:  80,
		Rows:  24,
		Mosh:  &moshmodel.Config{Hostname: "mosh.example", Username: "deploy"},
	})
	if err != nil {
		t.Fatalf("open %s Session: %v", label, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func awaitPrediction(t *testing.T, transport *predictionTransport,
	want moshmodel.PredictionDisplay) {

	t.Helper()
	select {
	case got := <-transport.updates:
		if got != want {
			t.Fatalf("live prediction display = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("the running Session never received prediction display %q", want)
	}
}

// TestSavedTransportSettingReachesExistingAndNewPhoneSessions drives the same
// path the phone root wires: the shared preferences service persists the
// Transport group, its change hook pushes the value into the Session runtime,
// a Mosh Session that is already open is updated in place, and a Session
// opened afterwards starts with the saved value.
func TestSavedTransportSettingReachesExistingAndNewPhoneSessions(t *testing.T) {
	ctx := context.Background()
	factory := &predictionFactory{}
	service := appsession.NewService(factory)
	runtime := &phoneSessionRuntime{service: service}

	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(t.TempDir(), "preferences.json")),
		apppreferences.WithChangeHook(func(settings domainpreferences.Settings) {
			// This is the hook run_mobile installs.
			runtime.ApplyTransportSettings(settings)
		}))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("load preferences: %v", err)
	}
	// The phone root applies the loaded document before the first Session can
	// open.
	runtime.ApplyTransportSettings(preferences.Settings())

	existing := openMoshSession(t, service, "existing")
	if got := factory.opened(); len(got) != 1 || got[0] != moshmodel.PredictionAdaptive {
		t.Fatalf("new Session prediction displays = %v, want [adaptive]", got)
	}

	// Saving the Transport page's control is a normal preferences update.
	if err := preferences.Update(func(settings *domainpreferences.Settings) {
		settings.Transport.MoshPredictionDisplay = moshmodel.PredictionNever
	}); err != nil {
		t.Fatalf("save transport setting: %v", err)
	}
	awaitPrediction(t, factory.transport(0), moshmodel.PredictionNever)

	// A Session opened after the save reads the same document.
	created := openMoshSession(t, service, "created")
	if got := factory.opened(); len(got) != 2 || got[1] != moshmodel.PredictionNever {
		t.Fatalf("Session opened after the save = %v, want adaptive then never", got)
	}

	// Both Sessions stay independent; closing one must not disturb the rule.
	if err := existing.Close(); err != nil {
		t.Fatalf("close existing Session: %v", err)
	}
	select {
	case <-existing.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the existing Session never finished closing")
	}
	if created.Snapshot().Status == appsession.StatusClosed {
		t.Fatal("closing the existing Session also stopped the newly created one")
	}
}
