package vaultimport

import (
	"sort"
	"strings"
)

// resolveCSVJumps binds every CSV ProxyJump cell to records in the same file.
// A cell lists OpenSSH jump hosts ([user@]host[:port], comma separated, first
// hop closest to the client). Unlike ssh_config, an undeclared hop is not
// synthesized: the row itself is rejected, because silently importing a Host
// without the chain it asked for changes where its connection terminates. A
// row whose hop was rejected for its own ProxyJump is rejected too, so no
// surviving Host points at a Host that will not be imported.
func resolveCSVJumps(result *Result, pending map[int]string) {
	if len(pending) == 0 {
		return
	}

	rejected := make(map[int]bool, len(pending))
	refs := make(map[int][]int, len(pending))
	indexes := make([]int, 0, len(pending))
	for index := range pending {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		bound, badHop, ok := bindCSVJumpChain(result.Records, index, pending[index])
		if !ok {
			result.Issues = append(result.Issues,
				unresolvedJumpIssue(result.Records[index], badHop))
			rejected[index] = true
			continue
		}
		refs[index] = bound
	}

	ordered := make([]int, 0, len(refs))
	for index := range refs {
		ordered = append(ordered, index)
	}
	sort.Ints(ordered)
	for changed := true; changed; {
		changed = false
		for _, index := range ordered {
			if rejected[index] {
				continue
			}
			for _, ref := range refs[index] {
				if !rejected[ref] {
					continue
				}
				result.Issues = append(result.Issues,
					unresolvedJumpIssue(result.Records[index], result.Records[ref].Hostname))
				rejected[index] = true
				changed = true
				break
			}
		}
	}

	remap := make(map[int]int, len(result.Records))
	survivors := make([]Record, 0, len(result.Records))
	for index := range result.Records {
		if rejected[index] {
			continue
		}
		remap[index] = len(survivors)
		survivors = append(survivors, result.Records[index])
	}
	if len(rejected) > 0 {
		result.Records = survivors
	}
	for index, targets := range refs {
		if rejected[index] {
			continue
		}
		record := &result.Records[remap[index]]
		record.JumpRefs = nil
		for _, ref := range targets {
			if mapped, ok := remap[ref]; ok {
				record.JumpRefs = appendUniqueInt(record.JumpRefs, mapped)
			}
		}
	}
	removeJumpCycles(result)
}

// bindCSVJumpChain resolves one ProxyJump cell to record indexes. badHop names
// the hop that could not be represented or matched.
func bindCSVJumpChain(records []Record, owner int, raw string) ([]int, string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.EqualFold(value, "none") {
		return nil, "", true
	}
	var refs []int
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		hop, ok := parseCSVJumpHop(part)
		if !ok {
			return nil, part, false
		}
		target, found := matchCSVJumpHop(records, owner, hop)
		if !found {
			return nil, part, false
		}
		refs = appendUniqueInt(refs, target)
	}
	return refs, "", true
}

// parseCSVJumpHop reads one OpenSSH jump host. Only SSH-family hops make sense
// in a ProxyJump chain, so a URL naming another transport is rejected.
func parseCSVJumpHop(value string) (Jump, bool) {
	hostname, username, port, protocol, ok := parseTarget(value)
	if !ok || (protocol != "" && protocol != "ssh") {
		return Jump{}, false
	}
	return Jump{
		Username: strings.TrimSpace(username),
		Hostname: hostname,
		Port:     port,
	}, true
}

// matchCSVJumpHop binds a hop to a record by endpoint, then by label. A hop in
// the owning Host's Group wins a tie; file order breaks the rest. Every
// endpoint field the cell states must match, so a hop naming a different user
// never silently binds to a Host with another login.
func matchCSVJumpHop(records []Record, owner int, hop Jump) (int, bool) {
	group := normalizeGroup(records[owner].Group)
	for _, matches := range []func(Record, Jump) bool{
		csvJumpEndpointMatches, csvJumpLabelMatches,
	} {
		first, sameGroup := -1, -1
		for index := range records {
			if !matches(records[index], hop) {
				continue
			}
			if first < 0 {
				first = index
			}
			if sameGroup < 0 && normalizeGroup(records[index].Group) == group {
				sameGroup = index
			}
		}
		if sameGroup >= 0 {
			return sameGroup, true
		}
		if first >= 0 {
			return first, true
		}
	}
	return 0, false
}

func csvJumpEndpointMatches(record Record, hop Jump) bool {
	if !canServeAsCSVJump(record.Protocol) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(record.Hostname), hop.Hostname) {
		return false
	}
	return csvJumpFieldsMatch(record, hop)
}

func csvJumpLabelMatches(record Record, hop Jump) bool {
	if !canServeAsCSVJump(record.Protocol) {
		return false
	}
	label := strings.ToLower(strings.TrimSpace(record.Label))
	if label == "" || label != strings.ToLower(hop.Hostname) {
		return false
	}
	return csvJumpFieldsMatch(record, hop)
}

func csvJumpFieldsMatch(record Record, hop Jump) bool {
	if csvJumpPort(record.Port) != csvJumpPort(hop.Port) {
		return false
	}
	if hop.Username != "" &&
		!strings.EqualFold(strings.TrimSpace(record.Username), hop.Username) {
		return false
	}
	return true
}

// canServeAsCSVJump reports whether a record can be dialed as a jump host.
// Mosh and ET sessions bootstrap over SSH, while telnet, serial, local shells,
// and plugin transports cannot carry a ProxyJump hop.
func canServeAsCSVJump(protocol string) bool {
	switch protocol {
	case "", "ssh", "mosh", "et":
		return true
	default:
		return false
	}
}

// csvJumpPort treats an omitted or out-of-range port as the SSH default, the
// same reading the CSV writer uses when it omits port 22.
func csvJumpPort(port int) int {
	if port >= 1 && port <= 65535 {
		return port
	}
	return 22
}

func unresolvedJumpIssue(record Record, hop string) Issue {
	return Issue{
		Severity: SeverityError, Code: IssueUnresolvedJump,
		Row: record.Row, Field: "proxyjump", Value: hop,
	}
}
