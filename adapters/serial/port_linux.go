//go:build linux

package serial

import (
	"context"
	"io"
	"strconv"

	"golang.org/x/sys/unix"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

// platformOpenPort opens a Linux tty in raw mode and programs its termios.
// The descriptor is non-blocking; fdPort supplies the poll loop.
func platformOpenPort(ctx context.Context, cfg domainserial.Config) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(cfg.Path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if err := configureTermios(termios, cfg); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &fdPort{fd: fd}, nil
}

// configureTermios applies raw mode, framing, and flow control. The bits
// match the original serialport binding: no canonical processing or echo, and
// hardware or software flow control only when requested.
func configureTermios(t *unix.Termios, cfg domainserial.Config) error {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB |
		unix.CRTSCTS | unix.CMSPAR | unix.CBAUD
	t.Cflag |= unix.CLOCAL | unix.CREAD

	switch cfg.DataBits {
	case 5:
		t.Cflag |= unix.CS5
	case 6:
		t.Cflag |= unix.CS6
	case 7:
		t.Cflag |= unix.CS7
	default:
		t.Cflag |= unix.CS8
	}

	switch cfg.Parity {
	case domainserial.ParityEven:
		t.Cflag |= unix.PARENB
	case domainserial.ParityOdd:
		t.Cflag |= unix.PARENB | unix.PARODD
	case domainserial.ParityMark:
		// Linux stick parity: CMSPAR with PARODD is mark, without it space.
		t.Cflag |= unix.PARENB | unix.PARODD | unix.CMSPAR
	case domainserial.ParitySpace:
		t.Cflag |= unix.PARENB | unix.CMSPAR
	}

	if cfg.StopBits == domainserial.StopBitsTwo {
		t.Cflag |= unix.CSTOPB
	}

	switch cfg.FlowControl {
	case domainserial.FlowRTSCTS:
		t.Cflag |= unix.CRTSCTS
	case domainserial.FlowXONXOFF:
		t.Iflag |= unix.IXON | unix.IXOFF | unix.IXANY
	}

	baud, err := baudConstant(cfg.BaudRate)
	if err != nil {
		return err
	}
	t.Cflag |= baud
	t.Ispeed = baud
	t.Ospeed = baud
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return nil
}

func baudConstant(rate int) (uint32, error) {
	switch rate {
	case 300:
		return unix.B300, nil
	case 1200:
		return unix.B1200, nil
	case 2400:
		return unix.B2400, nil
	case 4800:
		return unix.B4800, nil
	case 9600:
		return unix.B9600, nil
	case 19200:
		return unix.B19200, nil
	case 38400:
		return unix.B38400, nil
	case 57600:
		return unix.B57600, nil
	case 115200:
		return unix.B115200, nil
	case 230400:
		return unix.B230400, nil
	case 460800:
		return unix.B460800, nil
	case 921600:
		return unix.B921600, nil
	default:
		return 0, &domainserial.UnsupportedError{Platform: "linux", Setting: "baudRate",
			Value: strconv.Itoa(rate)}
	}
}
