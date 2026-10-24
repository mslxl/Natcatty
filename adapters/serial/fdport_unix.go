//go:build linux || darwin

package serial

import (
	"errors"
	"io"
	"sync"

	"golang.org/x/sys/unix"
)

// readPollMilliseconds bounds one poll wait so a close request is noticed even
// when the device sends nothing. It is a readiness cadence, not a delay that
// any caller waits on.
const readPollMilliseconds = 100

// errPortHangup is a device failure: the descriptor reported an error or hung
// up. It is deliberately not io.EOF, so the Session classifies the end as an
// error rather than a user close.
var errPortHangup = errors.New("serial: port hung up")

// fdPort is a non-blocking POSIX device descriptor. Read and Write hold a read
// lock across one poll and syscall, and Close takes the write lock, so a
// descriptor is never closed under a syscall or reused by an unrelated open
// while one is in flight.
type fdPort struct {
	fd int

	mu       sync.RWMutex
	closed   bool
	closeErr error
}

func (p *fdPort) Read(buf []byte) (int, error) {
	for {
		p.mu.RLock()
		if p.closed {
			p.mu.RUnlock()
			return 0, io.EOF
		}
		events, err := p.wait(unix.POLLIN)
		if err != nil {
			p.mu.RUnlock()
			return 0, err
		}
		if events&unix.POLLIN == 0 {
			hungUp := events&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0
			p.mu.RUnlock()
			if hungUp {
				return 0, errPortHangup
			}
			continue
		}
		n, err := unix.Read(p.fd, buf)
		p.mu.RUnlock()
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			// A serial device has no zero-length read; treat it as a hangup.
			return 0, errPortHangup
		}
		return n, nil
	}
}

func (p *fdPort) Write(buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		p.mu.RLock()
		if p.closed {
			p.mu.RUnlock()
			return total, io.EOF
		}
		events, err := p.wait(unix.POLLOUT)
		if err != nil {
			p.mu.RUnlock()
			return total, err
		}
		if events&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			p.mu.RUnlock()
			return total, errPortHangup
		}
		if events&unix.POLLOUT == 0 {
			p.mu.RUnlock()
			continue
		}
		n, err := unix.Write(p.fd, buf[total:])
		p.mu.RUnlock()
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (p *fdPort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	p.closeErr = unix.Close(p.fd)
	return p.closeErr
}

// wait blocks until the descriptor is ready for events or the close cadence
// expires, whichever comes first.
func (p *fdPort) wait(events int16) (int16, error) {
	for {
		poll := []unix.PollFd{{Fd: int32(p.fd), Events: events}}
		n, err := unix.Poll(poll, readPollMilliseconds)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, nil
		}
		return poll[0].Revents, nil
	}
}
