package vault

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestNormalizeDraftTrimsFillsDefaultsAndFallsBackToHostname(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label:    "  ",
		Hostname: "  example.com  ",
		Username: "  root ",
		Group:    " /prod//web/ ",
		Tags:     []string{" prod ", "", "prod", " web "},
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}

	if draft.Label != "example.com" {
		t.Errorf("Label = %q, want the hostname", draft.Label)
	}
	if draft.Hostname != "example.com" {
		t.Errorf("Hostname = %q, want example.com", draft.Hostname)
	}
	if draft.Port != 22 {
		t.Errorf("Port = %d, want the SSH default 22", draft.Port)
	}
	if draft.Protocol != ProtocolSSH {
		t.Errorf("Protocol = %q, want %q", draft.Protocol, ProtocolSSH)
	}
	if draft.Username != "root" {
		t.Errorf("Username = %q, want root", draft.Username)
	}
	if draft.Group != "prod/web" {
		t.Errorf("Group = %q, want prod/web", draft.Group)
	}
	if got := strings.Join(draft.Tags, ","); got != "prod,web" {
		t.Errorf("Tags = %q, want prod,web", got)
	}
}

func TestNormalizeDraftDefaultsTheProtocolPort(t *testing.T) {
	tests := []struct {
		name     string
		protocol Protocol
		label    string
		hostname string
		want     int
	}{
		{"ssh", ProtocolSSH, "example.com", "example.com", 22},
		{"telnet", ProtocolTelnet, "example.com", "example.com", 23},
		{"local", ProtocolLocal, "Local shell", "", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft, err := NormalizeDraft(Draft{
				Label:    test.label,
				Hostname: test.hostname,
				Protocol: test.protocol,
			})
			if err != nil {
				t.Fatalf("NormalizeDraft: %v", err)
			}
			if draft.Port != test.want {
				t.Fatalf("Port = %d, want %d", draft.Port, test.want)
			}
		})
	}
}

func TestNormalizeDraftRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		draft Draft
		field Field
		rule  Rule
	}{
		{
			name:  "empty hostname",
			draft: Draft{Label: "web"},
			field: FieldHostname,
			rule:  RuleRequired,
		},
		{
			name:  "hostname with whitespace",
			draft: Draft{Label: "web", Hostname: "two words"},
			field: FieldHostname,
			rule:  RuleInvalid,
		},
		{
			name:  "hostname with a newline",
			draft: Draft{Label: "web", Hostname: "a\nb"},
			field: FieldHostname,
			rule:  RuleInvalid,
		},
		{
			name:  "port below the range",
			draft: Draft{Label: "web", Hostname: "a", Port: -1},
			field: FieldPort,
			rule:  RuleOutOfRange,
		},
		{
			name:  "port above the range",
			draft: Draft{Label: "web", Hostname: "a", Port: 65536},
			field: FieldPort,
			rule:  RuleOutOfRange,
		},
		{
			name:  "unknown protocol",
			draft: Draft{Label: "web", Hostname: "a", Protocol: "gopher"},
			field: FieldProtocol,
			rule:  RuleUnsupported,
		},
		{
			name:  "local Host without a label",
			draft: Draft{Protocol: ProtocolLocal},
			field: FieldLabel,
			rule:  RuleRequired,
		},
		{
			name:  "group with a control character",
			draft: Draft{Label: "web", Hostname: "a", Group: "prod\x00/web"},
			field: FieldGroup,
			rule:  RuleInvalid,
		},
		{
			name:  "tag that is too long",
			draft: Draft{Label: "web", Hostname: "a", Tags: []string{strings.Repeat("x", maxTagLength+1)}},
			field: FieldTags,
			rule:  RuleTooLong,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeDraft(test.draft)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("NormalizeDraft error = %v, want a ValidationError", err)
			}
			if validation.Field != test.field || validation.Rule != test.rule {
				t.Fatalf("validation = %s/%s, want %s/%s",
					validation.Field, validation.Rule, test.field, test.rule)
			}
		})
	}
}

func TestLocalHostAllowsAnEmptyHostname(t *testing.T) {
	draft, err := NormalizeDraft(Draft{Label: "My shell", Protocol: ProtocolLocal})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.Hostname != "" || draft.Port != 0 {
		t.Fatalf("hostname = %q, port = %d, want empty and 0", draft.Hostname, draft.Port)
	}
}

