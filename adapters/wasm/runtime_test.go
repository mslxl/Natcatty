package wasm_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/runtimecontract"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// contractLimits keeps deadline tests fast; the production defaults are
// asserted separately in the application package.
var contractLimits = appplugin.Limits{
	InitTimeout:   500 * time.Millisecond,
	CallTimeout:   time.Second,
	UnloadTimeout: 500 * time.Millisecond,
	WatchdogGrace: 100 * time.Millisecond,
}

// TestRuntimeContract runs the shared suite against the real wazero adapter.
// The fixture modules are checked into testdata so this is a real engine
// round trip, not a stub.
func TestRuntimeContract(t *testing.T) {
	runtimecontract.Run(t,
		func(t *testing.T) appplugin.Runtime {
			rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
			t.Cleanup(func() { _ = rt.Close(context.Background()) })
			return rt
		},
		func(t *testing.T, name string) appplugin.ModuleSpec {
			t.Helper()
			file := name
			if name == runtimecontract.FixtureStaleHandle {
				file = runtimecontract.FixtureHello
			}
			data, err := os.ReadFile(filepath.Join("testdata", file+".wasm"))
			if err != nil {
				t.Fatalf("read %s fixture: %v", name, err)
			}
			return appplugin.ModuleSpec{
				Name:           name,
				WASM:           data,
				MemoryMaxBytes: 64 << 20,
			}
		})
}

// TestInstantiateRejectsWrongSignature proves the IDL signature metadata is
// enforced before any guest code runs.
func TestInstantiateRejectsWrongSignature(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "bad-signature.wasm"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	_, err = rt.Instantiate(context.Background(), appplugin.ModuleSpec{
		Name:           "bad-signature",
		WASM:           data,
		MemoryMaxBytes: 64 << 20,
	}, appplugin.HostSink{})
	var signatureErr *domainpluginabi.SignatureError
	if !errors.As(err, &signatureErr) {
		t.Fatalf("Instantiate error = %v, want SignatureError", err)
	}
	if signatureErr.Function != domainpluginabi.ExportPoll {
		t.Fatalf("SignatureError.Function = %q", signatureErr.Function)
	}
}

// TestInstantiateRejectsExtraExport proves the runtime enforces the closed
// generated export set, not just the required exports.
func TestInstantiateRejectsExtraExport(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "extra-export.wasm"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	_, err = rt.Instantiate(context.Background(), appplugin.ModuleSpec{
		Name:           "extra-export",
		WASM:           data,
		MemoryMaxBytes: 64 << 20,
	}, appplugin.HostSink{})
	var unexpected *appplugin.UnexpectedExportError
	if !errors.As(err, &unexpected) {
		t.Fatalf("Instantiate error = %v, want UnexpectedExportError", err)
	}
	if unexpected.Name != "natcatty_extra" {
		t.Fatalf("UnexpectedExportError.Name = %q, want natcatty_extra", unexpected.Name)
	}
}

// TestInstantiateRejectsAModuleDeclaredMaximumAboveTheCap covers the
// successful-compilation path: wazero clamps the declared maximum to the
// runtime page limit, so instantiation must re-check the raw declaration
// rather than trusting the compiled definition.
func TestInstantiateRejectsAModuleDeclaredMaximumAboveTheCap(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "hello.wasm"))
	if err != nil {
		t.Fatalf("read hello fixture: %v", err)
	}
	rt := wasm.NewRuntime()
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	_, err = rt.Instantiate(context.Background(), appplugin.ModuleSpec{
		Name: "acme.memory", WASM: data, MemoryMaxBytes: 1 << 20,
	}, appplugin.HostSink{})
	var limit *appplugin.MemoryLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("Instantiate = %v, want MemoryLimitError", err)
	}
}

// TestInstantiateRejectsStartSectionBeforeExecution proves the runtime rejects
// a core start section before wazero instantiates the module. The fixture's
// start function logs through the host import, so any execution would be
// observable in the sink.
func TestInstantiateRejectsStartSectionBeforeExecution(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "start-section.wasm"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !strings.Contains(string(data), "start section ran") {
		t.Fatal("fixture does not carry the observable start function")
	}
	var logs []string
	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	_, err = rt.Instantiate(context.Background(), appplugin.ModuleSpec{
		Name:           "start-section",
		WASM:           data,
		MemoryMaxBytes: 64 << 20,
	}, appplugin.HostSink{
		Log: func(_ appplugin.LogLevel, message string) { logs = append(logs, message) },
	})
	var validation *appplugin.ModuleValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Instantiate error = %v, want ModuleValidationError", err)
	}
	if !strings.Contains(validation.Problem, "start section") {
		t.Fatalf("validation error = %q, want the start section rule", validation.Problem)
	}
	if len(logs) != 0 {
		t.Fatalf("the start function ran before validation: %v", logs)
	}
}

// TestDevelopmentPluginFixture proves the app's development-directory fixture
// has the module the contract expects.
func TestDevelopmentPluginFixture(t *testing.T) {
	for _, name := range []string{"plugin.toml", "plugin.wasm"} {
		if _, err := os.Stat(filepath.Join("testdata", "devplugin", name)); err != nil {
			t.Fatalf("development fixture is missing %s: %v", name, err)
		}
	}
}
