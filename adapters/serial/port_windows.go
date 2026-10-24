//go:build windows

package serial

import (
	"context"
	"io"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

const maxDWORD = 0xFFFFFFFF

// DCB flag bits. x/sys/windows exposes the parity, stop-bit, and RTS-control
// values but not the individual Flags bits, so the backend names them here
// from winbase.h.
const (
	dcbFlagBinary         = 0x0001
	dcbFlagParity         = 0x0002
	dcbFlagOutxCtsFlow    = 0x0004
	dcbFlagOutX           = 0x0100
	dcbFlagInX            = 0x0200
	dcbFlagTxContinueXoff = 0x0080
	dcbFlagDtrControl     = 0x0030
	dcbFlagRtsControl     = 0x3000
	dcbDtrEnable          = 0x0010
	dcbRtsEnable          = 0x1000
	dcbRtsHandshake       = 0x2000
)

// readPollMilliseconds bounds one ReadFile wait so a close request is noticed
// even when the device sends nothing. It is a driver timeout, not a test
// synchronization delay.
const readPollMilliseconds = 50

// platformOpenPort opens a Windows communications resource and programs its
// DCB.
func platformOpenPort(ctx context.Context, cfg domainserial.Config) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := windowsDevicePath(cfg.Path)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	if err := configurePort(handle, cfg); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &windowsPort{handle: handle}, nil
}

// windowsDevicePath turns a user-facing COM name into the raw-device path
// CreateFile requires. An already raw path is left alone.
func windowsDevicePath(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, `\\.\`) {
		return path
	}
	return `\\.\` + path
}

func configurePort(handle windows.Handle, cfg domainserial.Config) error {
	dcb := windows.DCB{DCBlength: uint32(unsafe.Sizeof(windows.DCB{}))}
	if err := windows.GetCommState(handle, &dcb); err != nil {
		return err
	}
	applyDCB(&dcb, cfg)
	if err := windows.SetCommState(handle, &dcb); err != nil {
		return err
	}
	timeouts := windows.CommTimeouts{
		ReadIntervalTimeout:         maxDWORD,
		ReadTotalTimeoutMultiplier:  0,
		ReadTotalTimeoutConstant:    readPollMilliseconds,
		WriteTotalTimeoutMultiplier: 0,
		WriteTotalTimeoutConstant:   0,
	}
	if err := windows.SetCommTimeouts(handle, &timeouts); err != nil {
		return err
	}
	return windows.PurgeComm(handle, windows.PURGE_RXABORT|windows.PURGE_TXABORT|
		windows.PURGE_RXCLEAR|windows.PURGE_TXCLEAR)
}

// applyDCB writes cfg into a DCB. It keeps the driver's unrelated settings and
// replaces only the framing and flow-control bits this transport owns.
func applyDCB(dcb *windows.DCB, cfg domainserial.Config) {
	dcb.BaudRate = uint32(cfg.BaudRate)
	dcb.ByteSize = uint8(cfg.DataBits)
	dcb.StopBits = stopBitsDCB(cfg.StopBits)
	dcb.Parity = parityDCB(cfg.Parity)

	flags := dcb.Flags
	flags &^= dcbFlagParity | dcbFlagOutxCtsFlow | dcbFlagOutX | dcbFlagInX |
		dcbFlagTxContinueXoff | dcbFlagDtrControl | dcbFlagRtsControl
	flags |= dcbFlagBinary | dcbDtrEnable | dcbRtsEnable
	if cfg.Parity != domainserial.ParityNone {
		flags |= dcbFlagParity
	}
	switch cfg.FlowControl {
	case domainserial.FlowRTSCTS:
		// fRtsControl was cleared above, so OR-ing HANDSHAKE replaces the
		// default ENABLE value.
		flags |= dcbFlagOutxCtsFlow | dcbRtsHandshake
	case domainserial.FlowXONXOFF:
		flags |= dcbFlagOutX | dcbFlagInX | dcbFlagTxContinueXoff
	}
	dcb.Flags = flags
}

func stopBitsDCB(bits domainserial.StopBits) uint8 {
	switch bits {
	case domainserial.StopBitsOneAndHalf:
		return windows.ONE5STOPBITS
	case domainserial.StopBitsTwo:
		return windows.TWOSTOPBITS
	default:
		return windows.ONESTOPBIT
	}
}

func parityDCB(parity domainserial.Parity) uint8 {
	switch parity {
	case domainserial.ParityOdd:
		return windows.ODDPARITY
	case domainserial.ParityEven:
		return windows.EVENPARITY
	case domainserial.ParityMark:
		return windows.MARKPARITY
	case domainserial.ParitySpace:
		return windows.SPACEPARITY
	default:
		return windows.NOPARITY
	}
}

// windowsPort is a synchronous communications handle. Close marks the port
// closed, aborts pending reads, and closes the handle only after every
// in-flight Read or Write returned, so a handle is never closed under a
// syscall.
type windowsPort struct {
	handle windows.Handle

	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
	once   sync.Once
}

func (p *windowsPort) beginIO() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.EOF
	}
	p.active.Add(1)
	return nil
}

func (p *windowsPort) endIO() { p.active.Done() }

func (p *windowsPort) Read(buf []byte) (int, error) {
	if err := p.beginIO(); err != nil {
		return 0, err
	}
	defer p.endIO()
	for {
		if p.isClosed() {
			return 0, io.EOF
		}
		var done uint32
		err := windows.ReadFile(p.handle, buf, &done, nil)
		if done > 0 {
			// A synchronous ReadFile that reports both bytes and an error must
			// not hide the error: it is a device failure, not a clean end.
			if err != nil {
				return int(done), err
			}
			return int(done), nil
		}
		if err != nil {
			if p.isClosed() {
				return 0, io.EOF
			}
			return 0, err
		}
		// A read timeout with no bytes is a normal idle interval.
	}
}

func (p *windowsPort) Write(buf []byte) (int, error) {
	if err := p.beginIO(); err != nil {
		return 0, err
	}
	defer p.endIO()
	if p.isClosed() {
		return 0, io.EOF
	}
	var done uint32
	if err := windows.WriteFile(p.handle, buf, &done, nil); err != nil {
		return int(done), err
	}
	return int(done), nil
}

func (p *windowsPort) Close() error {
	var err error
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		// PURGE_RXABORT ends a pending ReadFile, and TXABORT ends a pending
		// WriteFile, so Close does not wait for the poll interval.
		_ = windows.PurgeComm(p.handle,
			windows.PURGE_RXABORT|windows.PURGE_TXABORT)
		p.active.Wait()
		err = windows.CloseHandle(p.handle)
	})
	return err
}

func (p *windowsPort) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}
