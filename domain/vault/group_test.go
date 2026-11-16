package vault

import (
	"errors"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// Assigning several Hosts at once is the drop of a marquee selection: every
// Host gets the group, keeps its own fields, and an invalid member fails the
// whole assignment instead of half-applying it.
func TestAssignHostsGroupAssignsEveryHostOrNone(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	hosts := []Host{
		{ID: "host-1", Label: "web-1", Hostname: "10.0.0.1", GroupID: "old", Group: "dev"},
		{ID: "host-2", Label: "web-2", Hostname: "10.0.0.2", Username: "deploy"},
	}
	assigned, err := AssignHostsGroup(hosts, "g-prod", at)
	if err != nil {
		t.Fatalf("AssignHostsGroup: %v", err)
	}
	if len(assigned) != len(hosts) {
		t.Fatalf("assigned %d Hosts, want %d", len(assigned), len(hosts))
	}
	for index, host := range assigned {
		if host.GroupID != "g-prod" || host.Group != "" {
			t.Fatalf("Host %d membership = %q/%q, want the group ID only", index, host.GroupID, host.Group)
		}
		if !host.UpdatedAt.Equal(at) {
			t.Fatalf("Host %d UpdatedAt = %v, want %v", index, host.UpdatedAt, at)
		}
	}
	if assigned[1].Username != "deploy" || assigned[0].Hostname != "10.0.0.1" {
		t.Fatalf("assignment changed other Host fields: %+v", assigned)
	}
	if hosts[0].GroupID != "old" || hosts[1].GroupID != "" {
		t.Fatalf("assignment mutated its input: %+v", hosts)
	}

	root, err := AssignHostsGroup(hosts, "", at)
	if err != nil {
		t.Fatalf("AssignHostsGroup root: %v", err)
	}
	if root[0].GroupID != "" || root[1].GroupID != "" {
		t.Fatalf("root assignment left a group: %+v", root)
	}
}

func TestAssignHostsGroupRejectsAHostWithoutIdentityOrADuplicate(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	if _, err := AssignHostsGroup([]Host{{Label: "no id"}}, "g-prod", at); err == nil {
		t.Fatal("AssignHostsGroup accepted a Host with no identity")
	} else {
		var invalid *ValidationError
		if !errors.As(err, &invalid) || invalid.Field != FieldGroup {
			t.Fatalf("error = %v, want a FieldGroup validation error", err)
		}
	}
	duplicate := []Host{{ID: "host-1"}, {ID: "host-1"}}
	if _, err := AssignHostsGroup(duplicate, "g-prod", at); err == nil {
		t.Fatal("AssignHostsGroup accepted the same Host twice")
	}
}

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

func TestGroupJumpChainDefaultIsInheritedAsOneValue(t *testing.T) {
	groups := []Group{
		{Path: "prod", HostChain: &HostChain{HostIDs: []model.HostID{"bastion"}}},
		{Path: "prod/web", HostChain: &HostChain{HostIDs: []model.HostID{"edge", "bastion"}}},
		{Path: "prod/db"},
	}
	child := ResolveGroupDefaults(groups, "prod/web")
	if chain := child.HostChain; chain == nil || len(chain.HostIDs) != 2 ||
		chain.HostIDs[0] != "edge" || chain.HostIDs[1] != "bastion" {
		t.Fatalf("child chain = %+v, want the child's edge then bastion", chain)
	}
	sibling := ResolveGroupDefaults(groups, "prod/db")
	if chain := sibling.HostChain; chain == nil || len(chain.HostIDs) != 1 ||
		chain.HostIDs[0] != "bastion" {
		t.Fatalf("sibling chain = %+v, want the parent's bastion", chain)
	}

	// A Host's own hops replace the inherited chain whole.
	own := ApplyGroupDefaults(Host{
		Protocol:  ProtocolSSH,
		HostChain: &HostChain{HostIDs: []model.HostID{"own-hop"}},
	}, child)
	if chain := own.HostChain; chain == nil || len(chain.HostIDs) != 1 ||
		chain.HostIDs[0] != "own-hop" {
		t.Fatalf("own chain = %+v, want only own-hop", chain)
	}
	inherited := ApplyGroupDefaults(Host{Protocol: ProtocolSSH}, child)
	if chain := inherited.HostChain; chain == nil || len(chain.HostIDs) != 2 {
		t.Fatalf("inherited chain = %+v, want the group's two hops", chain)
	}
	// The resolved chain must not alias the group's stored slice.
	inherited.HostChain.HostIDs[0] = "mutated"
	if groups[1].HostChain.HostIDs[0] != "edge" {
		t.Fatal("ApplyGroupDefaults aliased the group's chain slice")
	}
	if local := ApplyGroupDefaults(Host{Protocol: ProtocolLocal}, child); local.HostChain != nil {
		t.Fatalf("a Local shell inherited a jump chain: %+v", local.HostChain)
	}
}

func TestNormalizeGroupDraftNormalizesAndValidatesTheJumpChain(t *testing.T) {
	draft, err := NormalizeGroupDraft(GroupDraft{
		Path:      " prod/web ",
		HostChain: &HostChain{HostIDs: []model.HostID{" edge ", "bastion"}},
	})
	if err != nil {
		t.Fatalf("NormalizeGroupDraft: %v", err)
	}
	if chain := draft.HostChain; chain == nil || chain.HostIDs[0] != "edge" {
		t.Fatalf("chain = %+v, want trimmed hops", chain)
	}
	empty, err := NormalizeGroupDraft(GroupDraft{
		Path: "prod", HostChain: &HostChain{},
	})
	if err != nil {
		t.Fatalf("NormalizeGroupDraft with an empty chain: %v", err)
	}
	if empty.HostChain != nil {
		t.Fatalf("empty chain = %+v, want nil to inherit", empty.HostChain)
	}
	for _, hops := range [][]model.HostID{
		{"bastion", "bastion"},
		{""},
	} {
		_, err := NormalizeGroupDraft(GroupDraft{
			Path: "prod", HostChain: &HostChain{HostIDs: hops},
		})
		var invalid *ValidationError
		if !groupAsValidation(err, &invalid) ||
			invalid.Field != FieldChain || invalid.Rule != RuleInvalid {
			t.Fatalf("chain %v error = %v, want chain/invalid", hops, err)
		}
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

// Ticket 48: a Host Group's X11 default applies to member SSH Hosts. The
// nearest group that sets it wins, and a Host that owns an explicit off keeps
// that off instead of inheriting the group's on.
func TestGroupDefaultsInheritX11Forwarding(t *testing.T) {
	groups := []Group{
		{Path: "prod", X11Forwarding: true, X11WarningAcknowledged: true},
		{Path: "prod/web"},
	}
	defaults := ResolveGroupDefaults(groups, "prod/web")
	if !defaults.X11Enabled() {
		t.Fatalf("defaults = %+v, want the ancestor's X11 forwarding", defaults)
	}

	unset := ApplyGroupDefaults(Host{Protocol: ProtocolSSH, Group: "prod/web"}, defaults)
	if !unset.X11Enabled() {
		t.Fatalf("unset Host = %+v, want inherited X11 forwarding", unset)
	}
	if !unset.X11ForwardingSet {
		t.Fatal("the inherited value must resolve exactly as an owned on would")
	}

	// An explicit off is a Host value, so it wins over the group default.
	off := ApplyGroupDefaults(Host{
		Protocol: ProtocolSSH, Group: "prod/web",
		X11Forwarding: false, X11ForwardingSet: true,
	}, defaults)
	if off.X11Enabled() || !off.X11ForwardingSet {
		t.Fatalf("explicit off = %+v, want the Host's off to win", off)
	}

	// A stored true predates the marker but still means the user chose it.
	legacyOn := ApplyGroupDefaults(Host{
		Protocol: ProtocolSSH, X11Forwarding: true, X11WarningAcknowledged: true,
	}, GroupDefaults{})
	if !legacyOn.X11Enabled() {
		t.Fatalf("legacy on = %+v, want it preserved without a group", legacyOn)
	}

	// X11 runs over SSH only: another transport never inherits it.
	mosh := ApplyGroupDefaults(Host{Protocol: ProtocolMosh, Group: "prod/web"}, defaults)
	if mosh.X11Forwarding || mosh.X11ForwardingSet {
		t.Fatalf("Mosh Host inherited X11: %+v", mosh)
	}
	local := ApplyGroupDefaults(Host{Protocol: ProtocolLocal, Group: "prod/web"}, defaults)
	if local.X11Forwarding || local.X11ForwardingSet {
		t.Fatalf("Local shell inherited X11: %+v", local)
	}
}

func TestGroupX11ForwardingRequiresSSHAndAcknowledgement(t *testing.T) {
	if _, err := NormalizeGroupDraft(GroupDraft{
		Path: "prod", Protocol: ProtocolSSH, X11Forwarding: true,
	}); err == nil {
		t.Fatal("a group turned X11 on without the acknowledgement")
	} else {
		var invalid *ValidationError
		if !errors.As(err, &invalid) || invalid.Field != FieldX11 ||
			invalid.Rule != RuleAcknowledgementRequired {
			t.Fatalf("error = %v, want FieldX11 acknowledgement required", err)
		}
	}
	// An explicit non-SSH protocol has no X11 control, so the default is
	// dropped rather than saved where no member could use it.
	telnet, err := NormalizeGroupDraft(GroupDraft{
		Path: "prod", Protocol: ProtocolTelnet,
		X11Forwarding: true, X11WarningAcknowledged: true,
	})
	if err != nil || telnet.X11Forwarding || telnet.X11WarningAcknowledged {
		t.Fatalf("Telnet group draft = %+v, %v; want X11 dropped", telnet, err)
	}
	draft, err := NormalizeGroupDraft(GroupDraft{
		Path: "prod", Protocol: ProtocolSSH,
		X11Forwarding: true, X11WarningAcknowledged: true,
	})
	if err != nil || !draft.X11Forwarding || !draft.X11WarningAcknowledged {
		t.Fatalf("valid group draft = %+v, %v", draft, err)
	}
	// A group with a blank protocol inherits SSH and may set the default; the
	// transport check happens when the member Host resolves it.
	if _, err := NormalizeGroupDraft(GroupDraft{
		Path: "prod", X11Forwarding: true, X11WarningAcknowledged: true,
	}); err != nil {
		t.Fatalf("blank-protocol group rejected X11: %v", err)
	}
}
