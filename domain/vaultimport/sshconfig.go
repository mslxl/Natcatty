package vaultimport

import (
	"strings"
)

// sshConfigBlock is one Host block's accumulated directives.
type sshConfigBlock struct {
	row            int
	patterns       []string
	hostname       string
	username       string
	port           int
	identityFiles  []string
	identityAgent  string
	agentExplicit  bool
	identitiesOnly bool
	addKeysToAgent string
	useKeychain    bool
	forwardX11     bool
	proxyJump      string
}

// parseSSHConfig parses an OpenSSH client configuration. Wildcard and negated
// Host patterns are skipped because the Vault stores concrete Hosts. A
// ProxyJump that names an undeclared Host creates an inline Host row so the
// chain can still be imported.
func parseSSHConfig(text string) Result {
	result := Result{Format: FormatSSHConfig}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	var blocks []sshConfigBlock
	var current *sshConfigBlock
	flush := func() {
		if current != nil {
			blocks = append(blocks, *current)
			current = nil
		}
	}

	for index, line := range lines {
		cleaned := strings.TrimSpace(stripSSHComment(line))
		if cleaned == "" {
			continue
		}
		fields := splitSSHFields(cleaned)
		if len(fields) == 0 {
			continue
		}
		keyword := strings.ToLower(fields[0])
		if keyword == "host" {
			flush()
			current = &sshConfigBlock{row: index + 1, patterns: fields[1:]}
			continue
		}
		if keyword == "match" {
			flush()
			continue
		}
		if current == nil {
			continue
		}
		value := strings.Join(fields[1:], " ")
		if value == "" {
			continue
		}
		switch keyword {
		case "hostname":
			current.hostname = unquoteSSH(value)
		case "user":
			current.username = unquoteSSH(value)
		case "port":
			if port, ok := parsePort(value); ok {
				current.port = port
			}
		case "identityfile":
			current.identityFiles = append(current.identityFiles, unquoteSSH(value))
		case "identityagent":
			current.identityAgent = unquoteSSH(value)
			current.agentExplicit = true
		case "identitiesonly":
			current.identitiesOnly = strings.EqualFold(value, "yes")
		case "addkeystoagent":
			current.addKeysToAgent = strings.ToLower(value)
		case "usekeychain":
			current.useKeychain = strings.EqualFold(value, "yes")
		case "forwardx11":
			current.forwardX11 = strings.EqualFold(value, "yes")
		case "proxyjump":
			current.proxyJump = unquoteSSH(value)
		}
	}
	flush()

	for _, block := range blocks {
		for _, pattern := range block.patterns {
			if pattern == "" || strings.HasPrefix(pattern, "!") ||
				strings.ContainsAny(pattern, "*?") {
				continue
			}
			hostname := block.hostname
			if hostname == "" {
				hostname = pattern
			}
			if !validHostname(hostname) {
				result.Issues = append(result.Issues,
					issue(IssueInvalidHostname, block.row, "hostname", hostname))
				continue
			}
			record := Record{
				Row: block.row, Source: "ssh_config", Label: pattern,
				Hostname: hostname, Username: block.username,
				Port: block.port, Protocol: "ssh",
				X11Forwarding: block.forwardX11,
			}
			if record.Port == 0 {
				record.Port = 22
			}
			if len(block.identityFiles) > 0 {
				record.KeyPath = block.identityFiles[0]
			}
			record.JumpSpecs = parseJumpSpecs(block.proxyJump)
			agentEnabled := block.agentExplicit && !strings.EqualFold(block.identityAgent, "none")
			// The macOS Keychain pattern (AddKeysToAgent plus UseKeychain)
			// implies agent-backed login even without IdentityAgent.
			keychainAgent := block.addKeysToAgent == "yes" && block.useKeychain
			record.UseSSHAgent = agentEnabled || (!block.agentExplicit && keychainAgent)
			result.Records = append(result.Records, record)
		}
	}

	resolveSSHJumps(&result)
	for index := range result.Records {
		if result.Records[index].Label == "" {
			result.Records[index].Label = result.Records[index].Hostname
		}
	}
	if len(result.Records) == 0 && !hasError(result.Issues) {
		result.Issues = append(result.Issues, issue(IssueNothingParsed, 0, "", ""))
	}
	return result
}

