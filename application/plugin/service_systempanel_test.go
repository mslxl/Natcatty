package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

type panelServiceFixture struct {
	host      *appplugin.HostServices
	session   *appplugin.ServiceSession
	authority *appplugin.Authority
	audit     *recordingAudit
	ref       appplugin.PluginRef
}

func newPanelServiceFixture(t *testing.T, panel *appplugin.SystemPanel) *panelServiceFixture {
	t.Helper()
	authority := appplugin.NewAuthority()
	audit := &recordingAudit{}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority: authority,
		Audit:     audit,
		Now:       func() time.Time { return now },
		Panel:     panel,
	})
	ref := panelRef()
	session := host.Open(ref, domainplugin.NewFiberID("test", ref.ID, 1), nil)
	t.Cleanup(host.CloseAll)
	return &panelServiceFixture{
		host: host, session: session, authority: authority, audit: audit, ref: ref,
	}
}

func (f *panelServiceFixture) grant(permission domainplugin.Permission, scope domainplugin.Scope) {
	f.authority.Grant(appplugin.SubjectForRef(f.ref), domainplugin.PermissionGrant{
		Permission: permission,
		Scope:      scope,
		GrantedAt:  time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC),
	})
}

func activePanelService(t *testing.T) (*appplugin.SystemPanel, *panelSource) {
	t.Helper()
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	source := &panelSource{snapshot: domainplugin.PanelSnapshot{
		SampledAt: t0,
		Collectors: map[string]json.RawMessage{
			"overview": json.RawMessage(`{"hostname":"alpha"}`),
		},
	}}
	panel := appplugin.NewSystemPanel(appplugin.PanelDeps{Source: source})
	t.Cleanup(panel.Close)
	ref := panelRef()
	sections := []domainplugin.PanelSection{
		{ID: "live", Title: "Live", Refresh: domainplugin.PanelRefreshPush},
	}
	panel.Bind(func() []appplugin.PanelFiber {
		return []appplugin.PanelFiber{panelFiber(ref, sections, nil)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	return panel, source
}

func TestPanelSnapshotNeedsThePanelAndHostScopedSessionsRead(t *testing.T) {
	panel, source := activePanelService(t)
	fixture := newPanelServiceFixture(t, panel)
	collectors := []domainplugin.PanelCollector{domainplugin.PanelCollectorOverview}
	ctx := context.Background()

	if _, err := fixture.session.PanelSnapshot(ctx, "alpha", collectors); err == nil {
		t.Fatal("a snapshot without system.panel succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("missing system.panel code = %q, want denied", code)
	}
	if err := fixture.session.PanelUpdate("alpha", "live", panelRaw(t,
		domainplugin.PanelDescriptor{Kind: domainplugin.PanelDescriptorMetric, Label: "x"})); err == nil {
		t.Fatal("an update without system.panel succeeded")
	}

	// system.panel alone publishes plugin data but never reads the Host's
	// collectors.
	fixture.grant(domainplugin.PermissionSystemPanel, domainplugin.Scope{})
	raw := panelRaw(t, domainplugin.PanelDescriptor{
		Kind: domainplugin.PanelDescriptorMetric, Label: "own", Value: "1",
	})
	if err := fixture.session.PanelUpdate("alpha", "live", raw); err != nil {
		t.Fatalf("a push with only system.panel: %v", err)
	}
	if _, err := fixture.session.PanelSnapshot(ctx, "alpha", collectors); err == nil {
		t.Fatal("a snapshot without sessions.read succeeded")
	} else {
		var service *appplugin.ServiceError
		if !errors.As(err, &service) || service.Permission != domainplugin.PermissionSessionsRead {
			t.Fatalf("snapshot denial = %v, want sessions.read", err)
		}
		if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
			t.Fatalf("missing sessions.read code = %q, want denied", code)
		}
	}
	if source.callCount() != 0 {
		t.Fatalf("the collectors ran without sessions.read: %d calls", source.callCount())
	}

	fixture.grant(domainplugin.PermissionSessionsRead, domainplugin.Scope{
		Kind: domainplugin.ScopeSession, Host: "alpha",
	})
	snapshot, err := fixture.session.PanelSnapshot(ctx, "alpha", collectors)
	if err != nil {
		t.Fatalf("Host-scoped snapshot: %v", err)
	}
	if snapshot.HostID != "alpha" || string(snapshot.Collectors["overview"]) != `{"hostname":"alpha"}` {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if source.callCount() != 1 {
		t.Fatalf("source calls = %d, want 1", source.callCount())
	}
	// The Host scope does not widen to another Host.
	if _, err := fixture.session.PanelSnapshot(ctx, "beta", collectors); err == nil {
		t.Fatal("a Host-scoped grant read another Host")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("wrong-Host snapshot code = %q, want denied", code)
	}

	reasons := panelAuditReasons(fixture.audit)
	for _, want := range []string{
		"systempanel.snapshot:denied",
		"systempanel.update:denied",
		"systempanel.update:ok",
		"systempanel.snapshot:ok",
	} {
		if !containsReason(reasons, want) {
			t.Errorf("audit lacks %q, reasons %v", want, reasons)
		}
	}
}

func TestPanelSnapshotRejectsASessionOnlySessionsReadGrant(t *testing.T) {
	panel, source := activePanelService(t)
	fixture := newPanelServiceFixture(t, panel)
	fixture.grant(domainplugin.PermissionSystemPanel, domainplugin.Scope{})
	fixture.grant(domainplugin.PermissionSessionsRead, domainplugin.Scope{
		Kind: domainplugin.ScopeSession, Session: "session-1",
	})
	if _, err := fixture.session.PanelSnapshot(context.Background(), "alpha",
		[]domainplugin.PanelCollector{domainplugin.PanelCollectorOverview}); err == nil {
		t.Fatal("a Session-only grant accepted a Host-wide snapshot")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("session-only grant code = %q, want denied", code)
	}
	if source.callCount() != 0 {
		t.Fatalf("the collectors ran for a Session-scoped grant: %d calls", source.callCount())
	}
}

func TestPanelServiceDispatcherRoutesSnapshotAndUpdate(t *testing.T) {
	panel, _ := activePanelService(t)
	fixture := newPanelServiceFixture(t, panel)
	fixture.grant(domainplugin.PermissionSystemPanel, domainplugin.Scope{})
	fixture.grant(domainplugin.PermissionSessionsRead, domainplugin.Scope{
		Kind: domainplugin.ScopeSession, Host: "alpha",
	})

	update := dispatchService(t, fixture.session, appplugin.ServiceSystemPanel,
		domainpluginabi.SystempanelMethodUpdate,
		`{"host_id":"alpha","section_id":"live","payload":{"items":[{"kind":"metric","label":"x","value":"1"}]}}`)
	if update.Code != appplugin.ServiceCodeOK {
		t.Fatalf("update envelope = %+v", update)
	}
	invalid := dispatchService(t, fixture.session, appplugin.ServiceSystemPanel,
		domainpluginabi.SystempanelMethodUpdate, `{"section_id":"live","payload":{"items":[]}}`)
	if invalid.Code != appplugin.ServiceCodeInvalid {
		t.Fatalf("missing host envelope = %+v", invalid)
	}

	snapshot := dispatchService(t, fixture.session, appplugin.ServiceSystemPanel,
		domainpluginabi.SystempanelMethodSnapshot,
		`{"host_id":"alpha","collectors":["overview"]}`)
	if snapshot.Code != appplugin.ServiceCodeOK {
		t.Fatalf("snapshot envelope = %+v", snapshot)
	}
	var decoded domainplugin.PanelSnapshot
	if err := json.Unmarshal(snapshot.Data, &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if decoded.HostID != "alpha" || len(decoded.Collectors) != 1 {
		t.Fatalf("decoded snapshot = %+v", decoded)
	}

	noRead := newPanelServiceFixture(t, panel)
	noRead.grant(domainplugin.PermissionSystemPanel, domainplugin.Scope{})
	denied := dispatchService(t, noRead.session, appplugin.ServiceSystemPanel,
		domainpluginabi.SystempanelMethodSnapshot, `{"host_id":"alpha","collectors":["overview"]}`)
	if denied.Code != appplugin.ServiceCodeDenied || denied.Permission != string(domainplugin.PermissionSessionsRead) {
		t.Fatalf("denied snapshot envelope = %+v", denied)
	}
}
