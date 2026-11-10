package plugin_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

type serviceFixture struct {
	host      *appplugin.HostServices
	session   *appplugin.ServiceSession
	authority *appplugin.Authority
	audit     *recordingAudit
	clipboard *appplugin.MemoryClipboard
	sink      *appplugin.MemoryNotificationSink
	ref       appplugin.PluginRef
	fiber     domainplugin.FiberID
	now       *time.Time
	dataRoot  string
}

func defaultServiceLimits() appplugin.ServiceLimits {
	return appplugin.ServiceLimits{
		EventQueueDepth:    8,
		NotificationRate:   2,
		NotificationWindow: time.Minute,
		StorageValueBytes:  1 << 20,
		StoragePluginBytes: 32 << 20,
	}
}

func newServiceFixture(t *testing.T, limits appplugin.ServiceLimits) *serviceFixture {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	audit := &recordingAudit{}
	clipboard := appplugin.NewMemoryClipboard("initial clipboard")
	sink := &appplugin.MemoryNotificationSink{}
	authority := appplugin.NewAuthority()
	ref := appplugin.PluginRef{
		ID:      "acme.services",
		Name:    "Acme Services",
		Version: "1.0.0",
		Source:  domainplugin.SourceLocalFile,
	}
	fiber := domainplugin.NewFiberID("installed", ref.ID, 1)
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority:     authority,
		Audit:         audit,
		Now:           func() time.Time { return now },
		DataDir:       func(id string) string { return filepath.Join(root, id) },
		Clipboard:     clipboard,
		Notifications: sink,
		HostSettings: appplugin.MemoryHostSettings{Values: map[string]string{
			"app.locale":   "en",
			"app.platform": "darwin/arm64",
		}},
		Limits: limits,
	})
	session := host.Open(ref, fiber, map[string]any{"greeting": "hello"})
	session.BindHandles(appplugin.NewHandleTable(appplugin.MaxHandles))
	t.Cleanup(func() { host.CloseAll() })
	return &serviceFixture{
		host: host, session: session, authority: authority, audit: audit,
		clipboard: clipboard, sink: sink, ref: ref, fiber: fiber,
		now: &now, dataRoot: root,
	}
}

func (f *serviceFixture) grant(permission domainplugin.Permission, scope domainplugin.Scope) {
	f.authority.Grant(appplugin.SubjectForRef(f.ref), domainplugin.PermissionGrant{
		Permission: permission, Scope: scope, GrantedAt: *f.now,
	})
}

func serviceErrorCode(t *testing.T, err error) string {
	t.Helper()
	var service *appplugin.ServiceError
	if !errors.As(err, &service) {
		t.Fatalf("error %v is not a *ServiceError", err)
	}
	return service.Code
}

