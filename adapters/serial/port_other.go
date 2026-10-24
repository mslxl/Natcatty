//go:build !linux && !darwin && !windows

package serial

import (
	"context"
	"io"
	"runtime"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

// platformOpenPort reports a typed unsupported error on a platform without a
// serial backend. It never panics, so the Session reports a connection error
// the user can understand.
func platformOpenPort(context.Context, domainserial.Config) (io.ReadWriteCloser, error) {
	return nil, &domainserial.UnsupportedError{Platform: runtime.GOOS, Setting: "serial ports"}
}

func platformListPorts(context.Context) ([]domainserial.PortInfo, error) {
	return nil, &domainserial.UnsupportedError{Platform: runtime.GOOS, Setting: "port enumeration"}
}
