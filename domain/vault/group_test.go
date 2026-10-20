package vault

import (
	"testing"
)

func TestNormalizeGroupDraftCanonicalizesAndValidates(t *testing.T) {
	draft, err := NormalizeGroupDraft(GroupDraft{
		Path:       "  prod\\ web//db ",
		Username:   " deploy ",
		Port:       2222,
		Protocol:   " SSH ",
		AuthMethod: "KEY",
	})
	if err != nil {
		t.Fatalf("NormalizeGroupDraft: %v", err)
	}
	if draft.Path != "prod/web/db" {
		t.Fatalf("Path = %q, want prod/web/db", draft.Path)
	}
	if draft.Username != "deploy" || draft.Port != 2222 ||
		draft.Protocol != ProtocolSSH || draft.AuthMethod != HostAuthKey {
		t.Fatalf("normalized draft = %+v", draft)
	}

	cases := []struct {
		name  string
		draft GroupDraft
		field Field
		rule  Rule
	}{
		{"missing path", GroupDraft{Path: "   "}, FieldGroup, RuleRequired},
		{"bad protocol", GroupDraft{Path: "prod", Protocol: "serial"}, FieldProtocol, RuleUnsupported},
		{"bad port", GroupDraft{Path: "prod", Port: 70000}, FieldPort, RuleOutOfRange},
		{"bad method", GroupDraft{Path: "prod", AuthMethod: "magic"}, FieldAuthMethod, RuleUnsupported},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeGroupDraft(test.draft)
			var invalid *ValidationError
			if !groupAsValidation(err, &invalid) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if invalid.Field != test.field || invalid.Rule != test.rule {
				t.Fatalf("error = %+v, want %s/%s", invalid, test.field, test.rule)
			}
		})
	}
}

func TestResolveGroupDefaultsMergesAncestorsChildLast(t *testing.T) {
	groups := []Group{
		{Path: "prod", Username: "deploy", Port: 2222, IdentityID: "identity-parent"},
		{Path: "prod/web", Username: "www", ProxyProfileID: "proxy-web"},
		{Path: "prod/web/internal", KeyID: "key-internal"},
	}

	got := ResolveGroupDefaults(groups, "prod/web/internal")
	if got.Username != "www" || got.Port != 2222 {
		t.Fatalf("defaults = %+v, want child username and parent port", got)
	}
	if got.IdentityID != "" || got.KeyID != "key-internal" {
		t.Fatalf("identity/key defaults = %+v, want the child's credentials to replace the parent identity", got)
	}
	if got.ProxyProfileID != "proxy-web" {
		t.Fatalf("ProxyProfileID = %q, want proxy-web", got.ProxyProfileID)
	}

	// A sibling outside prod/web does not see that child's defaults.
	sibling := ResolveGroupDefaults(groups, "prod/db")
	if sibling.Username != "deploy" || sibling.ProxyProfileID != "" {
		t.Fatalf("sibling defaults = %+v", sibling)
	}
}

func TestApplyGroupDefaultsHostWinsAndLocalShellsDoNotInherit(t *testing.T) {
	defaults := GroupDefaults{
		Username:         "deploy",
		Port:             2222,
		AuthMethod:       HostAuthKey,
		IdentityID:       "identity-1",
		ProxyProfileID:   "proxy-1",
		LegacyAlgorithms: true,
		Algorithms:       &AlgorithmOverrides{Ciphers: []string{"aes128-ctr"}},
	}

	host := Host{
		Protocol: ProtocolSSH,
		Port:     22,
		Username: "",
	}
	effective := ApplyGroupDefaults(host, defaults)
	if effective.Username != "deploy" || effective.IdentityID != "identity-1" {
		t.Fatalf("effective = %+v, want inherited identity and username", effective)
	}
	if effective.ProxyProfileID != "proxy-1" || !effective.LegacyAlgorithms {
		t.Fatalf("effective = %+v, want inherited proxy and legacy", effective)
	}
	if effective.Port != 22 {
		t.Fatalf("Port = %d, want the Host's 22 to win", effective.Port)
	}

	own := Host{
		Protocol:       ProtocolSSH,
		Port:           2200,
		Username:       "root",
		IdentityID:     "identity-own",
		ProxyProfileID: "proxy-own",
		Algorithms:     &AlgorithmOverrides{Ciphers: []string{"aes256-ctr"}},
	}
	effective = ApplyGroupDefaults(own, defaults)
	if effective.Username != "root" || effective.IdentityID != "identity-own" ||
		effective.ProxyProfileID != "proxy-own" {
		t.Fatalf("effective = %+v, want every Host value to win", effective)
	}
	if effective.Algorithms == nil || len(effective.Algorithms.Ciphers) != 1 ||
		effective.Algorithms.Ciphers[0] != "aes256-ctr" {
		t.Fatalf("Algorithms = %+v, want the Host's own", effective.Algorithms)
	}

	local := ApplyGroupDefaults(Host{Protocol: ProtocolLocal}, defaults)
	if local.Username != "" || local.IdentityID != "" || local.ProxyProfileID != "" ||
		local.LegacyAlgorithms {
		t.Fatalf("local shell inherited remote defaults: %+v", local)
	}
}