func TestPrivateStorageQuotasPersistenceAndTypedFailures(t *testing.T) {
	limits := defaultServiceLimits()
	limits.StorageValueBytes = 8
	limits.StoragePluginBytes = 12
	fixture := newServiceFixture(t, limits)
	fixture.grant(domainplugin.PermissionStorage, domainplugin.Scope{})

	if err := fixture.session.StorageSet("alpha", []byte("12345678")); err != nil {
		t.Fatalf("StorageSet first value: %v", err)
	}
	if err := fixture.session.StorageSet("beta", []byte("12345")); err == nil {
		t.Fatal("per-plugin quota was not enforced")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("per-plugin quota code = %q, want limit", code)
	}
	if err := fixture.session.StorageSet("big", []byte("123456789")); err == nil {
		t.Fatal("per-value quota was not enforced")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("per-value quota code = %q, want limit", code)
	}
	if err := fixture.session.StorageSet("bad/key", []byte("x")); err == nil {
		t.Fatal("path separator key was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("invalid key code = %q, want invalid", code)
	}

	value, err := fixture.session.StorageGet("alpha")
	if err != nil || string(value) != "12345678" {
		t.Fatalf("StorageGet = %q, %v", value, err)
	}
	if _, err := fixture.session.StorageGet("missing"); err == nil {
		t.Fatal("missing key did not fail")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeNotFound {
		t.Fatalf("missing key code = %q, want not-found", code)
	}
	if err := fixture.session.StorageDelete("missing"); err == nil {
		t.Fatal("delete of a missing key did not fail")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeNotFound {
		t.Fatalf("delete missing code = %q, want not-found", code)
	}

	// A new host over the same data directory proves the value survives a
	// restart rather than living only in the session.
	restarted := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority: fixture.authority,
		Audit:     fixture.audit,
		Now:       func() time.Time { return *fixture.now },
		DataDir:   func(id string) string { return filepath.Join(fixture.dataRoot, id) },
		Limits:    limits,
	})
	other := restarted.Open(fixture.ref, domainplugin.NewFiberID("installed", fixture.ref.ID, 2), nil)
	other.BindHandles(appplugin.NewHandleTable(appplugin.MaxHandles))
	value, err = other.StorageGet("alpha")
	if err != nil || string(value) != "12345678" {
		t.Fatalf("persisted StorageGet = %q, %v", value, err)
	}
	restarted.CloseAll()

	if !fixture.audit.has(domainplugin.AuditService) {
		t.Fatal("storage access was not audited")
	}
}

func TestPrivateStorageListAndDeniedAccess(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionStorage, domainplugin.Scope{})
	for _, key := range []string{"alpha", "alpine", "beta"} {
		if err := fixture.session.StorageSet(key, []byte("v")); err != nil {
			t.Fatalf("StorageSet(%q): %v", key, err)
		}
	}
	keys, err := fixture.session.StorageList("al")
	if err != nil || len(keys) != 2 || keys[0] != "alpha" || keys[1] != "alpine" {
		t.Fatalf("StorageList = %v, %v", keys, err)
	}

	denied := newServiceFixture(t, defaultServiceLimits())
	_, err = denied.session.StorageGet("anything")
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("denied storage code = %q", code)
	}
	if !denied.audit.has(domainplugin.AuditService) {
		t.Fatal("denied storage access was not audited")
	}
	var service *appplugin.ServiceError
	if !errors.As(err, &service) || service.Permission != domainplugin.PermissionStorage {
		t.Fatalf("denied error = %+v", service)
	}
}

func TestClipboardPermissionsAreSeparateAndAudited(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionClipboardRead, domainplugin.Scope{})

	text, err := fixture.session.ClipboardRead(context.Background())
	if err != nil || text != "initial clipboard" {
		t.Fatalf("ClipboardRead = %q, %v", text, err)
	}
	err = fixture.session.ClipboardWrite(context.Background(), "new text")
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("write without clipboard.write code = %q", code)
	}
	if got, _ := fixture.clipboard.ReadText(context.Background()); got != "initial clipboard" {
		t.Fatalf("clipboard after denied write = %q", got)
	}

	fixture.grant(domainplugin.PermissionClipboardWrite, domainplugin.Scope{})
	if err := fixture.session.ClipboardWrite(context.Background(), "new text"); err != nil {
		t.Fatalf("ClipboardWrite: %v", err)
	}
	if got, _ := fixture.clipboard.ReadText(context.Background()); got != "new text" {
		t.Fatalf("clipboard = %q, want new text", got)
	}
	auditRows := fixture.audit.count(domainplugin.AuditService)
	if auditRows < 3 {
		t.Fatalf("service audit rows = %d, want at least 3", auditRows)
	}
}

