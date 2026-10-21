package app

import (
	"errors"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
)

func TestConnectionLogFromEventKeepsUnsavedTargetsAndDropsLocalLabels(t *testing.T) {
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
	ended := started.Add(time.Second)
	tests := []struct {
		name        string
		event       appsession.ConnectionEvent
		wantLabel   string
		wantHost    string
		wantUnsaved bool
		wantKind    string
		wantError   string
	}{
		{
			name: "local shell drops the application label",
			event: appsession.ConnectionEvent{
				Label: "Local Terminal", Kind: appsession.KindLocal,
				StartedAt: started, EndedAt: ended,
			},
			wantKind: "local",
		},
		{
			name: "unsaved Host keeps the typed label and address",
			event: appsession.ConnectionEvent{
				Label: "deploy@typed.example", Hostname: "typed.example",
				Username: "deploy", UnsavedHost: true, Kind: appsession.KindSSH,
				StartedAt: started, EndedAt: ended,
			},
			wantLabel: "deploy@typed.example", wantHost: "typed.example",
			wantUnsaved: true, wantKind: "ssh",
		},
		{
			name: "saved Host keeps its identity and label",
			event: appsession.ConnectionEvent{
				HostID: "host-1", Label: "web-1", Hostname: "10.0.0.1",
				Username: "deploy", Kind: appsession.KindSSH,
				StartedAt: started, EndedAt: ended,
			},
			wantLabel: "web-1", wantHost: "10.0.0.1", wantKind: "ssh",
		},
		{
			name: "failed attempt records the error",
			event: appsession.ConnectionEvent{
				Label: "deploy@typed.example", Hostname: "typed.example",
				Kind: appsession.KindSSH, UnsavedHost: true, Err: errors.New("connection refused"),
				StartedAt: started, EndedAt: ended,
			},
			wantLabel: "deploy@typed.example", wantHost: "typed.example",
			wantUnsaved: true, wantKind: "ssh", wantError: "connection refused",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := domainhistory.NormalizeConnectionLog(connectionLogFromEvent(test.event))
			if log.HostLabel != test.wantLabel || log.Hostname != test.wantHost {
				t.Fatalf("label/address = %q/%q, want %q/%q",
					log.HostLabel, log.Hostname, test.wantLabel, test.wantHost)
			}
			if log.UnsavedHost != test.wantUnsaved {
				t.Fatalf("unsaved = %v, want %v", log.UnsavedHost, test.wantUnsaved)
			}
			if log.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", log.Kind, test.wantKind)
			}
			if log.Error != test.wantError {
				t.Fatalf("error = %q, want %q", log.Error, test.wantError)
			}
			if !log.StartedAt.Equal(started) || !log.EndedAt.Equal(ended) {
				t.Fatalf("times = %v..%v", log.StartedAt, log.EndedAt)
			}
		})
	}
}
