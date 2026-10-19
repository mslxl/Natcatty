// Package pty runs a process in a pseudo-terminal in pure Go: /dev/ptmx on
// macOS and Linux, ConPTY on Windows. It is the OS primitive behind local
// shell Sessions; application code sees only the Session transport port.
//
// The syscall plumbing follows the same approach as MyGo's internal pty
// package (MIT), which cannot be imported from outside MyGo.
package pty

import (
	"errors"
	"sync"
)

// Options describe a process to start in a pseudo-terminal.
type Options struct {
	// Path is the program, and Args its arguments. Args[0] is the name the
	// program sees; for a login shell it may begin with a dash.
	Path string
	Args []string
	// Dir is the working directory, and Env the environment as "KEY=value".
	Dir string
	Env []string
	// Cols and Rows are the initial terminal size in cells.
	Cols, Rows int
}

// ErrClosed is returned by Write after Close.
var ErrClosed = errors.New("pty: closed")

// PTY is a process running in a pseudo-terminal: Read returns its output and
// Write types into it.
type PTY struct {
	sys
	waitOnce sync.Once
	code     int
	waitErr  error
	exited   chan struct{}
}

// Start starts a process in a pseudo-terminal.
func Start(options Options) (*PTY, error) {
	if options.Cols <= 0 || options.Rows <= 0 {
		options.Cols, options.Rows = 80, 24
	}
	if len(options.Args) == 0 {
		options.Args = []string{options.Path}
	}
	p := &PTY{exited: make(chan struct{})}
	if err := p.start(options); err != nil {
		return nil, err
	}
	go func() {
		p.code, p.waitErr = p.wait()
		close(p.exited)
	}()
	return p, nil
}

// Wait waits for the process to exit and returns its exit code.
func (p *PTY) Wait() (int, error) {
	<-p.exited
	return p.code, p.waitErr
}

// Exited is closed when the process exited.
func (p *PTY) Exited() <-chan struct{} { return p.exited }
