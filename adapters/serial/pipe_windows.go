//go:build windows

package serial

import (
	"errors"
	"io"
	"sync"

	"golang.org/x/sys/windows"
)

// errPipeWait reports an unexpected WaitForMultipleObjects result. It is a
// driver failure, not a clean pipe end.
var errPipeWait = errors.New("serial: named pipe wait failed")

// errPipeDisconnected reports a pipe server that went away without this
// Session closing the pipe. Windows named pipes have no clean end-of-stream
// marker: a server closing its handle surfaces as ERROR_BROKEN_PIPE,
// ERROR_PIPE_NOT_CONNECTED, or ERROR_NO_DATA. Those mean the console process
// crashed or exited, so they are device failures (the Session ends with exit
// code 1), matching a COM port that disappears. Only Close marks the user's own
// hangup, and that path returns io.EOF with the transport's closed flag set.
var errPipeDisconnected = errors.New("serial: named pipe disconnected")

// openNamedPipe opens a local Windows named pipe as a byte stream. A pipe has
// no DCB or COMMTIMEOUTS; the overlapped wait below supplies the same bounded
// read cadence and close behavior as a COM port. Open failures stay raw so the
// Factory can type them for every backend.
func openNamedPipe(path string) (io.ReadWriteCloser, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &pipePort{handle: handle, stop: stop}, nil
}

// pipePort is an overlapped byte stream. Every operation waits on its own
// completion event or the port's stop event, so Close never needs the pipe
// server to send data first.
type pipePort struct {
	handle windows.Handle
	stop   windows.Handle

	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
	once   sync.Once
	err    error
	// onPending, when set by a test, fires once an overlapped operation is
	// pending, so cancellation can be tested without a timing assumption.
	onPending func()
}

func (p *pipePort) beginIO() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.EOF
	}
	p.active.Add(1)
	return nil
}

func (p *pipePort) endIO() { p.active.Done() }

func (p *pipePort) Read(buf []byte) (int, error) {
	if err := p.beginIO(); err != nil {
		return 0, err
	}
	defer p.endIO()
	n, err := p.perform(func(overlapped *windows.Overlapped, done *uint32) error {
		return windows.ReadFile(p.handle, buf, done, overlapped)
	})
	return p.classify(n, err)
}

func (p *pipePort) Write(buf []byte) (int, error) {
	if err := p.beginIO(); err != nil {
		return 0, err
	}
	defer p.endIO()
	n, err := p.perform(func(overlapped *windows.Overlapped, done *uint32) error {
		return windows.WriteFile(p.handle, buf, done, overlapped)
	})
	return p.classify(n, err)
}

// perform starts one overlapped operation and waits for it to finish or for
// Close to signal the stop event. A cancelled operation is drained before
// returning so the caller's buffer is no longer in use.
func (p *pipePort) perform(start func(*windows.Overlapped, *uint32) error) (int, error) {
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlapped := &windows.Overlapped{HEvent: event}
	var done uint32
	err = start(overlapped, &done)
	if err == nil {
		return int(done), nil
	}
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return int(done), err
	}
	if p.onPending != nil {
		p.onPending()
	}
	for {
		status, err := windows.WaitForMultipleObjects(
			[]windows.Handle{event, p.stop}, false, readPollMilliseconds)
		if err != nil {
			p.cancel(overlapped)
			return int(done), err
		}
		switch status {
		case windows.WAIT_OBJECT_0:
			err := windows.GetOverlappedResult(p.handle, overlapped, &done, false)
			return int(done), err
		case windows.WAIT_OBJECT_0 + 1:
			p.cancel(overlapped)
			return int(done), io.EOF
		case uint32(windows.WAIT_TIMEOUT):
			if p.isClosed() {
				p.cancel(overlapped)
				return int(done), io.EOF
			}
		default:
			p.cancel(overlapped)
			return int(done), errPipeWait
		}
	}
}

// cancel ends a pending operation and waits for the driver to release it.
func (p *pipePort) cancel(overlapped *windows.Overlapped) {
	_ = windows.CancelIoEx(p.handle, overlapped)
	var done uint32
	_ = windows.GetOverlappedResult(p.handle, overlapped, &done, true)
}

// classify maps a close this Session initiated to a clean EOF. A server
// disconnect is a device failure: a pipe has no orderly EOF, so the broken-pipe
// family means the console process is gone.
func (p *pipePort) classify(n int, err error) (int, error) {
	if err == nil {
		return n, nil
	}
	if p.isClosed() {
		return n, io.EOF
	}
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
		errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) ||
		errors.Is(err, windows.ERROR_NO_DATA) ||
		errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
		return n, errPipeDisconnected
	}
	return n, err
}

func (p *pipePort) Close() error {
	var closeErr error
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		_ = windows.SetEvent(p.stop)
		_ = windows.CancelIoEx(p.handle, nil)
		p.active.Wait()
		closeErr = windows.CloseHandle(p.handle)
		_ = windows.CloseHandle(p.stop)
		p.mu.Lock()
		p.err = closeErr
		p.mu.Unlock()
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *pipePort) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}
