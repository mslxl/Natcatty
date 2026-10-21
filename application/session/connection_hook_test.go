package session

import (
	"context"
	"errors"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

type failingFactory struct {
	err error
}

func (f failingFactory) Open(context.Context, StartSpec) (Transport, error) {
	return nil, f.err
}

func TestConnectionHookReportsSuccessfulAndFailedAttempts(t *testing.T) {
	events := make(chan ConnectionEvent, 2)
	transport := newFakeTransport()
	service := NewService(&fakeFactory{transport: transport},
		WithConnectionHook(func(event ConnectionEvent) { events <- event }))
	t.Cleanup(service.CloseAll)

	spec := StartSpec{
		Kind: KindSSH, Label: "web-1", HostID: "host-1",
		Hostname: "10.0.0.1", Username: "deploy",
	}
	session, err := service.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	event := <-events
	if event.Err != nil {
		t.Fatalf("successful event error = %v", event.Err)
	}
	if event.SessionID != session.ID() || event.HostID != "host-1" ||
		event.Label != "web-1" || event.Hostname != "10.0.0.1" ||
		event.Username != "deploy" || event.Kind != KindSSH {
		t.Fatalf("event = %+v", event)
	}
	if event.StartedAt.IsZero() || event.EndedAt.IsZero() ||
		event.EndedAt.Before(event.StartedAt) {
		t.Fatalf("timestamps = %v..%v", event.StartedAt, event.EndedAt)
	}

	if _, _, _, ok := service.SessionInfo(session.ID()); !ok {
		t.Fatal("SessionInfo did not find the live Session")
	}
	hostID, label, kind, ok := service.SessionInfo(session.ID())
	if !ok || hostID != "host-1" || label != "web-1" || kind != KindSSH {
		t.Fatalf("SessionInfo = %q, %q, %q, %v", hostID, label, kind, ok)
	}
}

func TestConnectionHookReportsAFailedDial(t *testing.T) {
	events := make(chan ConnectionEvent, 1)
	want := errors.New("connection refused")
	service := NewService(failingFactory{err: want},
		WithConnectionHook(func(event ConnectionEvent) { events <- event }))

	_, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Label: "web-1", HostID: "host-1", Hostname: "10.0.0.1",
	})
	if !errors.Is(err, want) {
		t.Fatalf("Open error = %v, want the dial error", err)
	}
	event := <-events
	if !errors.Is(event.Err, want) {
		t.Fatalf("event error = %v", event.Err)
	}
	if event.SessionID != "" || event.HostID != "host-1" || event.Kind != KindSSH {
		t.Fatalf("event = %+v", event)
	}
}

func TestSessionInfoReportsTheLocalLabel(t *testing.T) {
	transport := newFakeTransport()
	service := NewService(&fakeFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	hostID, label, kind, ok := service.SessionInfo(session.ID())
	if !ok || hostID != model.HostID("") || label != "Local Terminal" || kind != KindLocal {
		t.Fatalf("SessionInfo = %q, %q, %q, %v", hostID, label, kind, ok)
	}
	if _, _, _, ok := service.SessionInfo("missing"); ok {
		t.Fatal("SessionInfo found a missing Session")
	}
}
