package rfb

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// TestRA2ReplayedServerFrameIsRejected proves the AES-EAX session counter is
// load-bearing: replaying a captured server frame under the next nonce fails
// authentication instead of delivering the plaintext twice.
func TestRA2ReplayedServerFrameIsRejected(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 16)
	writer, err := newEAXSession(key, key)
	if err != nil {
		t.Fatalf("newEAXSession: %v", err)
	}
	reader, err := newEAXSession(key, key)
	if err != nil {
		t.Fatalf("newEAXSession: %v", err)
	}

	var wire bytes.Buffer
	if err := writer.writeMessage(&wire, []byte("framebuffer update")); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}
	frame := append([]byte(nil), wire.Bytes()...)

	first, err := reader.readMessage(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("first readMessage: %v", err)
	}
	if string(first) != "framebuffer update" {
		t.Fatalf("first message = %q", first)
	}
	if _, err := reader.readMessage(bytes.NewReader(frame)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("replayed frame = %v, want ErrMalformed", err)
	}
}

// TestRA2GenerateKeyObservesCancellationAndTimeout proves key generation stops
// waiting when the caller's context is cancelled or its handshake budget runs
// out, so a hostile server cannot keep the client occupied indefinitely. The
// key size cap is a second, earlier bound.
func TestRA2GenerateKeyObservesCancellationAndTimeout(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		started := time.Now()
		if _, err := ra2GenerateKey(ctx, time.Minute, 2048); !errors.Is(err, context.Canceled) {
			t.Fatalf("ra2GenerateKey = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("cancelled key generation took %v", elapsed)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := time.Now()
		if _, err := ra2GenerateKey(ctx, time.Millisecond, 4096); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ra2GenerateKey = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("timed-out key generation took %v", elapsed)
		}
	})
}
