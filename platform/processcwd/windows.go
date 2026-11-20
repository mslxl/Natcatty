//go:build windows

package processcwd

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessBasicInformation is the NtQueryInformationProcess class that carries
// the PEB address.
const processBasicInformation = 0

// pebProcessParameters is PEB.ProcessParameters on the target's bitness.
func pebProcessParameters(target32 bool) uintptr {
	if target32 {
		return 0x10
	}
	return 0x20
}

// parametersCurrentDirectory is RTL_USER_PROCESS_PARAMETERS.CurrentDirectory,
// a CURDIR whose first field is the DOS path UNICODE_STRING.
func parametersCurrentDirectory(target32 bool) uintptr {
	if target32 {
		return 0x24
	}
	return 0x38
}

// processInformation mirrors PROCESS_BASIC_INFORMATION. uintptr fields make
// its layout match the caller's pointer size.
type processInformation struct {
	exitStatus                   uint32
	pebBaseAddress               uintptr
	affinityMask                 uintptr
	basePriority                 int32
	uniqueProcessID              uintptr
	inheritedFromUniqueProcessID uintptr
}

// unicodeString mirrors UNICODE_STRING.
type unicodeString struct {
	length        uint16
	maximumLength uint16
	buffer        uintptr
}

// Of returns the working directory of pid from the process environment block.
// Windows has no procfs and lsof; the shell's own process parameters hold the
// current directory, including after a builtin cd.
func Of(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrInvalid
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)

	var info processInformation
	if err := windows.NtQueryInformationProcess(process, processBasicInformation,
		unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return "", err
	}
	if info.pebBaseAddress == 0 {
		return "", fmt.Errorf("processcwd: process %d has no PEB", pid)
	}

	// A 32-bit process on 64-bit Windows has a 32-bit PEB layout. This
	// program's own pointer size decides the layout when it is itself 32-bit.
	target32 := unsafe.Sizeof(uintptr(0)) == 4
	if !target32 {
		var wow64 bool
		if err := windows.IsWow64Process(process, &wow64); err == nil {
			target32 = wow64
		}
	}

	parameters, err := readPointer(process, info.pebBaseAddress+pebProcessParameters(target32))
	if err != nil {
		return "", err
	}
	if parameters == 0 {
		return "", fmt.Errorf("processcwd: process %d has no parameters", pid)
	}
	var path unicodeString
	if err := readMemory(process, parameters+parametersCurrentDirectory(target32),
		unsafe.Pointer(&path), unsafe.Sizeof(path)); err != nil {
		return "", err
	}
	if path.length == 0 || path.length > 32*1024 || path.buffer == 0 {
		return "", fmt.Errorf("processcwd: process %d has no current directory", pid)
	}
	buffer := make([]uint16, path.length/2)
	if err := readMemory(process, path.buffer, unsafe.Pointer(&buffer[0]), uintptr(path.length)); err != nil {
		return "", err
	}
	cwd := syscall.UTF16ToString(buffer)
	cwd = filepath.Clean(cwd)
	if !valid(cwd) {
		return "", fmt.Errorf("processcwd: %q is not an absolute directory", cwd)
	}
	return cwd, nil
}

// readPointer reads one pointer-sized value from the target process.
func readPointer(process windows.Handle, address uintptr) (uintptr, error) {
	var value uintptr
	if err := readMemory(process, address, unsafe.Pointer(&value), unsafe.Sizeof(value)); err != nil {
		return 0, err
	}
	return value, nil
}

// readMemory copies size bytes at address from the target process.
func readMemory(process windows.Handle, address uintptr, buffer unsafe.Pointer, size uintptr) error {
	var read uintptr
	if err := windows.ReadProcessMemory(process, address, (*byte)(buffer), size, &read); err != nil {
		return err
	}
	if read != size {
		return fmt.Errorf("processcwd: read %d of %d bytes at %#x", read, size, address)
	}
	return nil
}
