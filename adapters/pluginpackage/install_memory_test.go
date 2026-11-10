package pluginpackage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/pluginpackage"
	"github.com/mslxl/natcatty/adapters/wasm"
)

// TestInstallValidationEnforcesTheEffectiveMemoryCap drives the real wasm
// validator through the package install path. It proves the installer uses the
// manifest's effective maximum (including the 64 MiB default) and rejects
// declared maxima above it, with or without an explicit manifest maximum,
// unbounded memories, and inverted bounds.
func TestInstallValidationEnforcesTheEffectiveMemoryCap(t *testing.T) {
	tests := []struct {
		name      string
		memory    testMemoryLimits
		manifest  int // 0 omits wasm.memory_max_mib
		wantError bool
	}{
		{
			name:      "declared maximum above the cap",
			memory:    testMemoryLimits{min: 1, max: 17},
			manifest:  1,
			wantError: true,
		},
		{
			name:      "unbounded memory",
			memory:    testMemoryLimits{min: 1, noMax: true},
			manifest:  64,
			wantError: true,
		},
		{
			name:      "minimum above maximum",
			memory:    testMemoryLimits{min: 2, max: 1},
			manifest:  1,
			wantError: true,
		},
		{
			name:     "declared maximum within the cap",
			memory:   testMemoryLimits{min: 1, max: 16},
			manifest: 1,
		},
		{
			name: "declared maximum above the default cap with an omitted manifest maximum",
			// 1025 pages is one page above the 64 MiB default cap.
			memory:    testMemoryLimits{min: 1, max: 1025},
			manifest:  0,
			wantError: true,
		},
		{
			name:     "omitted maximum uses the 64 MiB default",
			memory:   testMemoryLimits{min: 1, max: 16},
			manifest: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := installTestModule(test.memory)
			sum := sha256.Sum256(module)
			files := map[string][]byte{
				"plugin.toml":  []byte(installManifest(hex.EncodeToString(sum[:]), test.manifest)),
				"plugin.wasm":  module,
				"i18n/en.toml": []byte("app = \"Acme Memory\"\n"),
			}
			_, err := pluginpackage.New(wasm.Validator{}).
				Validate(context.Background(), writeArchive(t, files))
			if test.wantError {
				if err == nil {
					t.Fatal("invalid memory declaration was accepted")
				}
				if !strings.Contains(err.Error(), "plugin module") &&
					!strings.Contains(err.Error(), "memory") {
					t.Fatalf("Validate = %v, want a module validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func installManifest(hash string, memoryMaxMiB int) string {
	memory := ""
	if memoryMaxMiB > 0 {
		memory = fmt.Sprintf("memory_max_mib = %d\n", memoryMaxMiB)
	}
	return fmt.Sprintf(`manifest_version = 1
id = "acme.memory"
name = "Acme Memory"
version = "1.0.0"
plugin_api = "^0.1"
abi = 1
default_locale = "en"

[wasm]
path = "plugin.wasm"
sha256 = "%s"
%s
[permissions]
required = ["commands"]
optional = []
`, hash, memory)
}

// --- minimal module encoder (independent of adapters/wasm tests) -----------

type testMemoryLimits struct {
	min   uint32
	max   uint32
	noMax bool
}

func installTestModule(memory testMemoryLimits) []byte {
	var out []byte
	out = append(out, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	out = append(out, testSection(1, []byte{0x02, 0x60, 0x00, 0x01, 0x7f, 0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f})...)
	out = append(out, testSection(3, []byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00})...)

	min := memory.min
	if min == 0 {
		min = 1
	}
	section := []byte{0x01}
	if memory.noMax {
		section = append(section, 0x00)
		section = append(section, testLEB(min)...)
	} else {
		max := memory.max
		if max == 0 {
			max = 1
		}
		section = append(section, 0x01)
		section = append(section, testLEB(min)...)
		section = append(section, testLEB(max)...)
	}
	out = append(out, testSection(5, section)...)

	type export struct {
		name  string
		kind  byte
		index byte
	}
	exports := []export{
		{"memory", 0x02, 0x00},
		{"natcatty_abi_version", 0x00, 0x00},
		{"natcatty_init", 0x00, 0x01},
		{"natcatty_dispatch", 0x00, 0x02},
		{"natcatty_poll", 0x00, 0x03},
		{"natcatty_shutdown", 0x00, 0x04},
	}
	exportPayload := []byte{byte(len(exports))}
	for _, item := range exports {
		exportPayload = append(exportPayload, byte(len(item.name)))
		exportPayload = append(exportPayload, item.name...)
		exportPayload = append(exportPayload, item.kind, item.index)
	}
	out = append(out, testSection(7, exportPayload)...)

	codePayload := []byte{0x05}
	for i := 0; i < 5; i++ {
		body := []byte{0x00, 0x41, 0x01, 0x0b}
		codePayload = append(codePayload, byte(len(body)))
		codePayload = append(codePayload, body...)
	}
	out = append(out, testSection(10, codePayload)...)
	return out
}

func testSection(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, testLEB(uint32(len(payload)))...)
	return append(out, payload...)
}

func testLEB(value uint32) []byte {
	var out []byte
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}