// resolveSSHJumps resolves every ProxyJump spec to a record index, creating
// inline records for undeclared hops and removing cycles.
func resolveSSHJumps(result *Result) {
	n := len(result.Records)
	if n == 0 {
		return
	}
	byName := make(map[string]int)
	for index := 0; index < n; index++ {
		record := result.Records[index]
		if record.Hostname != "" {
			byName[strings.ToLower(record.Hostname)] = index
		}
		if record.Label != "" {
			byName[strings.ToLower(record.Label)] = index
		}
	}

	specs := make([][]Jump, n)
	for index := 0; index < n; index++ {
		specs[index] = result.Records[index].JumpSpecs
	}
	for index := 0; index < n; index++ {
		for _, spec := range specs[index] {
			name := strings.ToLower(spec.Hostname)
			target, ok := byName[name]
			if !ok {
				port := spec.Port
				if port == 0 {
					port = 22
				}
				result.Records = append(result.Records, Record{
					Row: result.Records[index].Row, Source: "ssh_config",
					Label: spec.Hostname, Hostname: spec.Hostname,
					Username: spec.Username, Port: port, Protocol: "ssh",
					InlineJump: true,
				})
				target = len(result.Records) - 1
				byName[name] = target
				result.Issues = append(result.Issues, issue(
					IssueInvalidJump, result.Records[index].Row, "proxyjump", spec.Hostname))
			}
			result.Records[index].JumpRefs = appendUniqueInt(
				result.Records[index].JumpRefs, target)
		}
	}
	removeJumpCycles(result)
}

func appendUniqueInt(values []int, value int) []int {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

// removeJumpCycles drops edges that close a cycle. ProxyJump cycles cannot be
// represented by a jump chain and would make a connection loop forever.
func removeJumpCycles(result *Result) {
	n := len(result.Records)
	state := make([]int, n)
	reported := false
	var visit func(int) bool
	visit = func(node int) bool {
		if state[node] == 1 {
			return true
		}
		if state[node] == 2 {
			return false
		}
		state[node] = 1
		kept := result.Records[node].JumpRefs[:0]
		for _, ref := range result.Records[node].JumpRefs {
			if ref < 0 || ref >= n {
				continue
			}
			if visit(ref) {
				reported = true
				continue
			}
			kept = append(kept, ref)
		}
		result.Records[node].JumpRefs = kept
		state[node] = 2
		return false
	}
	for index := 0; index < n; index++ {
		visit(index)
	}
	if reported {
		result.Issues = append(result.Issues, issue(IssueCyclicJump, 0, "proxyjump", ""))
	}
}

// stripSSHComment removes a # comment, leaving quoted text alone.
func stripSSHComment(line string) string {
	var out strings.Builder
	quote := rune(0)
	for _, r := range line {
		switch {
		case quote != 0:
			out.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
			out.WriteRune(r)
		case r == '#':
			return out.String()
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// splitSSHFields splits on whitespace, keeping quoted values intact.
func splitSSHFields(line string) []string {
	var fields []string
	var current strings.Builder
	quote := rune(0)
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return fields
}

func unquoteSSH(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 &&
		((value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}

// parseJumpSpecs reads one ProxyJump value into hops.
func parseJumpSpecs(value string) []Jump {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "none") {
		return nil
	}
	var jumps []Jump
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		hostname, username, port, _, ok := parseTarget(part)
		if !ok {
			continue
		}
		jumps = append(jumps, Jump{Username: username, Hostname: hostname, Port: port})
	}
	return jumps
}
