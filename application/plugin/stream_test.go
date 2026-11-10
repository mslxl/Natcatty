package plugin

import (
	"errors"
	"testing"
)

func TestStreamBufferEnforcesChunkAndBufferCeilings(t *testing.T) {
	stream := NewStreamBuffer()
	tooLarge := make([]byte, StreamChunkSize+1)
	var limit *LimitError
	if err := stream.Write(tooLarge); !errors.As(err, &limit) {
		t.Fatalf("oversized chunk = %v, want LimitError", err)
	}
	if limit.Resource != "stream chunk bytes" || limit.Limit != StreamChunkSize {
		t.Fatalf("limit = %+v, want the chunk ceiling", limit)
	}

	chunk := make([]byte, StreamChunkSize)
	if err := stream.Write(chunk); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if stream.Buffered() != StreamChunkSize {
		t.Fatalf("buffered = %d, want %d", stream.Buffered(), StreamChunkSize)
	}
	if stream.Credit() != StreamInitialWindow-StreamChunkSize {
		t.Fatalf("credit = %d, want the initial window minus the chunk", stream.Credit())
	}

	read, err := stream.Read()
	if err != nil || len(read) != StreamChunkSize {
		t.Fatalf("Read = %d bytes, %v", len(read), err)
	}
	if stream.Buffered() != 0 {
		t.Fatalf("buffered after read = %d", stream.Buffered())
	}
	if stream.Credit() != StreamInitialWindow {
		t.Fatalf("credit after read = %d, want %d", stream.Credit(), StreamInitialWindow)
	}
}

func TestStreamBufferBackpressureIsCreditBased(t *testing.T) {
	stream := NewStreamBuffer()
	chunk := make([]byte, StreamChunkSize)
	// Consume the 256 KiB initial window: four 64 KiB chunks.
	for i := 0; i < StreamInitialWindow/StreamChunkSize; i++ {
		if err := stream.Write(chunk); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := stream.Write([]byte{1}); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("Write without credit = %v, want ErrWouldBlock", err)
	}
	if _, err := stream.Read(); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := stream.Write([]byte{1}); err != nil {
		t.Fatalf("Write after a read restored credit: %v", err)
	}

	// Credit may grow to the buffer ceiling, but no further.
	if err := stream.Grant(StreamMaxBuffered); err == nil {
		t.Fatal("Grant past the buffer ceiling succeeded")
	}
	stream = NewStreamBuffer()
	if err := stream.Grant(StreamMaxBuffered - StreamInitialWindow); err != nil {
		t.Fatalf("Grant up to the ceiling: %v", err)
	}
	if stream.Credit() != StreamMaxBuffered {
		t.Fatalf("credit = %d, want %d", stream.Credit(), StreamMaxBuffered)
	}
	if err := stream.Grant(1); err == nil {
		t.Fatal("Grant beyond the ceiling succeeded")
	}
	if err := stream.Grant(-1); err == nil {
		t.Fatal("negative Grant succeeded")
	}
}

func TestStreamBufferCloseDrainsAndRejectsWrites(t *testing.T) {
	stream := NewStreamBuffer()
	if err := stream.Write([]byte("last")); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !stream.Closed() {
		t.Fatal("Closed reported false")
	}
	if err := stream.Write([]byte("more")); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Write after Close = %v, want ErrStreamClosed", err)
	}
	chunk, err := stream.Read()
	if err != nil || string(chunk) != "last" {
		t.Fatalf("Read after Close = %q, %v", chunk, err)
	}
	if _, err := stream.Read(); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Read drained stream = %v, want ErrStreamClosed", err)
	}
}

func TestStreamBufferEmptyOpenReadWouldBlock(t *testing.T) {
	stream := NewStreamBuffer()
	if _, err := stream.Read(); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("Read empty stream = %v, want ErrWouldBlock", err)
	}
}