func TestNotificationsAreRateLimitedDeduplicatedAndActionsBecomeEvents(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionNotifications, domainplugin.Scope{})
	fixture.grant(domainplugin.PermissionEventsSubscribe,
		domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: "notification.action"})
	handle, err := fixture.session.EventsSubscribe("notification.action")
	if err != nil {
		t.Fatalf("EventsSubscribe: %v", err)
	}
	actions := []appplugin.NotificationAction{{ID: "open", Label: "Open"}, {ID: "dismiss", Label: "Dismiss"}}

	first, err := fixture.session.NotificationPost(context.Background(), "Build finished", "All tests passed", actions)
	if err != nil || first.ID == "" || first.Deduplicated {
		t.Fatalf("first NotificationPost = %+v, %v", first, err)
	}
	duplicate, err := fixture.session.NotificationPost(context.Background(), "Build finished", "All tests passed", actions)
	if err != nil || !duplicate.Deduplicated || duplicate.ID != first.ID {
		t.Fatalf("duplicate NotificationPost = %+v, %v", duplicate, err)
	}
	if got := fixture.sink.Len(); got != 1 {
		t.Fatalf("posted notifications = %d, want 1", got)
	}

	if _, err := fixture.session.NotificationPost(context.Background(), "Second", "one", nil); err != nil {
		t.Fatalf("second unique notification: %v", err)
	}
	_, err = fixture.session.NotificationPost(context.Background(), "Third", "two", nil)
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("rate limit code = %q, want limit", code)
	}
	if got := fixture.sink.Len(); got != 2 {
		t.Fatalf("posted notifications after rate limit = %d, want 2", got)
	}

	if err := fixture.host.NotificationAction(first.ID, "open"); err != nil {
		t.Fatalf("NotificationAction: %v", err)
	}
	events, err := fixture.session.EventsPoll(handle, 8)
	if err != nil {
		t.Fatalf("EventsPoll: %v", err)
	}
	if len(events) != 1 || events[0].Type != domainplugin.EventNotificationAction {
		t.Fatalf("action events = %+v", events)
	}
	var payload map[string]string
	if err := json.Unmarshal(events[0].Data, &payload); err != nil {
		t.Fatalf("decode action payload: %v", err)
	}
	if payload["notification"] != first.ID || payload["action"] != "open" {
		t.Fatalf("action payload = %v", payload)
	}
	err = fixture.host.NotificationAction(first.ID, "open")
	if err == nil {
		t.Fatal("the same action was accepted twice")
	}
	var missing *appplugin.NotificationNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("second action error = %v, want NotificationNotFoundError", err)
	}

	// The window is fixed: after it elapses a new unique post is allowed.
	*fixture.now = fixture.now.Add(time.Minute)
	if _, err := fixture.session.NotificationPost(context.Background(), "After window", "ok", nil); err != nil {
		t.Fatalf("notification after window: %v", err)
	}
}

func TestNotificationDeniedAndUnknownActionAreTyped(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	_, err := fixture.session.NotificationPost(context.Background(), "Title", "Body", nil)
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("denied notification code = %q", code)
	}
	fixture.grant(domainplugin.PermissionNotifications, domainplugin.Scope{})
	result, err := fixture.session.NotificationPost(context.Background(), "Title", "Body",
		[]appplugin.NotificationAction{{ID: "open", Label: "Open"}})
	if err != nil {
		t.Fatalf("NotificationPost: %v", err)
	}
	err = fixture.host.NotificationAction(result.ID, "delete")
	if err == nil {
		t.Fatal("an undeclared action was accepted")
	}
	var action *appplugin.NotificationActionError
	if !errors.As(err, &action) {
		t.Fatalf("error = %v, want NotificationActionError", err)
	}
	if err := fixture.session.NotificationDismiss(result.ID); err != nil {
		t.Fatalf("NotificationDismiss: %v", err)
	}
}

func TestNotificationWithoutSinkFailsTyped(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.host.CloseAll()
	root := t.TempDir()
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority: fixture.authority,
		Now:       func() time.Time { return *fixture.now },
		DataDir:   func(id string) string { return filepath.Join(root, id) },
	})
	session := host.Open(fixture.ref, domainplugin.NewFiberID("installed", fixture.ref.ID, 3), nil)
	session.BindHandles(appplugin.NewHandleTable(appplugin.MaxHandles))
	t.Cleanup(func() { host.CloseAll() })
	if err := fixture.authority.Check(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionNotifications, domainplugin.Scope{}); err != nil {
		fixture.grant(domainplugin.PermissionNotifications, domainplugin.Scope{})
	}
	_, err := session.NotificationPost(context.Background(), "Title", "Body", nil)
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeFault {
		t.Fatalf("notification without sink code = %q, want fault", code)
	}
}

