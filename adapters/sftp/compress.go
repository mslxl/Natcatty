package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// extractionDiagnosticLimit bounds the remote stderr kept for an error
// message. tar writes only a few lines; anything past this is a runaway shell.
const extractionDiagnosticLimit = 4096

// errExtractionUnsupported reports an exec transport that cannot signal
// end-of-input while keeping stdout open. Closing the whole channel instead
// would kill tar before it could report a status, so the upload falls back to
// the per-file path rather than trusting an unknown outcome.
var errExtractionUnsupported = errors.New(
	"sftp: the exec transport cannot half-close an archive extraction")

// extractTar streams a gzip tar into dir with the remote tar command. The
// caller has already validated every archive name; dir is the extraction
// staging directory, so a failure never touches a live destination.
func extractTar(ctx context.Context, exec appconnection.Executor, dir string, archive io.Reader) error {
	if exec == nil {
		return errExtractionUnsupported
	}
	quoted, err := quotePath(dir)
	if err != nil {
		return err
	}
	stream, err := exec.OpenExec(ctx, "tar -xzf - -C "+quoted)
	if err != nil {
		return err
	}
	defer stream.Close()
	// Refuse before streaming anything: closing the whole channel after writing
	// would kill tar mid-extract. The caller cleans its empty staging directory
	// and falls back to the per-file path.
	half, ok := stream.(appconnection.HalfCloser)
	if !ok {
		return errExtractionUnsupported
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-finished:
		}
	}()
	defer close(finished)

	diagnostics := &diagnosticBuffer{limit: extractionDiagnosticLimit}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		if stderr := stream.Stderr(); stderr != nil {
			_, _ = io.Copy(diagnostics, stderr)
		}
	}()

	if _, err := io.Copy(stream, archive); err != nil {
		return err
	}
	if err := half.CloseWrite(); err != nil {
		return err
	}
	code, waitErr := stream.ExitCode()
	// ExitCode's Wait closes the output pipes, so the stderr reader ends.
	<-stderrDone
	if waitErr != nil {
		return waitErr
	}
	if code != 0 {
		message := strings.TrimSpace(diagnostics.String())
		if message == "" {
			message = "no diagnostics"
		}
		return fmt.Errorf("sftp: remote tar exited %d: %s", code, message)
	}
	return nil
}

// diagnosticBuffer keeps the first limit bytes written and discards the rest,
// so a remote that floods stderr cannot block on a full pipe or grow memory.
type diagnosticBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	if room := b.limit - len(b.data); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		b.data = append(b.data, p[:room]...)
	}
	b.mu.Unlock()
	return len(p), nil
}

func (b *diagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