func TestDraftGroupNormalizationKeepsPathsReadable(t *testing.T) {
	tests := map[string]string{
		"":                 "",
		"/":                "",
		"prod":             "prod",
		"/prod/web/":       "prod/web",
		"prod//web":        "prod/web",
		" prod / web ":     "prod/web",
		"prod\\web":        "prod/web",
		"prod/web/servers": "prod/web/servers",
	}
	for input, want := range tests {
		if got := NormalizeGroup(input); got != want {
			t.Errorf("NormalizeGroup(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMergeKeyDistinguishesEndpointsAndLocalHosts(t *testing.T) {
	base := Host{Label: "web", Protocol: ProtocolSSH, Hostname: "Example.COM", Port: 22, Username: "Root", Group: "prod"}
	same := base
	same.Label = "renamed"
	same.Hostname = "example.com"
	same.Username = "root"
	if base.MergeKey() != same.MergeKey() {
		t.Fatalf("merge keys differ for the same endpoint:\n%q\n%q", base.MergeKey(), same.MergeKey())
	}

	otherPort := base
	otherPort.Port = 2222
	if base.MergeKey() == otherPort.MergeKey() {
		t.Fatal("merge keys match for different ports")
	}

	localA := Host{Label: "Bash", Protocol: ProtocolLocal}
	localB := Host{Label: "PowerShell", Protocol: ProtocolLocal}
	if localA.MergeKey() == localB.MergeKey() {
		t.Fatal("merge keys match for two local Hosts")
	}
}

func TestNormalizeDraftNormalizesTheJumpChainAndProxy(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label:    "web",
		Hostname: "example.com",
		HostChain: &HostChain{HostIDs: []model.HostID{
			"  jump-1 ", "jump-2",
		}},
		Proxy: &ProxyConfig{
			Type: " SOCKS5 ", Host: " proxy.example.com ", Port: 1080,
			Username: " alice ", Password: "s3cret",
		},
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.HostChain == nil || len(draft.HostChain.HostIDs) != 2 ||
		draft.HostChain.HostIDs[0] != "jump-1" || draft.HostChain.HostIDs[1] != "jump-2" {
		t.Fatalf("HostChain = %+v", draft.HostChain)
	}
	if draft.Proxy == nil || draft.Proxy.Type != ProxySOCKS5 ||
		draft.Proxy.Host != "proxy.example.com" || draft.Proxy.Port != 1080 ||
		draft.Proxy.Username != "alice" || draft.Proxy.Password != "s3cret" {
		t.Fatalf("Proxy = %+v", draft.Proxy)
	}
}

func TestNormalizeDraftDropsAnEmptyChainAndProxy(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		HostChain: &HostChain{},
		Proxy:     &ProxyConfig{},
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.HostChain != nil || draft.Proxy != nil {
		t.Fatalf("chain = %+v, proxy = %+v; want both nil", draft.HostChain, draft.Proxy)
	}
}

func TestNormalizeDraftRejectsInvalidChainsAndProxies(t *testing.T) {
	tests := []struct {
		name  string
		draft Draft
		field Field
		rule  Rule
	}{
		{
			name:  "empty jump reference",
			draft: Draft{Label: "web", Hostname: "a", HostChain: &HostChain{HostIDs: []model.HostID{""}}},
			field: FieldChain,
			rule:  RuleInvalid,
		},
		{
			name:  "duplicate jump reference",
			draft: Draft{Label: "web", Hostname: "a", HostChain: &HostChain{HostIDs: []model.HostID{"j", "j"}}},
			field: FieldChain,
			rule:  RuleInvalid,
		},
		{
			name: "chain too long",
			draft: Draft{Label: "web", Hostname: "a", HostChain: &HostChain{
				HostIDs: make([]model.HostID, maxHostChainLength+1),
			}},
			field: FieldChain,
			rule:  RuleTooLong,
		},
		{
			name:  "unknown proxy type",
			draft: Draft{Label: "web", Hostname: "a", Proxy: &ProxyConfig{Type: "telnet", Host: "p", Port: 1}},
			field: FieldProxy,
			rule:  RuleUnsupported,
		},
		{
			name:  "HTTP proxy without a host",
			draft: Draft{Label: "web", Hostname: "a", Proxy: &ProxyConfig{Type: ProxyHTTP, Port: 8080}},
			field: FieldProxy,
			rule:  RuleRequired,
		},
		{
			name:  "SOCKS5 proxy with an invalid port",
			draft: Draft{Label: "web", Hostname: "a", Proxy: &ProxyConfig{Type: ProxySOCKS5, Host: "p", Port: 0}},
			field: FieldProxy,
			rule:  RuleOutOfRange,
		},
		{
			name:  "ProxyCommand without a command",
			draft: Draft{Label: "web", Hostname: "a", Proxy: &ProxyConfig{Type: ProxyCommand}},
			field: FieldProxy,
			rule:  RuleRequired,
		},
		{
			name:  "HTTP proxy with a command",
			draft: Draft{Label: "web", Hostname: "a", Proxy: &ProxyConfig{Type: ProxyHTTP, Host: "p", Port: 1, Command: "nc"}},
			field: FieldProxy,
			rule:  RuleInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeDraft(test.draft)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("NormalizeDraft error = %v, want a ValidationError", err)
			}
			if validation.Field != test.field || validation.Rule != test.rule {
				t.Fatalf("validation = %s/%s, want %s/%s",
					validation.Field, validation.Rule, test.field, test.rule)
			}
		})
	}
}

func TestNormalizeDraftNormalizesAlgorithmOverrides(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		Algorithms: &AlgorithmOverrides{
			KeyExchanges: []string{" curve25519-sha256 ", "", "curve25519-sha256"},
			Ciphers:      []string{"aes256-ctr", "aes256-ctr"},
			MACs:         []string{"  "},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.Algorithms == nil {
		t.Fatal("Algorithms = nil")
	}
	if !slices.Equal(draft.Algorithms.KeyExchanges, []string{"curve25519-sha256"}) {
		t.Fatalf("key exchanges = %v", draft.Algorithms.KeyExchanges)
	}
	if !slices.Equal(draft.Algorithms.Ciphers, []string{"aes256-ctr"}) {
		t.Fatalf("ciphers = %v", draft.Algorithms.Ciphers)
	}
	if draft.Algorithms.MACs != nil || draft.Algorithms.HostKeys != nil {
		t.Fatalf("empty categories = %v/%v, want nil", draft.Algorithms.MACs, draft.Algorithms.HostKeys)
	}

	empty, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		Algorithms: &AlgorithmOverrides{Ciphers: []string{" "}},
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if empty.Algorithms != nil {
		t.Fatalf("Algorithms = %+v, want nil for an empty selection", empty.Algorithms)
	}
}

func TestNormalizeDraftRejectsInvalidAlgorithmOverrides(t *testing.T) {
	tooMany := make([]string, 0, maxAlgorithmCount+1)
	for i := 0; i <= maxAlgorithmCount; i++ {
		tooMany = append(tooMany, fmt.Sprintf("algorithm-%d", i))
	}
	tests := []struct {
		name       string
		algorithms *AlgorithmOverrides
		rule       Rule
	}{
		{
			name:       "too many algorithms",
			algorithms: &AlgorithmOverrides{KeyExchanges: tooMany},
			rule:       RuleTooLong,
		},
		{
			name:       "algorithm name too long",
			algorithms: &AlgorithmOverrides{MACs: []string{strings.Repeat("a", maxAlgorithmNameLength+1)}},
			rule:       RuleTooLong,
		},
		{
			name:       "algorithm name with a control character",
			algorithms: &AlgorithmOverrides{HostKeys: []string{"ssh-ed25519\x00"}},
			rule:       RuleInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeDraft(Draft{Label: "web", Hostname: "a", Algorithms: test.algorithms})
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("NormalizeDraft error = %v, want a ValidationError", err)
			}
			if validation.Field != FieldAlgorithms || validation.Rule != test.rule {
				t.Fatalf("validation = %s/%s, want algorithms/%s", validation.Field, validation.Rule, test.rule)
			}
		})
	}
}

