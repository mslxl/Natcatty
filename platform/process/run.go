package process

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Result is a finished command's output and exit status.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Run runs command through the platform shell to completion and collects its
// output, as an SSH exec does on a Host. A command that exits non-zero is a
// Result, not an error; ending ctx kills the command and everything it
// spawned and returns ctx's error.
func Run(ctx context.Context, command string) (Result, error) {
	if command == "" {
		return Result{}, errors.New("process: empty command")
	}
	cmd := shellCommand(command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		terminate(cmd)
		<-done
		return Result{}, ctx.Err()
	}
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		result.ExitCode = exit.ExitCode()
	case err != nil:
		return Result{}, err
	}
	return result, nil
}
