package pty

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	kernel32                              = syscall.NewLazyDLL("kernel32.dll")
	procCreatePseudoConsole               = kernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole               = kernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole                = kernel32.NewProc("ClosePseudoConsole")
	procInitializeProcThreadAttributeList = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = kernel32.NewProc("DeleteProcThreadAttributeList")
)

const (
	procThreadAttributePseudoConsole = 0x00020016
	extendedStartupInfoPresent       = 0x00080000
	createUnicodeEnvironment         = 0x00000400
)

type startupInfoEx struct {
	syscall.StartupInfo
	attributes uintptr
}

type sys struct {
	mu      sync.Mutex
	console uintptr // the HPCON, 0 once closed
	in      *os.File
	out     *os.File

	processMu sync.Mutex
	process   syscall.Handle // 0 once reaped
}

func (p *PTY) start(options Options) error {
	if err := procCreatePseudoConsole.Find(); err != nil {
		return errors.New("pty: ConPTY needs Windows 10 1809 or later")
	}

	// The console reads input from inR and writes output to outW; this side
	// writes to inW and reads outR.
	var inR, inW, outR, outW syscall.Handle
	if err := syscall.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return fmt.Errorf("pty: %w", err)
	}
	if err := syscall.CreatePipe(&outR, &outW, nil, 0); err != nil {
		syscall.CloseHandle(inR)
		syscall.CloseHandle(inW)
		return fmt.Errorf("pty: %w", err)
	}
	var console uintptr
	r, _, _ := procCreatePseudoConsole.Call(
		coord(options.Cols, options.Rows),
		uintptr(inR),
		uintptr(outW),
		0,
		uintptr(unsafe.Pointer(&console)),
	)
	// The console has its own copies of its ends.
	syscall.CloseHandle(inR)
	syscall.CloseHandle(outW)
	if r != 0 {
		syscall.CloseHandle(inW)
		syscall.CloseHandle(outR)
		return fmt.Errorf("pty: CreatePseudoConsole: %w", syscall.Errno(r&0xFFFF))
	}
	p.console = console
	p.in = os.NewFile(uintptr(inW), "conpty-in")
	p.out = os.NewFile(uintptr(outR), "conpty-out")
	if err := p.spawn(options); err != nil {
		p.Close()
		p.out.Close()
		return err
	}
	return nil
}

func (p *PTY) spawn(options Options) error {
	var size uintptr
	procInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	list := make([]byte, size)
	if r, _, err := procInitializeProcThreadAttributeList.Call(
		uintptr(unsafe.Pointer(&list[0])), 1, 0, uintptr(unsafe.Pointer(&size)),
	); r == 0 {
		return fmt.Errorf("pty: InitializeProcThreadAttributeList: %w", err)
	}
	defer procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&list[0])))
	if r, _, err := procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&list[0])),
		0,
		procThreadAttributePseudoConsole,
		p.console,
		unsafe.Sizeof(p.console),
		0,
		0,
	); r == 0 {
		return fmt.Errorf("pty: UpdateProcThreadAttribute: %w", err)
	}

	startup := startupInfoEx{attributes: uintptr(unsafe.Pointer(&list[0]))}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	// The console's own standard handles: without invalid ones, a child of a
	// process whose output is redirected would write there instead.
	startup.Flags = syscall.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr =
		syscall.InvalidHandle, syscall.InvalidHandle, syscall.InvalidHandle

	commandLine, err := syscall.UTF16PtrFromString(commandLine(options.Path, options.Args))
	if err != nil {
		return err
	}
	var dir *uint16
	if options.Dir != "" {
		if dir, err = syscall.UTF16PtrFromString(options.Dir); err != nil {
			return err
		}
	}
	var environment *uint16
	if options.Env != nil {
		block := environmentBlock(options.Env)
		environment = &block[0]
	}

	var info syscall.ProcessInformation
	err = syscall.CreateProcess(
		nil,
		commandLine,
		nil,
		nil,
		false,
		extendedStartupInfoPresent|createUnicodeEnvironment,
		environment,
		dir,
		&startup.StartupInfo,
		&info,
	)
	if err != nil {
		return fmt.Errorf("pty: starting %s: %w", options.Path, err)
	}
	syscall.CloseHandle(info.Thread)
	p.process = info.Process
	return nil
}

func (p *PTY) wait() (int, error) {
	p.processMu.Lock()
	process := p.process
	p.processMu.Unlock()
	if process == 0 {
		return 0, nil
	}
	if _, err := syscall.WaitForSingleObject(process, syscall.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	err := syscall.GetExitCodeProcess(process, &code)
	p.processMu.Lock()
	p.process = 0
	p.processMu.Unlock()
	syscall.CloseHandle(process)
	return int(code), err
}

// Read reads the output of the process; it returns io.EOF after Close, once
// the console wrote what it had.
func (p *PTY) Read(b []byte) (int, error) { return p.out.Read(b) }

// Write types b into the terminal.
func (p *PTY) Write(b []byte) (int, error) {
	n, err := p.in.Write(b)
	if errors.Is(err, os.ErrClosed) {
		err = ErrClosed
	}
	return n, err
}

// Resize sets the terminal size.
func (p *PTY) Resize(cols, rows int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.console == 0 {
		return ErrClosed
	}
	if r, _, _ := procResizePseudoConsole.Call(p.console, coord(cols, rows)); r != 0 {
		return fmt.Errorf("pty: ResizePseudoConsole: %w", syscall.Errno(r&0xFFFF))
	}
	return nil
}

// Close closes the console, which ends the processes attached to it. Read
// returns io.EOF once it has written what it had.
func (p *PTY) Close() error {
	p.mu.Lock()
	console := p.console
	p.console = 0
	p.mu.Unlock()
	if console == 0 {
		return nil
	}
	p.in.Close()
	// ClosePseudoConsole waits for the output to be read before Windows 11
	// 24H2, so let Read keep reading until the end of the output.
	go func() {
		procClosePseudoConsole.Call(console)
		p.out.Close()
	}()
	return nil
}

// Kill kills the process, unless it already exited.
func (p *PTY) Kill() error {
	p.processMu.Lock()
	process := p.process
	p.processMu.Unlock()
	if process == 0 {
		return nil
	}
	return syscall.TerminateProcess(process, 1)
}

// coord packs a COORD, which ConPTY takes by value.
func coord(cols, rows int) uintptr {
	return uintptr(uint16(max(1, min(cols, 0x7FFF)))) |
		uintptr(uint16(max(1, min(rows, 0x7FFF))))<<16
}

// commandLine joins a program and its arguments as the C runtime splits them;
// Args[0] is the program's name.
func commandLine(path string, args []string) string {
	parts := []string{syscall.EscapeArg(path)}
	if len(args) > 1 {
		for _, arg := range args[1:] {
			parts = append(parts, syscall.EscapeArg(arg))
		}
	}
	return strings.Join(parts, " ")
}

// environmentBlock encodes env as CreateProcess takes it: NUL-terminated
// UTF-16 strings, then another NUL.
func environmentBlock(env []string) []uint16 {
	var block []uint16
	for _, kv := range env {
		block = append(block, utf16.Encode([]rune(kv))...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0)
}
