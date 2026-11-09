package vault

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestNormalizeGroupDraftDropsSSHDefaultsOnlyForExplicitNonSSHProtocols(t *testing.T) {
	// A group that explicitly selects Telnet never reads its members' SSH
	// bundle, so a draft left over from an SSH group must not persist it. A
	// blank protocol still inherits from the parent group and keeps the
	// defaults untouched.
	credentialed := GroupDraft{
		Path: "prod", AuthMethod: HostAuthKey,
		IdentityID:  model.IdentityID("identity-1"),
		KeyID:       model.KeyID("key-1"),
		UseSSHAgent: true, RequiresMFA: true,
	}
	telnet := credentialed
	telnet.Protocol = ProtocolTelnet
	normalized, err := NormalizeGroupDraft(telnet)
	if err != nil {
		t.Fatalf("NormalizeGroupDraft telnet: %v", err)
	}
	if normalized.IdentityID != "" || normalized.KeyID != "" || normalized.AuthMethod != "" ||
		normalized.UseSSHAgent || normalized.RequiresMFA {
		t.Fatalf("telnet group kept its SSH defaults: %+v", normalized)
	}

	inherited, err := NormalizeGroupDraft(credentialed)
	if err != nil {
		t.Fatalf("NormalizeGroupDraft blank protocol: %v", err)
	}
	if inherited.IdentityID != "identity-1" || inherited.KeyID != "key-1" ||
		inherited.AuthMethod != HostAuthKey || !inherited.UseSSHAgent || !inherited.RequiresMFA {
		t.Fatalf("blank-protocol group lost its defaults: %+v", inherited)
	}
}

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
		// A group exists before a Host can belong to it; inheritance follows
		// this node's ParentID chain.
		{Path: "prod/db"},
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

