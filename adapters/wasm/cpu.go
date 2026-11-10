package wasm

import "golang.org/x/sys/cpu"

// compilerCPUFeatures mirrors wazero's CPU gate for the selected target. The
// amd64 compiler requires SSE4.1; the default arm64 feature set has no extra
// CPU requirement. Checking it keeps the reported engine mode truthful on
// hosts where wazero's automatic selection would choose the interpreter.
func compilerCPUFeatures(goarch string) bool {
	if goarch == "amd64" {
		return cpu.X86.HasSSE41
	}
	return true
}
