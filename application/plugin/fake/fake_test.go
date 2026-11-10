package fake_test

import (
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	"github.com/mslxl/natcatty/application/plugin/runtimecontract"
)

// contractLimits mirrors the wazero adapter's contract test so deadline
// subtests finish quickly.
var contractLimits = appplugin.Limits{
	InitTimeout:   500 * time.Millisecond,
	CallTimeout:   time.Second,
	UnloadTimeout: 500 * time.Millisecond,
	WatchdogGrace: 100 * time.Millisecond,
}

// TestRuntimeContract keeps the fake on the same wire contract as the wazero
// adapter. The fake selects behaviour by fixture name, so the module bytes are
// irrelevant here.
func TestRuntimeContract(t *testing.T) {
	runtimecontract.Run(t,
		func(t *testing.T) appplugin.Runtime {
			return fake.New(fake.WithLimits(contractLimits))
		},
		func(t *testing.T, name string) appplugin.ModuleSpec {
			t.Helper()
			return appplugin.ModuleSpec{
				Name:           name,
				WASM:           []byte("fixture"),
				MemoryMaxBytes: 64 << 20,
			}
		})
}
