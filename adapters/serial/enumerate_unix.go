//go:build linux || darwin

package serial

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

// platformListPorts lists hardware serial devices. It deliberately omits
// allocatable pseudo-terminals: the original listed hardware only, while a
// user can still type any /dev/ path for a PTY.
func platformListPorts(ctx context.Context) ([]domainserial.PortInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS == "darwin" {
		return listDarwinPorts(ctx)
	}
	return listLinuxPorts(ctx)
}

// listLinuxPorts walks /sys/class/tty. A directory with a backing device is a
// real tty; a directory without one is the kernel's virtual console family.
func listLinuxPorts(ctx context.Context) ([]domainserial.PortInfo, error) {
	entries, err := os.ReadDir("/sys/class/tty")
	if err != nil {
		return listUnixGlobs(), nil
	}
	ports := make([]domainserial.PortInfo, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if skipLinuxTTY(name) {
			continue
		}
		device := filepath.Join("/sys/class/tty", name, "device")
		if _, err := os.Stat(device); err != nil {
			continue
		}
		path := filepath.Join("/dev", name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		info := domainserial.PortInfo{Path: path, Type: "hardware"}
		info.Manufacturer = readSysfs(device, "..", "manufacturer")
		info.SerialNumber = readSysfs(device, "..", "serial")
		info.VendorID = readSysfs(device, "..", "idVendor")
		info.ProductID = readSysfs(device, "..", "idProduct")
		info.PNPID = readSysfs(device, "..", "product")
		ports = append(ports, info)
	}
	if len(ports) == 0 {
		return listUnixGlobs(), nil
	}
	sortPorts(ports)
	return ports, nil
}

func skipLinuxTTY(name string) bool {
	switch {
	case strings.HasPrefix(name, "console"):
		return true
	case strings.HasPrefix(name, "tty") && isAllDigits(strings.TrimPrefix(name, "tty")):
		// tty0, tty1, ... are virtual consoles; ttyS0 and ttyUSB0 are not.
		return true
	}
	return false
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// readSysfs joins a sysfs directory with parts and returns the trimmed file
// contents, or "" when the attribute does not exist or cannot be read.
func readSysfs(base string, parts ...string) string {
	path := filepath.Join(append([]string{base}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// listDarwinPorts lists the callout (cu.*) and dial-in (tty.*) device nodes.
// The glob patterns do not match ttys000 pseudo-terminals, which keeps the
// list to hardware as the original did.
func listDarwinPorts(ctx context.Context) ([]domainserial.PortInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ports := make([]domainserial.PortInfo, 0, 8)
	seen := map[string]bool{}
	for _, pattern := range []string{"/dev/cu.*", "/dev/tty.*"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if seen[match] {
				continue
			}
			seen[match] = true
			ports = append(ports, domainserial.PortInfo{Path: match, Type: "hardware"})
		}
	}
	sortPorts(ports)
	return ports, nil
}

// listUnixGlobs is the fallback when sysfs is unavailable, as in a chroot or a
// container without /sys. It matches the common USB and UART names.
func listUnixGlobs() []domainserial.PortInfo {
	patterns := []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/ttyAMA*", "/dev/ttyS*"}
	seen := map[string]bool{}
	ports := make([]domainserial.PortInfo, 0, 8)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if seen[match] {
				continue
			}
			seen[match] = true
			ports = append(ports, domainserial.PortInfo{Path: match, Type: "hardware"})
		}
	}
	sortPorts(ports)
	return ports
}

func sortPorts(ports []domainserial.PortInfo) {
	sort.Slice(ports, func(i, j int) bool { return ports[i].Path < ports[j].Path })
}
