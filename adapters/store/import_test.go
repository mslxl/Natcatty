package store

import (
	"context"
	"testing"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestSaveImportBatchWritesGroupsAndHostsTogether(t *testing.T) {
	store, _ := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	batch := appvault.ImportBatch{
		Groups: []domainvault.Group{{Path: "Prod", OrderKey: 1000, CreatedAt: now, UpdatedAt: now}},
		Hosts: []domainvault.Host{{
			ID: model.HostID("host-1"), Label: "web", Hostname: "web.example.test",
			Port: 2222, Username: "deploy", Protocol: domainvault.ProtocolSSH,
			Group: "Prod", OrderKey: 1000, CreatedAt: now, UpdatedAt: now,
		}},
	}
	if err := store.SaveImportBatch(context.Background(), batch); err != nil {
		t.Fatalf("SaveImportBatch: %v", err)
	}

	hosts, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID != "host-1" || hosts[0].Group != "Prod" {
		t.Fatalf("hosts = %+v", hosts)
	}
	groups, err := store.ListGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Path != "Prod" {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestSaveImportBatchWritesNothingWhenARecordIsInvalid(t *testing.T) {
	store, _ := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	batch := appvault.ImportBatch{
		Groups: []domainvault.Group{{Path: "Prod", OrderKey: 1000, CreatedAt: now, UpdatedAt: now}},
		Hosts: []domainvault.Host{
			{
				ID: model.HostID("host-1"), Label: "web", Hostname: "web.example.test",
				Protocol: domainvault.ProtocolSSH, Group: "Prod", OrderKey: 1000,
				CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: model.HostID("host-2"), Label: "broken", Hostname: "",
				Protocol: domainvault.ProtocolSSH, Group: "Prod", OrderKey: 2000,
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	if err := store.SaveImportBatch(context.Background(), batch); err == nil {
		t.Fatal("expected the batch to be rejected")
	}
	hosts, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 0 {
		t.Fatalf("partial Hosts were written: %+v", hosts)
	}
	groups, err := store.ListGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("partial groups were written: %+v", groups)
	}
}
