package sync

import (
	"testing"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func hostsPayload(count int) Payload {
	hosts := make([]domainvault.Host, 0, count)
	for i := 0; i < count; i++ {
		hosts = append(hosts, mergeTestHost(string(rune('a'+i)), "Host", "host.example"))
	}
	return Payload{Hosts: hosts}
}

func TestShrinkGuardBlocksLargeAbsoluteLoss(t *testing.T) {
	reference := hostsPayload(12)
	outgoing := hostsPayload(2)
	finding := DetectDestructiveSync(outgoing, &reference, nil)
	if !finding.Suspicious() || finding.Kind != GuardShrink || finding.Reason != GuardReasonLargeShrink {
		t.Fatalf("finding = %+v", finding)
	}
	if finding.EntityType != "hosts" || finding.BaseCount != 12 || finding.OutgoingCount != 2 || finding.Lost != 10 {
		t.Fatalf("finding detail = %+v", finding)
	}
}

func TestShrinkGuardBlocksBulkRatioAtThreeOrMore(t *testing.T) {
	reference := hostsPayload(6)
	outgoing := hostsPayload(3)
	finding := DetectDestructiveSync(outgoing, &reference, nil)
	if finding.Kind != GuardShrink || finding.Reason != GuardReasonBulkShrink || finding.Lost != 3 {
		t.Fatalf("finding = %+v", finding)
	}
}

func TestShrinkGuardAllowsSmallOrdinaryDeletes(t *testing.T) {
	cases := []struct {
		from int
		to   int
	}{
		{2, 1},    // losing one of two is an ordinary edit
		{5, 3},    // losing two of five is below the absolute minimum
		{100, 95}, // losing five is below the large-loss and ratio gates
	}
	for _, test := range cases {
		reference := hostsPayload(test.from)
		outgoing := hostsPayload(test.to)
		if finding := DetectDestructiveSync(outgoing, &reference, nil); finding.Suspicious() {
			t.Errorf("%d -> %d blocked: %+v", test.from, test.to, finding)
		}
	}
}

func TestShrinkGuardGroupSpecialCase(t *testing.T) {
	reference := Payload{
		Hosts:  hostsPayload(2).Hosts,
		Groups: []domainvault.Group{mergeTestGroup("team", "")},
	}
	outgoing := Payload{Hosts: hostsPayload(2).Hosts}
	finding := DetectDestructiveSync(outgoing, &reference, nil)
	if finding.Kind != GuardShrink || finding.EntityType != "groups" || finding.Reason != GuardReasonBulkShrink {
		t.Fatalf("finding = %+v", finding)
	}
	// An explicitly empty whole Vault stays a real deletion for a small vault:
	// the tombstone rule takes over instead.
	outgoing = Payload{}
	finding = DetectDestructiveSync(outgoing, &reference, nil)
	if finding.Kind != GuardTombstone {
		t.Fatalf("full wipe finding = %+v", finding)
	}
}

func TestShrinkGuardUsesTheRemoteWhenNoBaseExists(t *testing.T) {
	remote := hostsPayload(10)
	finding := DetectDestructiveSync(hostsPayload(1), nil, &remote)
	if !finding.Suspicious() || !finding.ViaRemote {
		t.Fatalf("finding = %+v", finding)
	}
	// With no reference at all there is nothing to protect.
	if finding := DetectDestructiveSync(hostsPayload(0), nil, nil); finding.Suspicious() {
		t.Fatalf("first upload finding = %+v", finding)
	}
}

func TestTombstoneGuardBlocksAWholeVaultDeletion(t *testing.T) {
	reference := hostsPayload(2)
	finding := DetectDestructiveSync(Payload{}, &reference, nil)
	if finding.Kind != GuardTombstone || finding.Reason != GuardReasonTombstone {
		t.Fatalf("finding = %+v", finding)
	}
	if finding.BaseCount != 2 || finding.Lost != 2 {
		t.Fatalf("finding detail = %+v", finding)
	}
}

func TestTombstoneGuardFallsBackToTheRemoteWhenTheAnchorIsEmpty(t *testing.T) {
	emptyBase := Payload{}
	remote := hostsPayload(2)
	finding := DetectDestructiveSync(Payload{}, &emptyBase, &remote)
	if finding.Kind != GuardTombstone || !finding.ViaRemote || finding.BaseCount != 2 {
		t.Fatalf("empty anchor bypassed the guard: %+v", finding)
	}
	// A populated anchor stays authoritative even when the cloud contains
	// different data: it is the shared base the merge is computed against.
	base := hostsPayload(4)
	if finding := DetectDestructiveSync(hostsPayload(1), &base, &remote); finding.ViaRemote {
		t.Fatalf("a populated anchor was replaced by the remote: %+v", finding)
	}
	// Nothing populated anywhere means there is no data to protect.
	if finding := DetectDestructiveSync(Payload{}, &emptyBase, nil); finding.Suspicious() {
		t.Fatalf("empty Vault with no remote finding = %+v", finding)
	}
}

func TestShrinkGuardProtectsNetworkProfiles(t *testing.T) {
	profiles := func(count int) []domainvault.NetworkProfile {
		values := make([]domainvault.NetworkProfile, 0, count)
		for i := 0; i < count; i++ {
			values = append(values, mergeTestNetworkProfile(
				"net-"+string(rune('a'+i)), domainvault.NetworkKindWireGuard))
		}
		return values
	}
	reference := Payload{NetworkProfiles: profiles(12)}
	outgoing := Payload{NetworkProfiles: profiles(2)}
	finding := DetectDestructiveSync(outgoing, &reference, nil)
	if finding.Kind != GuardShrink || finding.Reason != GuardReasonLargeShrink ||
		finding.EntityType != "networkProfiles" {
		t.Fatalf("finding = %+v", finding)
	}
	// Network Profiles count as Vault data for the whole-vault tombstone rule.
	if finding := DetectDestructiveSync(Payload{}, &reference, nil); finding.Kind != GuardTombstone {
		t.Fatalf("tombstone finding = %+v", finding)
	}
}

func TestTombstoneGuardDoesNotFireWhenAnyEntitySurvives(t *testing.T) {
	reference := hostsPayload(2)
	outgoing := hostsPayload(1)
	if finding := DetectDestructiveSync(outgoing, &reference, nil); finding.Suspicious() {
		t.Fatalf("a surviving entity is not a tombstone sync: %+v", finding)
	}
}

func dataProfilesPayload(count int) Payload {
	profiles := make([]domainplugin.DataProfile, 0, count)
	for index := 0; index < count; index++ {
		profiles = append(profiles, mergeTestProfile(
			string(rune('a'+index)), "Profile", nil))
	}
	return Payload{DataProfiles: profiles}
}

func TestTombstoneGuardProtectsDataProfiles(t *testing.T) {
	reference := dataProfilesPayload(4)
	finding := DetectDestructiveSync(Payload{}, &reference, nil)
	if finding.Kind != GuardTombstone || finding.BaseCount != 4 || finding.Lost != 4 {
		t.Fatalf("finding = %+v", finding)
	}
}

func TestShrinkGuardProtectsDataProfiles(t *testing.T) {
	reference := dataProfilesPayload(12)
	finding := DetectDestructiveSync(dataProfilesPayload(2), &reference, nil)
	if finding.Kind != GuardShrink || finding.EntityType != "dataProfiles" ||
		finding.Lost != 10 {
		t.Fatalf("finding = %+v", finding)
	}
}
