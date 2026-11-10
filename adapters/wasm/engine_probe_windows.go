//go:build windows

package wasm

import "golang.org/x/sys/windows"

// probeExecutableMemory mirrors the runtime check wazero performs before it
// selects the compiler: reserve and commit pages, flip them to
// execute-and-read, then release them. Group policy or exploit protection can
// deny this even on windows/amd64; wazero then falls back to the interpreter.
func probeExecutableMemory() error {
	const size = 4096
	address, err := windows.VirtualAlloc(0, size, windows.MEM_COMMIT, windows.PAGE_READWRITE)
	if err != nil {
		return err
	}
	var previous uint32
	if err := windows.VirtualProtect(address, size, windows.PAGE_EXECUTE_READ, &previous); err != nil {
		_ = windows.VirtualFree(address, 0, windows.MEM_RELEASE)
		return err
	}
	return windows.VirtualFree(address, 0, windows.MEM_RELEASE)
}
