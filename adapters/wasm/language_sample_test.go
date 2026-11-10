package wasm_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// sampleLanguageModule loads the committed language-provider sample. The
// adapter layer may not import internal/testutil, so the test reads the module
// by its module-relative path; the artifact is checked in and the sample's own
// e2e test verifies its manifest and hash.
func sampleLanguageModule(t *testing.T) appplugin.ModuleSpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "sdk", "samples",
		"language-provider", "plugin.wasm"))
	if err != nil {
		t.Fatalf("read the committed language sample: %v", err)
	}
	return appplugin.ModuleSpec{
		Name: "acme.language-provider", WASM: data, MemoryMaxBytes: 64 << 20,
	}
}

// TestLanguageProviderSampleHonorsCancellation drives the committed sample
// through the real wazero bridge. The guest polls language_cancelled at the
// top of its request export; when the host reports cancellation it returns
// without calling language_respond, which is exactly the outcome the router
// must classify as canceled rather than as a provider fault. The optional
// natcatty_language_cancel export is exercised through the same bridge.
func TestLanguageProviderSampleHonorsCancellation(t *testing.T) {
	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })

	var canceledMu sync.Mutex
	canceled := make(map[uint64]bool)
	instance, err := rt.Instantiate(context.Background(), sampleLanguageModule(t),
		appplugin.HostSink{
			LanguageCancelled: func(requestID uint64) bool {
				canceledMu.Lock()
				defer canceledMu.Unlock()
				return canceled[requestID]
			},
		})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = instance.Close(context.Background()) }()
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	capable, ok := instance.(appplugin.LanguageCapableInstance)
	if !ok {
		t.Fatal("the sample does not expose the language provider exports")
	}

	// A normal request proves the sample is alive and answering on this
	// bridge instance.
	payload, err := capable.LanguageRequest(context.Background(),
		appplugin.LanguageRequest{
			ID: 1, Kind: domainplugin.LanguageCompletion,
			Payload: []byte(`{"version":1,"content":"select 1"}`),
		})
	if err != nil {
		t.Fatalf("LanguageRequest: %v", err)
	}
	if !strings.Contains(string(payload), "sample_keyword") {
		t.Fatalf("completion payload = %q", payload)
	}

	// Supersede request 2 before the guest runs its request export. The real
	// guest observes the poll and returns without a response.
	canceledMu.Lock()
	canceled[2] = true
	canceledMu.Unlock()
	_, err = capable.LanguageRequest(context.Background(), appplugin.LanguageRequest{
		ID: 2, Kind: domainplugin.LanguageDiagnostics,
		Payload: []byte(`{"version":1,"content":"-- TODO: fix"}`),
	})
	var noResponse *appplugin.LanguageNoResponseError
	if !errors.As(err, &noResponse) {
		t.Fatalf("cancelled request error = %v, want LanguageNoResponseError", err)
	}
	if noResponse.Kind != domainplugin.LanguageDiagnostics {
		t.Fatalf("LanguageNoResponseError.Kind = %q", noResponse.Kind)
	}

	// The committed sample's optional cancel export runs through the real
	// bridge without needing the request call slot.
	if err := capable.CancelLanguage(context.Background(), 2); err != nil {
		t.Fatalf("CancelLanguage export: %v", err)
	}
}
