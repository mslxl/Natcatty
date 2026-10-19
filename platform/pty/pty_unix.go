//go:build darwin || linux

package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

type sys struct {
	master *os.File
	cmd    *exec.Cmd
}

func (p *PTY) start(options Options) error {
	master, slaveName, err := openMaster()
	if err != nil {
		return err
	}
	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return fmt.Errorf("pty: opening %s: %w", slaveName, err)
	}
	defer slave.Close() // the child has its own copy

	if err := setSize(master, options.Cols, options.Rows); err != nil {
		master.Close()
		return err
	}
	cmd := &exec.Cmd{
		Path:   options.Path,
		Args:   options.Args,
		Dir:    options.Dir,
		Env:    options.Env,
		Stdin:  slave,
		Stdout: slave,
		Stderr: slave,
	}
	// A session of its own whose controlling terminal is the slave, so the
	// terminal's signals reach the foreground process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		return err
	}
	p.master, p.cmd = master, cmd
	return nil
}

func (p *PTY) wait() (int, error) {
	err := p.cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return 0, err
}

// Read reads the output of the process. It returns io.EOF once no process has
// the terminal open any more, or after Close.
func (p *PTY) Read(b []byte) (int, error) {
	n, err := p.master.Read(b)
	if err != nil && n == 0 {
		// Linux reports EIO when the last process with the terminal open
		// closes it.
		if errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) {
			err = io.EOF
		}
	}
	return n, err
}

// Write types b into the terminal.
func (p *PTY) Write(b []byte) (int, error) {
	n, err := p.master.Write(b)
	if errors.Is(err, os.ErrClosed) {
		err = ErrClosed
	}
	return n, err
}

// Resize sets the terminal size, which sends the foreground process SIGWINCH.
func (p *PTY) Resize(cols, rows int) error {
	return setSize(p.master, cols, rows)
}

// Close closes the terminal, which hangs up the processes that have it open.
// It does not wait for them to exit.
func (p *PTY) Close() error { return p.master.Close() }

// Kill kills the process and its process group, which it leads.
func (p *PTY) Kill() error {
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func setSize(f *os.File, cols, rows int) error {
	ws := struct{ row, col, xpixel, ypixel uint16 }{clamp(rows), clamp(cols), 0, 0}
	return ioctl(f, syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}

func clamp(v int) uint16 { return uint16(max(0, min(v, 0xFFFF))) }

// ioctl calls ioctl on f with a pointer, which it converts in the system call
// so what it points to stays put meanwhile.
func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}