func TestEventsPreservePerTypeOrderingAndEmitGapEvents(t *testing.T) {
	limits := defaultServiceLimits()
	limits.EventQueueDepth = 2
	fixture := newServiceFixture(t, limits)
	fixture.grant(domainplugin.PermissionEventsSubscribe,
		domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: "terminal.exit"})
	handle, err := fixture.session.EventsSubscribe("terminal.exit")
	if err != nil {
		t.Fatalf("EventsSubscribe: %v", err)
	}
	for i := 0; i < 4; i++ {
		fixture.host.Publish(domainplugin.EventTerminalExit, map[string]int{"n": i})
	}
	// A different type must not enter this subscription's queue or sequence.
	fixture.host.Publish(domainplugin.EventVaultChanged, map[string]string{"vault": "unrelated"})

	events, err := fixture.session.EventsPoll(handle, 8)
	if err != nil {
		t.Fatalf("EventsPoll: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want gap plus two survivors", events)
	}
	if events[0].Type != domainplugin.EventGap {
		t.Fatalf("first event = %+v, want events.gap", events[0])
	}
	var gap map[string]any
	if err := json.Unmarshal(events[0].Data, &gap); err != nil {
		t.Fatalf("decode gap: %v", err)
	}
	if gap["type"] != "terminal.exit" || gap["dropped"] != float64(2) {
		t.Fatalf("gap payload = %v", gap)
	}
	if events[1].Sequence >= events[2].Sequence {
		t.Fatalf("survivor order = %d, %d", events[1].Sequence, events[2].Sequence)
	}
	var second map[string]int
	if err := json.Unmarshal(events[2].Data, &second); err != nil || second["n"] != 3 {
		t.Fatalf("newest event = %+v, %v", events[2], err)
	}

	// A limited poll returns the oldest events first and leaves the rest.
	for i := 0; i < 3; i++ {
		fixture.host.Publish(domainplugin.EventTerminalExit, map[string]int{"n": i})
	}
	first, err := fixture.session.EventsPoll(handle, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("limited poll = %+v, %v", first, err)
	}
	secondBatch, err := fixture.session.EventsPoll(handle, 8)
	if err != nil || len(secondBatch) != 2 {
		t.Fatalf("second poll = %+v, %v", secondBatch, err)
	}
	if first[0].Sequence >= secondBatch[0].Sequence {
		t.Fatalf("poll order = %d then %d", first[0].Sequence, secondBatch[0].Sequence)
	}
}

func TestEventsDeniedUnknownTypeAndRevokedHandleAreTyped(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	_, err := fixture.session.EventsSubscribe("terminal.exit")
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("denied subscribe code = %q", code)
	}
	fixture.grant(domainplugin.PermissionEventsSubscribe,
		domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: "terminal.exit"})
	_, err = fixture.session.EventsSubscribe("not.an.event")
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("unknown type code = %q", code)
	}
	handle, err := fixture.session.EventsSubscribe("terminal.exit")
	if err != nil {
		t.Fatalf("EventsSubscribe: %v", err)
	}
	if err := fixture.session.EventsUnsubscribe(handle); err != nil {
		t.Fatalf("EventsUnsubscribe: %v", err)
	}
	_, err = fixture.session.EventsPoll(handle, 1)
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("revoked handle code = %q", code)
	}
	// Closing the fiber invalidates a live subscription handle too.
	handle, err = fixture.session.EventsSubscribe("terminal.exit")
	if err != nil {
		t.Fatalf("EventsSubscribe again: %v", err)
	}
	fixture.host.Close(fixture.fiber)
	_, err = fixture.session.EventsPoll(handle, 1)
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("closed session poll code = %q", code)
	}
}

