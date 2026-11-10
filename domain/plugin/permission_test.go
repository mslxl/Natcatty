package plugin

import (
	"strings"
	"testing"
)

func TestPermissionVocabularyIsClosed(t *testing.T) {
	for _, name := range []string{
		"commands", "views", "settings.contribute", "mcp.tools", "ui.native",
		"clipboard.write", "hosts.read", "terminal.observe", "vault.secrets",
		"files.read", "network.dial", "events.subscribe", "provider.data",
		"services.invoke", "services.provide", "auth.invoke", "trust.verify",
	} {
		if !IsKnownPermission(name) {
			t.Errorf("%q is missing from the closed vocabulary", name)
		}
	}
	for _, name := range []string{"network", "provider.authentication", "companion.execute",
		"filesystem.read", "unknown", ""} {
		if IsKnownPermission(name) {
			t.Errorf("%q should not be a permission", name)
		}
	}
}

func TestPermissionRequestsRejectUnknownAndDuplicateNames(t *testing.T) {
	tests := []struct {
		name     string
		required []string
		optional []string
	}{
		{"unknown", []string{"commands", "provider.authentication"}, nil},
		{"duplicate across lists", []string{"commands"}, []string{"commands"}},
		{"duplicate within a list", []string{"commands", "commands"}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			permissions := Permissions{Required: test.required, Optional: test.optional}
			if _, _, err := permissions.Requests(); err == nil {
				t.Fatal("invalid permission list was accepted")
			}
		})
	}
}

func TestPermissionScopesMustMatchThePermissionKind(t *testing.T) {
	permissions := Permissions{
		Required: []string{"files.read", "network.dial"},
		Scopes: map[string]Scope{
			"files.read": {Path: "/tmp/notes"},
			"network.dial": {
				Host: "example.com", Ports: []int{443}, Protocol: "tls", TLS: "verify",
			},
		},
	}
	required, _, err := permissions.Requests()
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}
	if required[0].Scope.Kind != ScopeFiles || required[1].Scope.Kind != ScopeNetwork {
		t.Fatalf("scope kinds = %q/%q", required[0].Scope.Kind, required[1].Scope.Kind)
	}
}

func TestPermissionScopeValidationRejectsWildcardsAndEscapes(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
	}{
		{"hostname wildcard", Scope{Kind: ScopeHost, Host: "*"}},
		{"path wildcard", Scope{Kind: ScopeFiles, Path: "/tmp/*"}},
		{"path traversal", Scope{Kind: ScopeFiles, Path: "/tmp/../etc"}},
		{"relative path", Scope{Kind: ScopeFiles, Path: "notes"}},
		{"network wildcard host", Scope{Kind: ScopeNetwork, Host: "*",
			Ports: []int{443}, Protocol: "tls", TLS: "verify"}},
		{"network missing ports", Scope{Kind: ScopeNetwork, Host: "example.com",
			Protocol: "tcp"}},
		{"tls without policy", Scope{Kind: ScopeNetwork, Host: "example.com",
			Ports: []int{443}, Protocol: "tls"}},
		{"unknown access", Scope{Kind: ScopeProvider, Provider: "acme.db", Access: "admin"}},
		{"service wildcard", Scope{Kind: ScopeServices, Service: "plugin.acme.*",
			Access: "read"}},
		{"host scope with a path", Scope{Kind: ScopeHost, Host: "host-1", Path: "/tmp"}},
		{"session scope with a host", Scope{Kind: ScopeSession, Session: "session-1",
			Host: "host-1"}},
		{"files scope with ports", Scope{Kind: ScopeFiles, Path: "/tmp", Ports: []int{443}}},
		{"provider scope with a protocol", Scope{Kind: ScopeProvider, Provider: "acme.db",
			Access: AccessReadOnly, Protocol: "tcp"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.scope.Validate(); err == nil {
				t.Fatalf("scope %+v was accepted", test.scope)
			}
		})
	}
}

func TestPermissionScopeValidationAcceptsExactResources(t *testing.T) {
	scopes := []Scope{
		{Kind: ScopeHost, Host: "host-1"},
		{Kind: ScopeSession, Session: "session-1"},
		{Kind: ScopeVault, Host: "host-1", Field: "password"},
		{Kind: ScopeFiles, Path: "/Users/me/notes"},
		{Kind: ScopeFiles, Path: "C:\\Users\\me\\notes"},
		{Kind: ScopeNetwork, Host: "example.com", Ports: []int{22, 443},
			Protocol: "tls", TLS: "system"},
		{Kind: ScopeEvents, Event: "session.changed"},
		{Kind: ScopeSettings, Key: "terminal.font"},
		{Kind: ScopeProvider, Provider: "acme.db", Access: AccessReadOnly},
		{Kind: ScopeServices, Service: "plugin.acme.db.query", Provider: "acme.db",
			Version: "^1.2", Access: ServiceAccessRead},
	}
	for _, scope := range scopes {
		if err := scope.Validate(); err != nil {
			t.Errorf("scope %+v: %v", scope, err)
		}
	}
}

func TestManifestRejectsUnknownPermissions(t *testing.T) {
	manifest := validManifest()
	manifest.Permissions.Required = []string{"commands", "provider.authentication"}
	err := manifest.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown permission") {
		t.Fatalf("Validate = %v, want an unknown-permission error", err)
	}
}

func TestManifestRequiresADeclaredScopeForResourceScopedPermissions(t *testing.T) {
	manifest := validManifest()
	manifest.Permissions.Optional = []string{"files.read"}
	err := manifest.Validate()
	if err == nil || !strings.Contains(err.Error(), "requires a structured scope") {
		t.Fatalf("Validate = %v, want a missing-scope error", err)
	}
}

func TestManifestRejectsAScopeForAnUndeclaredPermission(t *testing.T) {
	manifest := validManifest()
	manifest.Permissions.Scopes = map[string]Scope{
		"files.read": {Path: "/tmp/notes"},
	}
	err := manifest.Validate()
	if err == nil || !strings.Contains(err.Error(), "neither required nor optional") {
		t.Fatalf("Validate = %v, want an undeclared-scope error", err)
	}
}
