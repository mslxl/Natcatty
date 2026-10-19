package connection

import (
	"io"
	"strings"
	"sync"
	"time"
)

// DefaultLivenessProbe is how long a shell opened on a reused parked
// transport must stay alive before the pool accepts it. The original uses
// 350 ms; it is clamped to 5 s when configured.
const DefaultLivenessProbe = 350 * time.Millisecond

const maxLivenessProbe = 5 * time.Second

// RemoteAllowsIdleParkedShellReuse reports whether a server can host a new
// interactive shell on a parked transport. 齐治 TERM-SSHD accepts the channel
// and then exits immediately, so it is never parked.
func RemoteAllowsIdleParkedShellReuse(remoteVersion string) bool {
	return !strings.Contains(normalizeRemoteVersion(remoteVersion), "TERM-SSHD")
}

// RemoteNeedsReusedShellLivenessCheck reports whether a reused shell must
// survive the settle window. OpenSSH and Dropbear multiplex cleanly; every
// other banner, including an unknown one, is checked.
func RemoteNeedsReusedShellLivenessCheck(remoteVersion string) bool {
	if !RemoteAllowsIdleParkedShellReuse(remoteVersion) {
		return true
	}
	software := normalizeRemoteVersion(remoteVersion)
	if software == "" {
		return true
	}
	lower := strings.ToLower(software)
	return !strings.HasPrefix(lower, "openssh") && !strings.HasPrefix(lower, "dropbear")
}

func normalizeRemoteVersion(remoteVersion string) string {
	version := strings.TrimSpace(remoteVersion)
	for _, prefix := range []string{"SSH-2.0-", "SSH-1.99-"} {
		if strings.HasPrefix(version, prefix) {
			return strings.TrimPrefix(version, prefix)
		}
	}
	return version
}

func resolveLivenessProbe(duration time.Duration) time.Duration {
	if duration < 0 {
		return DefaultLivenessProbe
	}
	if duration > maxLivenessProbe {
		return maxLivenessProbe
	}
	return duration
}

// probeReusedTerminal watches a just-opened reused shell for settle. It
// returns a Terminal that replays buffered output and owns the only reader,
// and reports false when the shell ended before the window elapsed.
func probeReusedTerminal(terminal Terminal, settle time.Duration) (Terminal, bool) {
	settle = resolveLivenessProbe(settle)
	if settle == 0 {
		return terminal, true
	}

	chunks := make(chan probeChunk, 64)
	firstError := make(chan struct{}, 1)
	stop := make(chan struct{})
	var stopOnce sync.Once
	halt := func() { stopOnce.Do(func() { close(stop) }) }

	go func() {
		defer close(chunks)
		buffer := make([]byte, 32<<10)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				data := append([]byte(nil), buffer[:n]...)
				select {
				case chunks <- probeChunk{data: data}:
				case <-stop:
					return
				}
			}
			if err != nil {
				select {
				case firstError <- struct{}{}:
				default:
				}
				select {
				case chunks <- probeChunk{err: err}:
				case <-stop:
				}
				return
			}
		}
	}()

	timer := time.NewTimer(settle)
	defer timer.Stop()
	select {
	case <-firstError:
		halt()
		_ = terminal.Close()
		return nil, false
	case <-timer.C:
		return &probedTerminal{Terminal: terminal, chunks: chunks, stop: stop, halt: halt}, true
	}
}

type probeChunk struct {
	data []byte
	err  error
}

// probedTerminal relays output produced before and after the liveness
// window. Exactly one goroutine reads the underlying Terminal.
type probedTerminal struct {
	Terminal
	chunks chan probeChunk
	stop   chan struct{}
	halt   func()
	rest   []byte
}

func (p *probedTerminal) Read(out []byte) (int, error) {
	for len(p.rest) == 0 {
		chunk, ok := <-p.chunks
		if !ok {
			return 0, io.EOF
		}
		if chunk.err != nil {
			return 0, chunk.err
		}
		p.rest = chunk.data
	}
	n := copy(out, p.rest)
	p.rest = p.rest[n:]
	return n, nil
}

func (p *probedTerminal) Close() error {
	p.halt()
	return p.Terminal.Close()
}

// End forwards the underlying terminal's classification. Embedding only
// promotes the Terminal interface's methods, so End must be explicit.
func (p *probedTerminal) End() TerminalEnd {
	if ending, ok := p.Terminal.(EndingTerminal); ok {
		return ending.End()
	}
	return TerminalEnd{Reason: TerminalEndError}
}
