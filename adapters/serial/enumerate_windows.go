//go:build windows

package serial

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

// serialCommKey maps device names to the COM ports the kernel assigned. It is
// the same source Windows device managers show; the values are the port names
// CreateFile accepts after the raw-device prefix.
const serialCommKey = `HARDWARE\DEVICEMAP\SERIALCOMM`

// platformListPorts enumerates the COM ports Windows assigned. The original
// serialport list used SetupAPI for richer USB metadata; the registry key is
// the stable, dependency-free source for the port paths the Session can open.
func platformListPorts(ctx context.Context) ([]domainserial.PortInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, serialCommKey, registry.QUERY_VALUE)
	if err != nil {
		// A machine with no serial driver may not publish the key. That is an
		// empty list, not a failure to enumerate. Any other error, such as
		// access denied, is a real enumeration failure the dialog can show.
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
			errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, nil
		}
		return nil, fmt.Errorf("serial: enumerate ports: %w", err)
	}
	defer key.Close()

	names, err := key.ReadValueNames(0)
	if err != nil {
		return nil, fmt.Errorf("serial: enumerate ports: %w", err)
	}
	seen := make(map[string]bool, len(names))
	ports := make([]domainserial.PortInfo, 0, len(names))
	for _, name := range names {
		value, _, err := key.GetStringValue(name)
		if err != nil || strings.TrimSpace(value) == "" {
			continue
		}
		path := strings.ToUpper(strings.TrimSpace(value))
		if seen[path] {
			continue
		}
		seen[path] = true
		ports = append(ports, domainserial.PortInfo{
			Path: path,
			Type: "hardware",
		})
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Path < ports[j].Path })
	return ports, nil
}