// TestRevokedSubscriptionCannotBeReusedAfterRegrant covers spec line 184:
// revocation invalidates every issued handle, including subscriptions, and a
// later grant for the same permission must not revive the old handle or
// deliver events queued before the revocation.
func TestRevokedSubscriptionCannotBeReusedAfterRegrant(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	scope := domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: "terminal.exit"}
	fixture.grant(domainplugin.PermissionEventsSubscribe, scope)
	handle, err := fixture.session.EventsSubscribe("terminal.exit")
	if err != nil {
		t.Fatalf("EventsSubscribe: %v", err)
	}
	fixture.host.Publish(domainplugin.EventTerminalExit, map[string]int{"n": 1})
	fixture.host.Publish(domainplugin.EventTerminalExit, map[string]int{"n": 2})

	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionEventsSubscribe)
	if count := fixture.host.SubscribeCount(); count != 0 {
		t.Fatalf("live subscriptions after revocation = %d, want 0", count)
	}

	// Events published while the permission is revoked are not queued.
	fixture.host.Publish(domainplugin.EventTerminalExit, map[string]int{"n": 3})
	fixture.grant(domainplugin.PermissionEventsSubscribe, scope)

	_, err = fixture.session.EventsPoll(handle, 8)
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("revoked subscription poll code = %q, want invalid", code)
	}

	// A fresh subscription after the re-grant starts empty: neither the
	// pre-revocation queue nor the post-revocation publish is delivered.
	fresh, err := fixture.session.EventsSubscribe("terminal.exit")
	if err != nil {
		t.Fatalf("EventsSubscribe after regrant: %v", err)
	}
	events, err := fixture.session.EventsPoll(fresh, 8)
	if err != nil {
		t.Fatalf("EventsPoll after regrant: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events after regrant = %+v, want an empty queue", events)
	}
}

// TestPrivateStorageIsIsolatedPerPluginIdentity proves two plugin identities
// sharing one host service never see each other's KV namespace.
func TestPrivateStorageIsIsolatedPerPluginIdentity(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionStorage, domainplugin.Scope{})
	if err := fixture.session.StorageSet("first-secret", []byte("one")); err != nil {
		t.Fatalf("StorageSet: %v", err)
	}

	otherRef := fixture.ref
	otherRef.ID = "acme.other"
	otherSubject := appplugin.SubjectForRef(otherRef)
	fixture.authority.Grant(otherSubject, domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionStorage, GrantedAt: *fixture.now,
	})
	other := fixture.host.Open(otherRef,
		domainplugin.NewFiberID("installed", otherRef.ID, 1), nil)
	other.BindHandles(appplugin.NewHandleTable(appplugin.MaxHandles))
	if _, err := other.StorageGet("first-secret"); err == nil {
		t.Fatal("a second plugin identity read another plugin's value")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeNotFound {
		t.Fatalf("other identity get code = %q, want not-found", code)
	}
	keys, err := other.StorageList("")
	if err != nil {
		t.Fatalf("other StorageList: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("other identity list = %v, want empty", keys)
	}
	if err := other.StorageSet("second-secret", []byte("two")); err != nil {
		t.Fatalf("other StorageSet: %v", err)
	}
	if _, err := fixture.session.StorageGet("second-secret"); err == nil {
		t.Fatal("the first plugin identity read another plugin's value")
	}
	keys, err = fixture.session.StorageList("")
	if err != nil || len(keys) != 1 || keys[0] != "first-secret" {
		t.Fatalf("first identity list = %v, %v", keys, err)
	}
}

func TestHostSettingsReadIsKeyScoped(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	localeScope := domainplugin.Scope{Kind: domainplugin.ScopeSettings, Key: "app.locale"}
	fixture.grant(domainplugin.PermissionSettingsRead, localeScope)
	value, err := fixture.session.SettingsRead(context.Background(), "app.locale")
	if err != nil || value != "en" {
		t.Fatalf("SettingsRead = %q, %v", value, err)
	}
	_, err = fixture.session.SettingsRead(context.Background(), "app.platform")
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("other key code = %q, want denied", code)
	}
	platformScope := domainplugin.Scope{Kind: domainplugin.ScopeSettings, Key: "app.platform"}
	fixture.grant(domainplugin.PermissionSettingsRead, platformScope)
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref), domainplugin.PermissionSettingsRead)
	// Revocation drops every key grant for the permission.
	_, err = fixture.session.SettingsRead(context.Background(), "app.locale")
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("revoked settings code = %q", code)
	}
	_, err = fixture.session.SettingsRead(context.Background(), "")
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("empty key code = %q, want invalid", code)
	}
}