func TestCloneAlgorithmsReturnsADeepCopy(t *testing.T) {
	original := &AlgorithmOverrides{
		KeyExchanges: []string{"curve25519-sha256"},
		Ciphers:      []string{"aes256-ctr"},
		MACs:         []string{"hmac-sha2-256"},
		HostKeys:     []string{"ssh-ed25519"},
	}
	clone := CloneAlgorithms(original)
	if clone == nil || clone == original {
		t.Fatalf("CloneAlgorithms = %+v", clone)
	}
	clone.KeyExchanges[0] = "changed"
	clone.HostKeys[0] = "changed"
	if original.KeyExchanges[0] != "curve25519-sha256" || original.HostKeys[0] != "ssh-ed25519" {
		t.Fatalf("CloneAlgorithms shared its slices: %+v", original)
	}
	if CloneAlgorithms(nil) != nil {
		t.Fatal("CloneAlgorithms(nil) != nil")
	}
}

func TestCloneProxyNeverCopiesTheCredential(t *testing.T) {
	proxy := CloneProxy(&ProxyConfig{
		Type: ProxyHTTP, Host: "proxy", Port: 8080,
		Username: "alice", Password: "s3cret", HasPassword: true,
	})
	if proxy == nil || proxy.Password != "" || !proxy.HasPassword || proxy.Username != "alice" {
		t.Fatalf("CloneProxy = %+v", proxy)
	}
}

func TestHostValidationAcceptsANormalizedHost(t *testing.T) {
	host := Host{
		ID:       "01HOST",
		Label:    "web",
		Protocol: ProtocolSSH,
		Hostname: "example.com",
		Port:     22,
		Tags:     []string{"prod"},
		Algorithms: &AlgorithmOverrides{
			Ciphers: []string{"aes256-ctr"},
		},
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	host.Algorithms = &AlgorithmOverrides{Ciphers: []string{""}}
	var validation *ValidationError
	if err := host.Validate(); !errors.As(err, &validation) || validation.Field != FieldAlgorithms {
		t.Fatalf("Validate error = %v, want an algorithms validation error", err)
	}
}
