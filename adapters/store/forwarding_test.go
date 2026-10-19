package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

func forwardingRule(id string, order int64) domainforward.Rule {
	disabled := false
	disabledReconnect := false
	return domainforward.Rule{
		ID: model.ForwardingRuleID(id), Label: id,
		Type: domainforward.TypeLocal, BindAddress: "127.0.0.1",
		LocalPort: 8080, RemoteHost: "db.internal", RemotePort: 5432,
		HostID: "host-1", OrderKey: order,
		AutoStart: true, AutoReconnect: &disabledReconnect, Enabled: &disabled,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
	}
}

func TestForwardingRulesRoundTripAndPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "natcatty.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rule := forwardingRule("rule-1", 1000)
	if err := first.SaveForwardingRule(context.Background(), rule); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := first.GetForwardingRule(context.Background(), rule.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Label != rule.Label || loaded.Type != rule.Type ||
		loaded.BindAddress != rule.BindAddress || loaded.LocalPort != rule.LocalPort ||
		loaded.RemoteHost != rule.RemoteHost || loaded.RemotePort != rule.RemotePort ||
		loaded.HostID != rule.HostID || loaded.OrderKey != rule.OrderKey {
		t.Fatalf("loaded = %+v, want %+v", loaded, rule)
	}
	if loaded.AutoReconnect == nil || *loaded.AutoReconnect {
		t.Fatalf("AutoReconnect = %v, want explicit false", loaded.AutoReconnect)
	}
	if loaded.Enabled == nil || *loaded.Enabled {
		t.Fatalf("Enabled = %v, want explicit false", loaded.Enabled)
	}

	rows, err := first.db.Query(context.Background(), `SELECT data FROM forwarding_rules WHERE id = ?`, string(rule.ID))
	if err != nil {
		t.Fatalf("query data: %v", err)
	}
	data := rows.Values[0][0].(string)
	if strings.Contains(data, `"status"`) || strings.Contains(data, `"error"`) {
		t.Fatalf("stored rule contains runtime state: %s", data)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	rules, err := second.ListForwardingRules(context.Background())
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != rule.ID {
		t.Fatalf("rules after reopen = %+v", rules)
	}
}

func TestForwardingRulesListOrderDeleteAndMissing(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	second := forwardingRule("second", 2000)
	first := forwardingRule("first", 1000)
	for _, rule := range []domainforward.Rule{second, first} {
		if err := store.SaveForwardingRule(ctx, rule); err != nil {
			t.Fatalf("Save %s: %v", rule.ID, err)
		}
	}
	rules, err := store.ListForwardingRules(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 2 || rules[0].ID != "first" || rules[1].ID != "second" {
		t.Fatalf("order = %+v, want first then second", rules)
	}

	if err := store.DeleteForwardingRule(ctx, first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.DeleteForwardingRule(ctx, first.ID); !errors.Is(err, appforwarding.ErrNotFound) {
		t.Fatalf("second delete = %v, want not found", err)
	}
	if _, err := store.GetForwardingRule(ctx, first.ID); !errors.Is(err, appforwarding.ErrNotFound) {
		t.Fatalf("Get after delete = %v, want not found", err)
	}
}

func TestForwardingRulesSanitizeMalformedStoredRecords(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	insertRaw := func(id, data string) {
		t.Helper()
		if _, err := store.db.Execute(ctx, `INSERT INTO forwarding_rules
			(id, label, type, host_id, bind_address, local_port, order_key, created_at, updated_at, data)
			VALUES (?, '', '', '', '', 0, 0, 0, 0, ?)`, id, data); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	insertRaw("bad-json", `{not json`)
	insertRaw("bad-type", `{"id":"bad-type","label":"x","type":"tunnel","hostId":"h","localPort":1}`)
	insertRaw("no-id", `{"label":"x","type":"local","hostId":"h","localPort":1,"remoteHost":"r","remotePort":2}`)

	stale := forwardingRule("stale", 1000)
	staleData, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	var staleMap map[string]any
	if err := json.Unmarshal(staleData, &staleMap); err != nil {
		t.Fatal(err)
	}
	staleMap["status"] = "active"
	staleMap["error"] = "stale runtime failure"
	staleData, err = json.Marshal(staleMap)
	if err != nil {
		t.Fatal(err)
	}
	insertRaw("stale", string(staleData))

	rules, err := store.ListForwardingRules(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != "stale" {
		t.Fatalf("rules = %+v, want only the usable record", rules)
	}
	if rules[0].Label != "stale" {
		t.Fatalf("loaded = %+v", rules[0])
	}
}

func TestSaveForwardingRuleRejectsInvalidRules(t *testing.T) {
	store, _ := openTestStore(t)
	rule := forwardingRule("invalid", 1000)
	rule.LocalPort = 70000
	if err := store.SaveForwardingRule(context.Background(), rule); err == nil {
		t.Fatal("Save accepted an out-of-range port")
	}
	rule = forwardingRule("", 1000)
	if err := store.SaveForwardingRule(context.Background(), rule); err == nil {
		t.Fatal("Save accepted an empty id")
	}
}
