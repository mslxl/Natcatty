package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// readObservation reads until want appears or the deadline expires. The
// observer's Read blocks, so it runs on its own goroutine.
func readObservation(t *testing.T, reader io.Reader, want string) {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var collected []byte
		buffer := make([]byte, 256)
		for !bytes.Contains(collected, []byte(want)) {
			n, err := reader.Read(buffer)
			if n > 0 {
				collected = append(collected, buffer[:n]...)
			}
			if err != nil {
				done <- result{data: collected, err: err}
				return
			}
		}
		done <- result{data: collected}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read %q: %v (read so far %q)", want, got.err, got.data)
		}
		if !bytes.Contains(got.data, []byte(want)) {
			t.Fatalf("read %q, want %q", got.data, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out reading %q", want)
	}
}

// TestObserverSnapshotMatchesTheOwnerAndReceivesFeed proves the Snapshot/Feed
// facade: the observation starts with the exact output the owning Session has
// produced, then blocks for output produced afterwards.
func TestObserverSnapshotMatchesTheOwnerAndReceivesFeed(t *testing.T) {
	transport := newFakeTransport()
	factory := &fakeFactory{transport: transport}
	service := newTestService(factory, WithIDGenerator(func() model.SessionID { return "observer-1" }))
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	transport.send("first line\r\n")
	eventually(t, "the first line", func() bool {
		return session.Snapshot().Stats.OutputBytes >= int64(len("first line\r\n"))
	})

	observation, err := session.Observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	t.Cleanup(func() { _ = observation.Close() })

	screen := observation.Screen()
	if screen.SessionID != session.ID() {
		t.Fatalf("snapshot owner = %q, want %q", screen.SessionID, session.ID())
	}
	if screen.Cols != 100 || screen.Rows != 30 {
		t.Fatalf("snapshot size = %dx%d, want 100x30", screen.Cols, screen.Rows)
	}
	if got := string(screen.Data); got != "first line\r\n" {
		t.Fatalf("snapshot = %q, want the owner's output", got)
	}

	transport.send("second line\r\n")
	readObservation(t, observation, "second line")
}

// TestObserverCannotWriteOrConsumeTheLiveView proves an observer is not a
// second live view: it cannot type into the Session and it never drains the
// owning view's queue.
func TestObserverCannotWriteOrConsumeTheLiveView(t *testing.T) {
	transport := newFakeTransport()
	factory := &fakeFactory{transport: transport}
	service := newTestService(factory)
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	view, err := session.AttachView()
	if err != nil {
		t.Fatalf("attach the live view: %v", err)
	}
	t.Cleanup(func() { _ = view.Close() })

	transport.send("live output\r\n")
	readObservation(t, view, "live output")

	if got := session.LiveViews(); got != 1 {
		t.Fatalf("live views = %d, want 1", got)
	}
	if _, err := session.AttachView(); !errors.Is(err, ErrViewAttached) {
		t.Fatalf("second AttachView = %v, want ErrViewAttached", err)
	}

	observation, err := session.Observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	t.Cleanup(func() { _ = observation.Close() })

	transport.send("seen by both\r\n")
	eventually(t, "the live queue", func() bool {
		pending, _ := session.TerminalConn().stats()
		return pending == len("seen by both\r\n")
	})
	pendingBefore, _ := session.TerminalConn().stats()
	readObservation(t, observation, "seen by both")
	pendingAfter, _ := session.TerminalConn().stats()
	if pendingAfter != pendingBefore {
		t.Fatalf("observer changed the live queue: pending %d -> %d", pendingBefore, pendingAfter)
	}
	if _, err := observation.Write([]byte("typed into the observer\n")); !errors.Is(err, ErrObserverReadOnly) {
		t.Fatalf("observer Write = %v, want ErrObserverReadOnly", err)
	}
	if got := transport.input(); got != "" {
		t.Fatalf("observer input reached the transport: %q", got)
	}
	if got := session.LiveViews(); got != 1 {
		t.Fatalf("live views after observing = %d, want 1", got)
	}
}

