package plugin_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// shutdownHangRuntime maps one plugin id onto the fake runtime's hostile
// shutdown fixture, so unload fails at the guest deadline.
type shutdownHangRuntime struct {
	inner *fake.Runtime
	id    string
}

func (r shutdownHangRuntime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	if spec.Name == r.id {
		spec.Name = fake.ShutdownHang
	}
	return r.inner.Instantiate(ctx, spec, sink)
}

func (r shutdownHangRuntime) Close(ctx context.Context) error { return r.inner.Close(ctx) }

// hostileManagerFixture builds a manager whose plugin activation succeeds but
// whose unload hangs past the short fixture deadline.
func hostileManagerFixture(t *testing.T, manifest domainplugin.Manifest,
	audit appplugin.AuditSink) (*appplugin.Manager, *memoryStore, *fixedValidator) {
	t.Helper()
	runtime := shutdownHangRuntime{
		inner: fake.New(fake.WithLimits(appplugin.Limits{
			UnloadTimeout: 25 * time.Millisecond,
		})),
		id: manifest.ID,
	}
	manager, store, validator, _ := managerFixture(t, manifest, nil, runtime, audit)
	return manager, store, validator
}

func enableRequiredCommands(t *testing.T, manager *appplugin.Manager, id string) {
	t.Helper()
	if err := manager.Enable(context.Background(), id,
		[]domainplugin.PermissionDecision{
			{Permission: domainplugin.PermissionCommands, Allow: true},
		}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
}

// assertUnloadDeadline preserves ticket 04's unload-deadline semantics: the
// manager still surfaces the typed guest deadline instead of swallowing it.
func assertUnloadDeadline(t *testing.T, err error) {
	t.Helper()
	var deadline *appplugin.DeadlineError
	if !errors.As(err, &deadline) {
		t.Fatalf("error = %v, want the guest DeadlineError from unload", err)
	}
	if deadline.Operation != "shutdown" {
		t.Fatalf("deadline operation = %q, want shutdown", deadline.Operation)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

// TestDisablePersistsRevocationWhenUnloadFails covers the durability bug: a
// hostile shutdown must not return before install.json drops the grants and
// the audit records the revoke/disable. A restart must not resurrect them.
func TestDisablePersistsRevocationWhenUnloadFails(t *testing.T) {
	manifest := managerManifest("acme.hostile", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, store, validator := hostileManagerFixture(t, manifest, audit)
	record := installForManager(t, manager, validator)
	enableRequiredCommands(t, manager, record.ID)

	err := manager.Disable(context.Background(), record.ID)
	assertUnloadDeadline(t, err)

	stored, ok, recordErr := store.Record(context.Background(), record.ID)
	if recordErr != nil || !ok {
		t.Fatalf("record after failed unload: ok=%v err=%v", ok, recordErr)
	}
	if stored.Enabled || len(stored.Grants) != 0 {
		t.Fatalf("disable did not persist the revocation: %+v", stored)
	}
	if !audit.has(domainplugin.AuditRevoke) || !audit.has(domainplugin.AuditDisable) {
		t.Fatal("disable did not audit both the revoke and the disable")
	}

	restored := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, validator, store, appplugin.WithAudit(audit))
	t.Cleanup(func() { _ = restored.Close(context.Background()) })
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	list, err := restored.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("restart resurrected the disabled plugin: %+v", list)
	}
	subject := appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID)
	if err := restored.Authority().Check(subject,
		domainplugin.PermissionCommands, domainplugin.Scope{}); !errors.Is(err, appplugin.ErrPermissionNotGranted) {
		t.Fatalf("restart resurrected the revoked grant: %v", err)
	}
}

// TestRevokePersistsTheRequiredRevocationWhenUnloadFails is the same guarantee
// for a required-permission revoke: the record is saved disabled with the
// reason and the audit event exists even though the guest ignored shutdown.
func TestRevokePersistsTheRequiredRevocationWhenUnloadFails(t *testing.T) {
	manifest := managerManifest("acme.hostile", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, store, validator := hostileManagerFixture(t, manifest, audit)
	record := installForManager(t, manager, validator)
	enableRequiredCommands(t, manager, record.ID)

	err := manager.Revoke(context.Background(), record.ID,
		domainplugin.PermissionCommands)
	assertUnloadDeadline(t, err)

	stored, ok, recordErr := store.Record(context.Background(), record.ID)
	if recordErr != nil || !ok {
		t.Fatalf("record after failed unload: ok=%v err=%v", ok, recordErr)
	}
	if stored.Enabled {
		t.Fatalf("revoke did not persist the disabled state: %+v", stored)
	}
	if _, exists := stored.Grant(domainplugin.PermissionCommands); exists {
		t.Fatalf("revoke left the required grant in install.json: %+v", stored.Grants)
	}
	if want := domainplugin.PermissionReason(domainplugin.ReasonPermissionRevoked,
		domainplugin.PermissionCommands); stored.Reason != want {
		t.Fatalf("reason = %q, want %q", stored.Reason, want)
	}
	if !audit.has(domainplugin.AuditRevoke) {
		t.Fatal("revoke was not audited when unload failed")
	}

	restored := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, validator, store, appplugin.WithAudit(audit))
	t.Cleanup(func() { _ = restored.Close(context.Background()) })
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	list, err := restored.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Enabled {
		t.Fatalf("restart resurrected the revoked plugin: %+v", list)
	}
	subject := appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID)
	if err := restored.Authority().Check(subject,
		domainplugin.PermissionCommands, domainplugin.Scope{}); !errors.Is(err, appplugin.ErrPermissionNotGranted) {
		t.Fatalf("restart resurrected the revoked grant: %v", err)
	}
}

// TestUninstallRemovesTheRecordWhenUnloadFails proves a hostile shutdown does
// not leave the package installed or skip the uninstall audit event.
func TestUninstallRemovesTheRecordWhenUnloadFails(t *testing.T) {
	manifest := managerManifest("acme.hostile", []string{"commands"}, nil, nil)
	audit := &recordingAudit{}
	manager, store, validator := hostileManagerFixture(t, manifest, audit)
	record := installForManager(t, manager, validator)
	enableRequiredCommands(t, manager, record.ID)

	err := manager.Uninstall(context.Background(), record.ID, true)
	assertUnloadDeadline(t, err)

	if _, ok, recordErr := store.Record(context.Background(), record.ID); recordErr != nil {
		t.Fatalf("Record: %v", recordErr)
	} else if ok {
		t.Fatal("uninstall left the install record on disk when unload failed")
	}
	if _, statErr := os.Stat(store.DataDir(record.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("confirmed purge left the data directory: %v", statErr)
	}
	if !audit.has(domainplugin.AuditUninstall) || !audit.has(domainplugin.AuditPurge) {
		t.Fatal("uninstall and purge were not both audited when unload failed")
	}

	restored := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, validator, store, appplugin.WithAudit(audit))
	t.Cleanup(func() { _ = restored.Close(context.Background()) })
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	list, err := restored.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("restart resurrected the uninstalled plugin: %+v", list)
	}
	subject := appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, record.ID)
	if err := restored.Authority().Check(subject,
		domainplugin.PermissionCommands, domainplugin.Scope{}); !errors.Is(err, appplugin.ErrPermissionNotGranted) {
		t.Fatalf("restart resurrected the uninstalled grant: %v", err)
	}
}