func TestParentGroupPathDropsTheLastSegment(t *testing.T) {
	for _, test := range []struct {
		path string
		want string
	}{
		{"prod/web/db", "prod/web"},
		{"prod/web", "prod"},
		{"prod", ""},
		{"", ""},
		{" prod / web / db ", "prod/web"},
		{"prod//web", "prod"},
	} {
		if got := ParentGroupPath(test.path); got != test.want {
			t.Errorf("ParentGroupPath(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestNetworkProfileReferenceFollowsProxyProfileMergingRules(t *testing.T) {
	// The Host editor trims the soft reference like every other selector, and a
	// saved Host keeps it through Validate.
	host, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "10.0.0.2", NetworkProfileID: "  net-host  ",
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if host.NetworkProfileID != "net-host" {
		t.Fatalf("Host NetworkProfileID = %q, want net-host", host.NetworkProfileID)
	}
	stored := Host{Label: "web", Hostname: "10.0.0.2", Protocol: ProtocolSSH,
		NetworkProfileID: host.NetworkProfileID}
	if err := stored.Validate(); err != nil {
		t.Fatalf("Host.Validate: %v", err)
	}

	groupDraft, err := NormalizeGroupDraft(GroupDraft{
		Path: "prod", NetworkProfileID: "  net-group  ",
	})
	if err != nil {
		t.Fatalf("NormalizeGroupDraft: %v", err)
	}
	if groupDraft.NetworkProfileID != "net-group" {
		t.Fatalf("Group NetworkProfileID = %q, want net-group", groupDraft.NetworkProfileID)
	}

	groups := []Group{
		{Path: "prod", NetworkProfileID: "net-parent"},
		{Path: "prod/web", NetworkProfileID: "net-child"},
		{Path: "prod/db"},
	}
	if got := ResolveGroupDefaults(groups, "prod/web").NetworkProfileID; got != "net-child" {
		t.Fatalf("child default = %q, want net-child", got)
	}
	if got := ResolveGroupDefaults(groups, "prod/db").NetworkProfileID; got != "net-parent" {
		t.Fatalf("sibling default = %q, want the parent's net-parent", got)
	}

	defaults := ResolveGroupDefaults(groups, "prod/web")
	own := ApplyGroupDefaults(Host{
		Protocol: ProtocolSSH, Group: "prod/web", NetworkProfileID: "net-own",
	}, defaults)
	if own.NetworkProfileID != "net-own" {
		t.Fatalf("a Host's own reference lost to the group: %q", own.NetworkProfileID)
	}
	inherited := ApplyGroupDefaults(Host{Protocol: ProtocolSSH, Group: "prod/web"}, defaults)
	if inherited.NetworkProfileID != "net-child" {
		t.Fatalf("inherited reference = %q, want net-child", inherited.NetworkProfileID)
	}
	local := ApplyGroupDefaults(Host{Protocol: ProtocolLocal, Group: "prod/web"}, defaults)
	if local.NetworkProfileID != "" {
		t.Fatalf("a Local shell inherited a Network Profile: %q", local.NetworkProfileID)
	}
}

func groupAsValidation(err error, target **ValidationError) bool {
	invalid, ok := err.(*ValidationError)
	if ok {
		*target = invalid
	}
	return ok
}

func TestEnsureGroupAncestorsSynthesizesTheTree(t *testing.T) {
	groups := EnsureGroupAncestors(
		[]Group{{Path: " prod / web "}},
		[]string{"staging/db"},
	)
	byPath := make(map[string]Group, len(groups))
	for _, group := range groups {
		byPath[group.Path] = group
	}

	prod, ok := byPath["prod"]
	if !ok || prod.ID != GroupIDFromPath("prod") || prod.ParentID != "" || prod.Name != "prod" {
		t.Fatalf("prod = %+v, want a root implicit group", prod)
	}
	web, ok := byPath["prod/web"]
	if !ok || web.ParentID != prod.ID || web.Name != "web" {
		t.Fatalf("prod/web = %+v, want a child of prod", web)
	}
	for _, path := range []string{"staging", "staging/db"} {
		if _, ok := byPath[path]; !ok {
			t.Fatalf("implicit ancestor %q is missing: %+v", path, groups)
		}
	}
	if byPath["staging/db"].ParentID != byPath["staging"].ID {
		t.Fatalf("staging/db parent = %q, want %q",
			byPath["staging/db"].ParentID, byPath["staging"].ID)
	}
}

func TestResolveGroupTreeRenamesAndMovesWithoutChangingIdentity(t *testing.T) {
	nodes := []GroupTreeNode{
		{ID: "g-prod", Name: "Prod", OrderKey: 1},
		{ID: "g-web", ParentID: "g-prod", Name: "Web", OrderKey: 2},
	}
	before := ResolveGroupTree(nodes)
	if before["g-prod"] != "Prod" || before["g-web"] != "Prod/Web" {
		t.Fatalf("paths = %v", before)
	}

	// Rename the root and move the child to the Vault root. The IDs are the
	// same records, so no delete/insert is implied.
	nodes[0].Name = "Production"
	nodes[1].ParentID = ""
	after := ResolveGroupTree(nodes)
	if after["g-prod"] != "Production" || after["g-web"] != "Web" {
		t.Fatalf("paths after rename and move = %v", after)
	}
}

func TestResolveGroupTreeIsDeterministicForConflicts(t *testing.T) {
	nodes := []GroupTreeNode{
		{ID: "b", Name: "dup", OrderKey: 1},
		{ID: "a", Name: "dup", OrderKey: 2},
		// Two concurrent moves created a cycle.
		{ID: "c", ParentID: "d", Name: "c"},
		{ID: "d", ParentID: "c", Name: "d"},
		// A dangling parent and a self-link both become roots.
		{ID: "e", ParentID: "missing", Name: "e"},
		{ID: "f", ParentID: "f", Name: "f"},
	}
	paths := ResolveGroupTree(nodes)
	if paths["c"] != "c" || paths["d"] != "c/d" {
		t.Fatalf("cycle paths = %q, %q; want the smallest ID to become the root",
			paths["c"], paths["d"])
	}
	if paths["b"] != "dup" || paths["a"] != "dup (2)" {
		t.Fatalf("duplicate sibling paths = %q, %q", paths["b"], paths["a"])
	}
	if paths["e"] != "e" || paths["f"] != "f" {
		t.Fatalf("dangling and self-parent paths = %q, %q", paths["e"], paths["f"])
	}
}