// TestHibernateAndResumePreserveTheSessionState proves a hibernated Session
// keeps its transport and state, buffers output while no view renders it, and
// resumes with one live view that replays the screen and then follows live
// output.
func TestHibernateAndResumePreserveTheSessionState(t *testing.T) {
	transport := newFakeTransport()
	factory := &fakeFactory{transport: transport}
	service := newTestService(factory, WithIDGenerator(func() model.SessionID { return "hibernate-1" }))
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Cols: 90, Rows: 28})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	view, err := session.AttachView()
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	transport.send("before hibernate\r\n")
	readObservation(t, view, "before hibernate")

	before := session.Snapshot()
	if !session.Hibernate() {
		t.Fatal("Hibernate returned false")
	}
	if !session.Snapshot().Hibernated {
		t.Fatal("the Session did not report Hibernated")
	}
	if got := session.LiveViews(); got != 0 {
		t.Fatalf("Hibernate left %d live views, want none", got)
	}
	if n, err := view.Read(make([]byte, 8)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("the hibernated view read = %d, %v; want 0, io.EOF", n, err)
	}
	_ = view.Close()

	transport.send("while hibernated\r\n")
	eventually(t, "the buffered output", func() bool {
		return session.Snapshot().Stats.OutputBytes >=
			int64(len("before hibernate\r\nwhile hibernated\r\n"))
	})

	resumed, err := session.Resume()
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	t.Cleanup(func() { _ = resumed.Close() })

	if session.Snapshot().Hibernated {
		t.Fatal("Resume left the Session hibernated")
	}
	after := session.Snapshot()
	if after.ID != before.ID || after.Kind != before.Kind ||
		after.Status != before.Status || after.Connection != before.Connection ||
		after.Cols != before.Cols || after.Rows != before.Rows ||
		after.Encoding != before.Encoding || after.CWD != before.CWD {
		t.Fatalf("state changed across hibernate/resume:\nbefore %+v\nafter %+v", before, after)
	}
	if got := session.LiveViews(); got != 1 {
		t.Fatalf("live views after resume = %d, want 1", got)
	}

	readObservation(t, resumed, "before hibernate")
	transport.send("after resume\r\n")
	readObservation(t, resumed, "after resume")

	if _, err := resumed.Write([]byte("typed\r")); err != nil {
		t.Fatalf("write through the resumed view: %v", err)
	}
	eventually(t, "the typed input", func() bool { return transport.input() == "typed\r" })
}

// TestClearBufferReachesObserversAndTheReplay proves Clear buffer uses the
// rendered-output path, so a later observer cannot replay scrollback the
// owning screen already cleared.
func TestClearBufferReachesObserversAndTheReplay(t *testing.T) {
	transport := newFakeTransport()
	factory := &fakeFactory{transport: transport}
	service := newTestService(factory)
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	transport.send("old scrollback\r\n")
	eventually(t, "the old output", func() bool {
		return session.Snapshot().Stats.OutputBytes >= int64(len("old scrollback\r\n"))
	})
	live, err := session.AttachView()
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })
	readObservation(t, live, "old scrollback")

	observation, err := session.Observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	t.Cleanup(func() { _ = observation.Close() })
	readObservation(t, observation, "old scrollback")

	session.ClearBuffer()
	readObservation(t, live, "\x1b[3J")
	readObservation(t, observation, "\x1b[3J")

	next, err := session.Observe()
	if err != nil {
		t.Fatalf("observe after clear: %v", err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if !bytes.Contains(next.Screen().Data, []byte("\x1b[3J")) {
		t.Fatalf("a later snapshot lacks the clear sequence: %q", next.Screen().Data)
	}
}

// TestObservationEndsWhenTheSessionEnds proves an observer's blocking Read
// returns io.EOF instead of waiting forever after the owning Session ends.
func TestObservationEndsWhenTheSessionEnds(t *testing.T) {
	transport := newFakeTransport()
	factory := &fakeFactory{transport: transport}
	service := newTestService(factory)
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	transport.send("seen before close\r\n")
	eventually(t, "the output", func() bool {
		return session.Snapshot().Stats.OutputBytes >= int64(len("seen before close\r\n"))
	})
	observation, err := session.Observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	t.Cleanup(func() { _ = observation.Close() })

	done := make(chan error, 1)
	go func() {
		buffer := make([]byte, 64)
		for {
			if _, err := observation.Read(buffer); err != nil {
				done <- err
				return
			}
		}
	}()

	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the Session did not finish")
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("observation read after the Session ended = %v, want io.EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the observation read blocked after the Session ended")
	}
}

// TestObservationSnapshotIsBounded proves a long-running Session keeps a
// bounded replay and starts it at a line boundary, so an observer can never
// retain unbounded output.
func TestObservationSnapshotIsBounded(t *testing.T) {
	transport := newFakeTransport()
	factory := &fakeFactory{transport: transport}
	service := newTestService(factory)
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	line := strings.Repeat("x", 63) + "\n"
	payload := strings.Repeat(line, (SnapshotMaxBytes/len(line))+20)
	expected := len(payload)
	// fakeTransport delivers one chunk per Read, so send in read-sized pieces.
	for len(payload) > 0 {
		chunk := payload
		if len(chunk) > 32<<10 {
			chunk = chunk[:32<<10]
		}
		transport.send(chunk)
		payload = payload[len(chunk):]
	}
	eventually(t, "all of the output", func() bool {
		return session.Snapshot().Stats.OutputBytes >= int64(expected)
	})

	observation, err := session.Observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	t.Cleanup(func() { _ = observation.Close() })

	data := observation.Screen().Data
	if len(data) > SnapshotMaxBytes {
		t.Fatalf("snapshot = %d bytes, want at most %d", len(data), SnapshotMaxBytes)
	}
	if len(data) == 0 || data[0] == '\n' {
		t.Fatalf("snapshot does not start at a line boundary: %q", data[:min(len(data), 8)])
	}
	if !strings.HasSuffix(string(data), line) {
		t.Fatalf("snapshot lost the newest line: %q", data[len(data)-8:])
	}
}