func TestResolveGroupDefaultsKeepsCredentialBundlesWhole(t *testing.T) {
	identityOverManual := ResolveGroupDefaults([]Group{
		{Path: "prod", Username: "deploy", AuthMethod: HostAuthPassword},
		{Path: "prod/web", IdentityID: "identity-web"},
	}, "prod/web")
	if identityOverManual.IdentityID != "identity-web" ||
		identityOverManual.Username != "" ||
		identityOverManual.AuthMethod != "" {
		t.Fatalf("identity child = %+v, want the identity bundle to replace manual credentials",
			identityOverManual)
	}

	manualOverIdentity := ResolveGroupDefaults([]Group{
		{Path: "prod", IdentityID: "identity-parent", KeyID: "key-parent"},
		{Path: "prod/web", Username: "www"},
	}, "prod/web")
	if manualOverIdentity.IdentityID != "" || manualOverIdentity.KeyID != "" ||
		manualOverIdentity.Username != "www" {
		t.Fatalf("manual child = %+v, want the manual bundle to replace the inherited identity",
			manualOverIdentity)
	}
}

func TestApplyGroupDefaultsKeepsCredentialBundlesWhole(t *testing.T) {
	defaults := GroupDefaults{
		Username:   "deploy",
		AuthMethod: HostAuthPassword,
		KeyID:      "key-group",
		IdentityID: "identity-group",
	}
	ownIdentity := ApplyGroupDefaults(Host{
		Protocol: ProtocolSSH, IdentityID: "identity-own",
	}, defaults)
	if ownIdentity.IdentityID != "identity-own" ||
		ownIdentity.Username != "" ||
		ownIdentity.KeyID != "" ||
		ownIdentity.AuthMethod != "" {
		t.Fatalf("own identity = %+v, want no group credential fields", ownIdentity)
	}

	ownManual := ApplyGroupDefaults(Host{
		Protocol: ProtocolSSH, Username: "root",
	}, defaults)
	if ownManual.Username != "root" || ownManual.IdentityID != "" {
		t.Fatalf("own manual credentials = %+v, want the group identity skipped", ownManual)
	}
}

func TestGroupPathHelpers(t *testing.T) {
	got := GroupAncestors(" prod / web / db ")
	want := []string{"prod", "prod/web", "prod/web/db"}
	if len(got) != len(want) {
		t.Fatalf("ancestors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ancestors = %v, want %v", got, want)
		}
	}
	if GroupName("prod/web/db") != "db" {
		t.Fatalf("GroupName = %q", GroupName("prod/web/db"))
	}
	if !IsGroupAtOrBelow("prod/web/db", "prod/web") ||
		IsGroupAtOrBelow("production", "prod") {
		t.Fatal("IsGroupAtOrBelow used a prefix instead of a path boundary")
	}
	if got := ReplaceGroupPrefix("prod/web/db", "prod/web", "prod/app"); got != "prod/app/db" {
		t.Fatalf("ReplaceGroupPrefix = %q", got)
	}
	if got := ReplaceGroupPrefix("prod/db", "prod/web", "prod/app"); got != "prod/db" {
		t.Fatalf("unrelated ReplaceGroupPrefix = %q", got)
	}
}

func groupAsValidation(err error, target **ValidationError) bool {
	invalid, ok := err.(*ValidationError)
	if ok {
		*target = invalid
	}
	return ok
}