type serviceEnvelope struct {
	Code       string          `json:"code"`
	Message    string          `json:"message,omitempty"`
	Permission string          `json:"permission,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

func dispatchService(t *testing.T, session *appplugin.ServiceSession,
	service string, method uint32, request string) serviceEnvelope {
	t.Helper()
	var payload []byte
	if request != "" {
		payload = []byte(request)
	}
	response := session.Dispatcher().Call(context.Background(),
		appplugin.AbiServiceCall(service, method, payload))
	var envelope serviceEnvelope
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatalf("decode %s.%d response %q: %v", service, method, response, err)
	}
	return envelope
}

// TestSamplePluginExercisesEveryHostService is the baseline sample: one guest
// session uses storage, clipboard, notifications, events, and settings through
// the same JSON dispatcher the WASM service modules call, then an ungranted
// session proves every denial is typed rather than a silent no-op.
func TestSamplePluginExercisesEveryHostService(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionStorage, domainplugin.Scope{})
	fixture.grant(domainplugin.PermissionClipboardRead, domainplugin.Scope{})
	fixture.grant(domainplugin.PermissionClipboardWrite, domainplugin.Scope{})
	fixture.grant(domainplugin.PermissionNotifications, domainplugin.Scope{})
	fixture.grant(domainplugin.PermissionEventsSubscribe,
		domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: "terminal.exit"})
	fixture.grant(domainplugin.PermissionEventsSubscribe,
		domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: "notification.action"})
	fixture.grant(domainplugin.PermissionSettingsRead,
		domainplugin.Scope{Kind: domainplugin.ScopeSettings, Key: "app.locale"})

	// Storage: set, get, list, delete.
	stored := base64.StdEncoding.EncodeToString([]byte("sample-value"))
	if got := dispatchService(t, fixture.session, "storage", domainpluginabi.StorageMethodSet,
		fmt.Sprintf(`{"key":"greeting","value":%q}`, stored)); got.Code != "ok" {
		t.Fatalf("storage.set = %+v", got)
	}
	gotValue := dispatchService(t, fixture.session, "storage", domainpluginabi.StorageMethodGet,
		`{"key":"greeting"}`)
	if gotValue.Code != "ok" {
		t.Fatalf("storage.get = %+v", gotValue)
	}
	var valueData struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(gotValue.Data, &valueData); err != nil {
		t.Fatalf("storage.get data = %s: %v", gotValue.Data, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(valueData.Value)
	if err != nil || string(decoded) != "sample-value" {
		t.Fatalf("storage.get value = %q, %v", decoded, err)
	}
	if got := dispatchService(t, fixture.session, "storage", domainpluginabi.StorageMethodList,
		`{"prefix":"gree"}`); got.Code != "ok" || !strings.Contains(string(got.Data), "greeting") {
		t.Fatalf("storage.list = %+v %s", got, got.Data)
	}
	if got := dispatchService(t, fixture.session, "storage", domainpluginabi.StorageMethodDelete,
		`{"key":"greeting"}`); got.Code != "ok" {
		t.Fatalf("storage.delete = %+v", got)
	}

	// Clipboard: write then read through separate permissions.
	if got := dispatchService(t, fixture.session, "clipboard", domainpluginabi.ClipboardMethodWrite,
		`{"text":"plugin text"}`); got.Code != "ok" {
		t.Fatalf("clipboard.write = %+v", got)
	}
	if got := dispatchService(t, fixture.session, "clipboard", domainpluginabi.ClipboardMethodRead, ""); got.Code != "ok" ||
		!strings.Contains(string(got.Data), "plugin text") {
		t.Fatalf("clipboard.read data = %+v %s", got, got.Data)
	}

	// Events subscribe before the action so its result is delivered.
	subscribed := dispatchService(t, fixture.session, "events", domainpluginabi.EventsMethodSubscribe,
		`{"type":"notification.action"}`)
	if subscribed.Code != "ok" {
		t.Fatalf("events.subscribe action = %+v", subscribed)
	}
	var subscribeData struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(subscribed.Data, &subscribeData); err != nil || subscribeData.Handle == "" {
		t.Fatalf("subscribe data = %s: %v", subscribed.Data, err)
	}
	if got := dispatchService(t, fixture.session, "notifications", domainpluginabi.NotificationsMethodPost,
		`{"title":"Sample","body":"Choose","actions":[{"id":"open","label":"Open"}]}`); got.Code != "ok" {
		t.Fatalf("notifications.post = %+v", got)
	}
	if got := dispatchService(t, fixture.session, "notifications",
		domainpluginabi.NotificationsMethodPost,
		`{"title":"Sample two","body":"Choose"}`); got.Code != "ok" {
		t.Fatalf("second notifications.post = %+v", got)
	}
	posted := fixture.sink.Notifications()
	if len(posted) != 2 || posted[0].ID == "" {
		t.Fatalf("posted notifications = %+v", posted)
	}
	if err := fixture.host.NotificationAction(posted[0].ID, "open"); err != nil {
		t.Fatalf("NotificationAction: %v", err)
	}
	polled := dispatchService(t, fixture.session, "events", domainpluginabi.EventsMethodPoll,
		`{"handle":"`+subscribeData.Handle+`","limit":8}`)
	if polled.Code != "ok" || !strings.Contains(string(polled.Data), "notification.action") {
		t.Fatalf("events.poll = %+v %s", polled, polled.Data)
	}
	if got := dispatchService(t, fixture.session, "events", domainpluginabi.EventsMethodUnsubscribe,
		`{"handle":"`+subscribeData.Handle+`"}`); got.Code != "ok" {
		t.Fatalf("events.unsubscribe = %+v", got)
	}

	// Settings read is key-scoped and read-only.
	if got := dispatchService(t, fixture.session, "settings", domainpluginabi.SettingsMethodRead,
		`{"key":"app.locale"}`); got.Code != "ok" || !strings.Contains(string(got.Data), `"en"`) {
		t.Fatalf("settings.read = %+v %s", got, got.Data)
	}

	// The same calls from an ungranted instance are typed denials.
	deniedRef := fixture.ref
	deniedRef.ID = "acme.denied"
	denied := fixture.host.Open(deniedRef,
		domainplugin.NewFiberID("installed", deniedRef.ID, 1), nil)
	denied.BindHandles(appplugin.NewHandleTable(appplugin.MaxHandles))
	denials := []serviceEnvelope{
		dispatchService(t, denied, "storage", domainpluginabi.StorageMethodGet, `{"key":"x"}`),
		dispatchService(t, denied, "clipboard", domainpluginabi.ClipboardMethodRead, ""),
		dispatchService(t, denied, "clipboard", domainpluginabi.ClipboardMethodWrite, `{"text":"x"}`),
		dispatchService(t, denied, "notifications", domainpluginabi.NotificationsMethodPost,
			`{"title":"x","body":"y"}`),
		dispatchService(t, denied, "events", domainpluginabi.EventsMethodSubscribe,
			`{"type":"terminal.exit"}`),
		dispatchService(t, denied, "settings", domainpluginabi.SettingsMethodRead,
			`{"key":"app.locale"}`),
	}
	for _, envelope := range denials {
		if envelope.Code != "denied" || envelope.Permission == "" {
			t.Fatalf("ungranted service call = %+v, want a typed denial", envelope)
		}
	}
}
