package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestHostTransportImplementationSurvivesRestart proves a per-Host override
// travels in the Host record and reads back unchanged after the database is
// reopened, without changing anything else about the Host.
func TestHostTransportImplementationSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "natcatty.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0).UTC()
	host := domainvault.Host{
		ID: model.HostID("host-1"), Label: "Mosh Box", Hostname: "mosh.example",
		Port: 22, Username: "me", Protocol: domainvault.ProtocolMosh,
		AuthMethod:              domainvault.HostAuthPassword,
		TransportImplementation: transportmodel.InProcess,
		CreatedAt:               when, UpdatedAt: when,
	}
	if err := first.Save(ctx, host); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	loaded, err := second.Get(ctx, model.HostID("host-1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.TransportImplementation != transportmodel.InProcess {
		t.Fatalf("override after restart = %q, want %q", loaded.TransportImplementation, transportmodel.InProcess)
	}
	if loaded.Protocol != domainvault.ProtocolMosh || loaded.Label != "Mosh Box" {
		t.Fatalf("other Host fields changed: %+v", loaded)
	}
}
