//go:build darwin || linux

package wasm

import "golang.org/x/sys/unix"

// probeExecutableMemory mirrors the runtime check wazero performs before it
// selects the compiler: allocate anonymous pages, make them executable, then
// release them. A hardened process without the JIT entitlement fails here and
// the interpreter remains usable.
func probeExecutableMemory() error {
	pages, err := unix.Mmap(-1, 0, unix.Getpagesize(),
		unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Munmap(pages) }()
	return unix.Mprotect(pages, unix.PROT_READ|unix.PROT_EXEC)
}
