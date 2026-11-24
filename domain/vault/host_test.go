package vault

import (
	"encoding/json"
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
	if draft.Port != 0 {
		t.Errorf("Port = %d, want 0 so the Host can inherit its group's port", draft.Port)
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

func TestNormalizeDraftRequiresTheX11WarningAcknowledgement(t *testing.T) {
	_, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com", X11Forwarding: true,
	})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("NormalizeDraft error = %v, want a ValidationError", err)
	}
	if invalid.Field != FieldX11 || invalid.Rule != RuleAcknowledgementRequired {
		t.Fatalf("validation = %+v, want X11 acknowledgement required", invalid)
	}

	draft, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		X11Forwarding: true, X11WarningAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if !draft.X11Forwarding || !draft.X11WarningAcknowledged {
		t.Fatalf("draft = %+v, want X11 enabled and acknowledged", draft)
	}
}

func TestNormalizeDraftDisablingX11DropsTheAcknowledgement(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com", X11WarningAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.X11Forwarding || draft.X11WarningAcknowledged {
		t.Fatalf("draft = %+v, want X11 disabled and the acknowledgement cleared", draft)
	}
}

func TestNormalizeDraftClearsX11OutsidePlainSSH(t *testing.T) {
	for _, test := range []struct {
		name      string
		protocol  Protocol
		transport Transport
	}{
		{"telnet", ProtocolTelnet, TransportPlain},
		{"vnc", ProtocolVNC, TransportPlain},
		{"mosh", ProtocolSSH, TransportMosh},
		{"et", ProtocolSSH, TransportET},
	} {
		draft, err := NormalizeDraft(Draft{
			Label: "web", Hostname: "example.com",
			Protocol: test.protocol, Transport: test.transport,
			X11Forwarding: true, X11WarningAcknowledged: true,
		})
		if err != nil {
			t.Errorf("%s: NormalizeDraft: %v", test.name, err)
			continue
		}
		// X11 is forwarded over a plain SSH connection. These protocols and
		// transports have no X11 channel, so the hidden preference is cleared
		// instead of making the record impossible to save.
		if draft.X11Forwarding || draft.X11WarningAcknowledged {
			t.Errorf("%s: draft = %+v, want X11 cleared", test.name, draft)
		}
	}
}

func TestHostX11EnabledNeedsBothPreferenceAndAcknowledgement(t *testing.T) {
	host := Host{X11Forwarding: true}
	if host.X11Enabled() {
		t.Fatal("an unacknowledged X11 preference enabled forwarding")
	}
	host.X11WarningAcknowledged = true
	if !host.X11Enabled() {
		t.Fatal("an acknowledged X11 preference did not enable forwarding")
	}
}

func TestNormalizeDraftKeepsAnEmptyPortForGroupInheritance(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolSSH, ProtocolTelnet, ProtocolVNC} {
		t.Run(string(protocol), func(t *testing.T) {
			draft, err := NormalizeDraft(Draft{
				Label:    "example.com",
				Hostname: "example.com",
				Protocol: protocol,
			})
			if err != nil {
				t.Fatalf("NormalizeDraft: %v", err)
			}
			if draft.Port != 0 {
				t.Fatalf("Port = %d, want 0 so the Host can inherit its group's port", draft.Port)
			}
		})
	}
}

func TestEffectivePortUsesTheHostThenTheProtocolDefault(t *testing.T) {
	tests := []struct {
		name     string
		protocol Protocol
		port     int
		want     int
	}{
		{"explicit SSH port wins", ProtocolSSH, 2222, 2222},
		{"SSH default", ProtocolSSH, 0, 22},
		{"telnet default", ProtocolTelnet, 0, 23},
		{"vnc default", ProtocolVNC, 0, 5900},
		{"vnc display port", ProtocolVNC, 5901, 5901},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EffectivePort(test.protocol, test.port); got != test.want {
				t.Fatalf("EffectivePort(%q, %d) = %d, want %d",
					test.protocol, test.port, got, test.want)
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
			name:  "removed local protocol",
			draft: Draft{Label: "web", Hostname: "a", Protocol: Protocol("local")},
			field: FieldProtocol,
			rule:  RuleUnsupported,
		},
		{
			name:  "removed mosh protocol",
			draft: Draft{Label: "web", Hostname: "a", Protocol: Protocol("mosh")},
			field: FieldProtocol,
			rule:  RuleUnsupported,
		},
		{
			name:  "removed et protocol",
			draft: Draft{Label: "web", Hostname: "a", Protocol: Protocol("et")},
			field: FieldProtocol,
			rule:  RuleUnsupported,
		},
		{
			name:  "unknown transport",
			draft: Draft{Label: "web", Hostname: "a", Protocol: ProtocolSSH, Transport: Transport("udp")},
			field: FieldTransport,
			rule:  RuleUnsupported,
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

func TestNormalizeDraftCanonicalizesTheTransport(t *testing.T) {
	cases := []struct {
		transport Transport
		want      Transport
	}{
		{"", TransportPlain},
		{" ssh ", TransportPlain},
		{"SSH", TransportPlain},
		{"mosh", TransportMosh},
		{" MOSH ", TransportMosh},
		{"et", TransportET},
		{"ET", TransportET},
	}
	for _, test := range cases {
		draft, err := NormalizeDraft(Draft{
			Label: "web", Hostname: "example.com",
			Protocol: ProtocolSSH, Transport: test.transport,
		})
		if err != nil {
			t.Fatalf("NormalizeDraft(%q): %v", test.transport, err)
		}
		if draft.Transport != test.want {
			t.Errorf("transport %q normalized to %q, want %q", test.transport, draft.Transport, test.want)
		}
	}
}

func TestNormalizeDraftClearsATransportOnANonSSHHost(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolTelnet, ProtocolVNC} {
		draft, err := NormalizeDraft(Draft{
			Label: "web", Hostname: "example.com",
			Protocol: protocol, Transport: TransportMosh,
		})
		if err != nil {
			t.Fatalf("NormalizeDraft %q: %v", protocol, err)
		}
		if draft.Transport != TransportPlain {
			t.Errorf("%q kept transport %q", protocol, draft.Transport)
		}
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

func TestMergeKeyDistinguishesEndpointsAndTransports(t *testing.T) {
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

	plain := base
	mosh := base
	mosh.Transport = TransportMosh
	et := base
	et.Transport = TransportET
	if plain.MergeKey() == mosh.MergeKey() || plain.MergeKey() == et.MergeKey() ||
		mosh.MergeKey() == et.MergeKey() {
		t.Fatal("merge keys match for different SSH transports")
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

func TestNormalizeDraftDefaultsAndValidatesTheFileProtocol(t *testing.T) {
	base := Draft{Label: "web", Hostname: "example.com", Protocol: ProtocolSSH}

	normalized, err := NormalizeDraft(base)
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if normalized.SFTPFileProtocol != FileProtocolAuto {
		t.Fatalf("default protocol = %q, want auto", normalized.SFTPFileProtocol)
	}

	base.SFTPFileProtocol = "  SFTP "
	normalized, err = NormalizeDraft(base)
	if err != nil {
		t.Fatalf("NormalizeDraft SFTP: %v", err)
	}
	if normalized.SFTPFileProtocol != FileProtocolSFTP {
		t.Fatalf("normalized protocol = %q", normalized.SFTPFileProtocol)
	}

	base.SFTPFileProtocol = FileProtocolSCP
	if _, err := NormalizeDraft(base); err != nil {
		t.Fatalf("SCP without sudo: %v", err)
	}
	base.SFTPSudo = true
	assertNormalizeValidation(t, base, FieldSFTPSudo, RuleUnsupported)

	base = Draft{Label: "web", Hostname: "example.com", Protocol: ProtocolSSH, SFTPSudo: true}
	if _, err := NormalizeDraft(base); err != nil {
		t.Fatalf("sudo with automatic protocol: %v", err)
	}
	base.SFTPFileProtocol = FileProtocolSFTP
	if _, err := NormalizeDraft(base); err != nil {
		t.Fatalf("sudo with SFTP: %v", err)
	}

	// A Telnet target has no SFTP subsystem. NormalizeDraft clears the
	// hidden preference instead of making an imported record unsaveable.
	telnet, err := NormalizeDraft(Draft{Label: "web", Hostname: "example.com", Protocol: ProtocolTelnet,
		SFTPFileProtocol: FileProtocolSFTP, SFTPSudo: true})
	if err != nil {
		t.Fatalf("NormalizeDraft Telnet file protocol: %v", err)
	}
	if telnet.SFTPFileProtocol != FileProtocolAuto || telnet.SFTPSudo {
		t.Fatalf("Telnet draft = %+v, want the file-transfer preference cleared", telnet)
	}

	base = Draft{Label: "web", Hostname: "example.com", Protocol: ProtocolSSH,
		SFTPFileProtocol: "ftp"}
	assertNormalizeValidation(t, base, FieldFileProtocol, RuleInvalid)
}

func TestValidateFieldsTreatsAnEmptyFileProtocolAsAutomatic(t *testing.T) {
	draft := Draft{Label: "web", Hostname: "example.com", Protocol: ProtocolSSH}
	if err := validateFields(draft); err != nil {
		t.Fatalf("SSH with an empty protocol: %v", err)
	}
	draft.Protocol = ProtocolTelnet
	if err := validateFields(draft); err != nil {
		t.Fatalf("Telnet with an empty protocol: %v", err)
	}
	draft.SFTPFileProtocol = FileProtocolSFTP
	var validation *ValidationError
	if err := validateFields(draft); !errors.As(err, &validation) ||
		validation.Field != FieldFileProtocol || validation.Rule != RuleUnsupported {
		t.Fatalf("Telnet with SFTP error = %v", err)
	}
}

func assertNormalizeValidation(t *testing.T, draft Draft, field Field, rule Rule) {
	t.Helper()
	_, err := NormalizeDraft(draft)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if validation.Field != field || validation.Rule != rule {
		t.Fatalf("validation = %+v, want %s/%s", validation, field, rule)
	}
}

func TestNetworkDeviceModeIsKeptOnlyForSSH(t *testing.T) {
	normalized, err := NormalizeDraft(Draft{
		Label: "switch", Hostname: "switch.example.com", Protocol: ProtocolSSH,
		DeviceType: " Network ",
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if normalized.DeviceType != DeviceTypeNetwork {
		t.Fatalf("DeviceType = %q, want network", normalized.DeviceType)
	}
	host := Host{Protocol: ProtocolSSH, DeviceType: normalized.DeviceType}
	if !host.IsNetworkDevice() {
		t.Fatal("an SSH Host with network device mode is not recognized")
	}

	// Another transport cannot run raw execution, so neither the draft nor the
	// read-side check may keep the mode.
	other, err := NormalizeDraft(Draft{
		Label: "console", Hostname: "console.local", Protocol: ProtocolTelnet,
		DeviceType: DeviceTypeNetwork,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft telnet: %v", err)
	}
	if other.DeviceType != DeviceTypeDefault {
		t.Fatalf("Telnet DeviceType = %q, want the default", other.DeviceType)
	}
	stray := Host{Protocol: ProtocolTelnet, DeviceType: DeviceTypeNetwork}
	if stray.IsNetworkDevice() {
		t.Fatal("a non-SSH Host was treated as a network device")
	}
}

func TestUsesSSHAuthFollowsTheEffectiveTransport(t *testing.T) {
	// The Host editor hides Identity, SSH Key, and Method when the effective
	// transport does not bootstrap through SSH. Mosh and Eternal Terminal ride
	// SSH, so an SSH Host with either transport keeps the controls; Telnet and
	// VNC do not. The editor and the state layer read the Host/Draft
	// capability, so the boundary is pinned here.
	for _, test := range []struct {
		name string
		host Host
		want bool
	}{
		{"ssh", Host{Protocol: ProtocolSSH}, true},
		{"mosh", Host{Protocol: ProtocolSSH, Transport: TransportMosh}, true},
		{"et", Host{Protocol: ProtocolSSH, Transport: TransportET}, true},
		{"telnet", Host{Protocol: ProtocolTelnet}, false},
		{"vnc", Host{Protocol: ProtocolVNC}, false},
		// An unknown protocol or transport has no capabilities.
		{"unknown protocol", Host{Protocol: Protocol("unknown")}, false},
		{"unknown transport", Host{Protocol: ProtocolSSH, Transport: Transport("udp")}, false},
	} {
		if got := test.host.UsesSSHAuth(); got != test.want {
			t.Errorf("%s: UsesSSHAuth() = %v, want %v", test.name, got, test.want)
		}
		draft := Draft{Protocol: test.host.Protocol, Transport: test.host.Transport}
		if got := draft.UsesSSHAuth(); got != test.want {
			t.Errorf("%s: Draft.UsesSSHAuth() = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestNormalizeDraftDropsSSHCredentialsFromNonSSHProtocols(t *testing.T) {
	// The Host editor hides Identity, SSH Key, and Method for Telnet and a
	// Local shell, but an existing Host opened for edit still carries their
	// stored values in its draft. NormalizeDraft is the last gate before the
	// save, so it must drop the hidden bundle there too.
	credentialed := Draft{
		Label: "web", Hostname: "example.com",
		IdentityID:  model.IdentityID("identity-1"),
		KeyID:       model.KeyID("key-1"),
		AuthMethod:  HostAuthKey,
		UseSSHAgent: true, RequiresMFA: true,
	}
	for _, protocol := range []Protocol{ProtocolTelnet, ProtocolVNC} {
		draft := credentialed
		draft.Protocol = protocol
		normalized, err := NormalizeDraft(draft)
		if err != nil {
			t.Fatalf("NormalizeDraft %q: %v", protocol, err)
		}
		if normalized.IdentityID != "" || normalized.KeyID != "" ||
			normalized.UseSSHAgent || normalized.RequiresMFA {
			t.Errorf("%q kept the hidden SSH bundle: %+v", protocol, normalized)
		}
		if normalized.AuthMethod != HostAuthAuto {
			t.Errorf("%q AuthMethod = %q, want %q", protocol, normalized.AuthMethod, HostAuthAuto)
		}
	}

	for _, transport := range []Transport{TransportPlain, TransportMosh, TransportET} {
		draft := credentialed
		draft.Protocol = ProtocolSSH
		draft.Transport = transport
		normalized, err := NormalizeDraft(draft)
		if err != nil {
			t.Fatalf("NormalizeDraft ssh/%q: %v", transport, err)
		}
		if normalized.IdentityID != "identity-1" || normalized.KeyID != "key-1" ||
			normalized.AuthMethod != HostAuthKey ||
			!normalized.UseSSHAgent || !normalized.RequiresMFA {
			t.Errorf("ssh/%q lost its SSH bundle: %+v", transport, normalized)
		}
	}
}

func TestNormalizeDraftKeepsMoshAndETSettingsOnTheirTransport(t *testing.T) {
	mosh, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		Protocol: ProtocolSSH, Transport: TransportMosh,
		MoshServerPath: "  /usr/local/bin/mosh-server  ", ETPort: 2022,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft mosh: %v", err)
	}
	if mosh.MoshServerPath != "/usr/local/bin/mosh-server" {
		t.Fatalf("MoshServerPath = %q", mosh.MoshServerPath)
	}
	if mosh.ETPort != 0 {
		t.Fatalf("mosh kept ETPort = %d", mosh.ETPort)
	}

	et, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		Protocol: ProtocolSSH, Transport: TransportET,
		MoshServerPath: "/usr/bin/mosh-server", ETPort: 8737,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft et: %v", err)
	}
	if et.ETPort != 8737 {
		t.Fatalf("ETPort = %d", et.ETPort)
	}
	if et.MoshServerPath != "" {
		t.Fatalf("et kept MoshServerPath = %q", et.MoshServerPath)
	}

	if plain, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		Protocol:       ProtocolSSH,
		MoshServerPath: "/usr/bin/mosh-server", ETPort: 8737,
	}); err != nil {
		t.Fatalf("NormalizeDraft plain: %v", err)
	} else if plain.MoshServerPath != "" || plain.ETPort != 0 {
		t.Fatalf("plain SSH kept transport settings: %+v", plain)
	}
}

func TestNormalizeDraftRejectsAnOutOfRangeETPort(t *testing.T) {
	_, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		Protocol: ProtocolSSH, Transport: TransportET, ETPort: 70000,
	})
	var invalid *ValidationError
	if !errors.As(err, &invalid) || invalid.Field != FieldETPort || invalid.Rule != RuleOutOfRange {
		t.Fatalf("error = %v, want an out-of-range ET port", err)
	}
}

func TestStartupCommandIsTrimmedAndSurvivesTheRecord(t *testing.T) {
	normalized, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com",
		StartupCommand: "  echo ready\r\nuptime  ",
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if normalized.StartupCommand != "echo ready\r\nuptime" {
		t.Fatalf("StartupCommand = %q", normalized.StartupCommand)
	}

	host := Host{
		ID:             model.HostID("host-1"),
		Label:          normalized.Label,
		Hostname:       normalized.Hostname,
		Protocol:       normalized.Protocol,
		StartupCommand: normalized.StartupCommand,
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	encoded, err := json.Marshal(host)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Host
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.StartupCommand != normalized.StartupCommand {
		t.Fatalf("round-trip StartupCommand = %q", decoded.StartupCommand)
	}

	if _, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com", StartupCommand: "echo\x00",
	}); err == nil {
		t.Fatal("a startup command containing NUL was accepted")
	} else {
		var invalid *ValidationError
		if !errors.As(err, &invalid) || invalid.Field != FieldStartupCommand || invalid.Rule != RuleInvalid {
			t.Fatalf("error = %v, want an invalid startup command", err)
		}
	}
}

func TestEffectivePortUsesTheSSHDefault(t *testing.T) {
	// The transport is not part of port resolution: an SSH Host with Mosh or
	// ET still connects to the SSH bootstrap port.
	if got := EffectivePort(ProtocolSSH, 0); got != 22 {
		t.Errorf("EffectivePort = %d, want 22", got)
	}
}

func TestAutoAttachTmuxIsKeptOnlyForPlainSSHHosts(t *testing.T) {
	ssh, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com", Protocol: ProtocolSSH,
		AutoAttachTmux: true,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft SSH: %v", err)
	}
	if !ssh.AutoAttachTmux {
		t.Fatal("an SSH draft lost the auto-attach preference")
	}

	for _, test := range []struct {
		protocol  Protocol
		transport Transport
	}{
		{ProtocolTelnet, TransportPlain},
		{ProtocolVNC, TransportPlain},
		{ProtocolSSH, TransportMosh},
		{ProtocolSSH, TransportET},
	} {
		normalized, err := NormalizeDraft(Draft{
			Label: "web", Hostname: "example.com",
			Protocol: test.protocol, Transport: test.transport,
			AutoAttachTmux: true,
		})
		if err != nil {
			t.Fatalf("NormalizeDraft %s/%s: %v", test.protocol, test.transport, err)
		}
		if normalized.AutoAttachTmux {
			t.Errorf("%s/%s kept autoAttachTmux", test.protocol, test.transport)
		}
	}

	network, err := NormalizeDraft(Draft{
		Label: "switch", Hostname: "10.0.0.1", Protocol: ProtocolSSH,
		DeviceType: DeviceTypeNetwork, AutoAttachTmux: true,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft network device: %v", err)
	}
	if network.AutoAttachTmux {
		t.Fatal("network-device mode kept autoAttachTmux")
	}
}

func TestNormalizeDraftCanonicalizesATOTPInput(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	draft, err := NormalizeDraft(Draft{
		Hostname: "example.test",
		TOTP:     "  " + strings.ToLower(secret) + "  ",
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if !strings.HasPrefix(draft.TOTP, "otpauth://totp/") {
		t.Fatalf("TOTP = %q, want a canonical otpauth URI", draft.TOTP)
	}
	parsed, err := ParseTOTP(draft.TOTP)
	if err != nil {
		t.Fatalf("canonical TOTP does not parse: %v", err)
	}
	if parsed.Algorithm() != TOTPAlgorithmSHA1 || parsed.Digits() != 6 || parsed.Period() != 30 {
		t.Fatalf("defaults = %s/%d/%d", parsed.Algorithm(), parsed.Digits(), parsed.Period())
	}
}

func TestNormalizeDraftRejectsAnInvalidTOTPInput(t *testing.T) {
	_, err := NormalizeDraft(Draft{Hostname: "example.test", TOTP: "not a secret"})
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if validation.Field != FieldTOTP || validation.Rule != RuleInvalid {
		t.Fatalf("validation = %+v", validation)
	}
}

func TestNormalizeDraftClearsTOTPForNonSSHProtocols(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	draft, err := NormalizeDraft(Draft{
		Hostname:  "example.test",
		Protocol:  ProtocolTelnet,
		TOTP:      secret,
		ClearTOTP: true,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.TOTP != "" || draft.ClearTOTP {
		t.Fatalf("non-SSH draft kept TOTP=%q clear=%v", draft.TOTP, draft.ClearTOTP)
	}
}

// TestNormalizeDraftKeepsTheRouteForEveryProtocol pins the split between route
// fields and target-side SSH features: an SSH jump chain, a proxy, and
// algorithm overrides describe how to reach the Host, so every built-in
// protocol and every SSH transport keeps them.
func TestNormalizeDraftKeepsTheRouteForEveryProtocol(t *testing.T) {
	route := func(protocol Protocol, transport Transport) Draft {
		return Draft{
			Label: "console", Hostname: "console.local",
			Protocol: protocol, Transport: transport,
			HostChain:        &HostChain{HostIDs: []model.HostID{"bastion"}},
			Proxy:            &ProxyConfig{Type: "socks5", Host: "proxy.local", Port: 1080},
			ProxyProfileID:   "proxy-1",
			NetworkProfileID: "net-1",
			LegacyAlgorithms: true,
			SkipEcdsaHostKey: true,
			Algorithms:       &AlgorithmOverrides{Ciphers: []string{"aes128-ctr"}},
		}
	}

	for _, test := range []struct {
		name      string
		protocol  Protocol
		transport Transport
	}{
		{"ssh", ProtocolSSH, TransportPlain},
		{"mosh", ProtocolSSH, TransportMosh},
		{"et", ProtocolSSH, TransportET},
		{"telnet", ProtocolTelnet, TransportPlain},
		{"vnc", ProtocolVNC, TransportPlain},
	} {
		normalized, err := NormalizeDraft(route(test.protocol, test.transport))
		if err != nil {
			t.Fatalf("%s NormalizeDraft: %v", test.name, err)
		}
		if normalized.HostChain == nil || normalized.Proxy == nil ||
			normalized.ProxyProfileID != "proxy-1" || normalized.NetworkProfileID != "net-1" ||
			!normalized.LegacyAlgorithms || !normalized.SkipEcdsaHostKey || normalized.Algorithms == nil {
			t.Fatalf("%s route = chain %+v, proxy %+v, profile %q, network %q, algorithms %v/%v/%+v",
				test.name, normalized.HostChain, normalized.Proxy, normalized.ProxyProfileID,
				normalized.NetworkProfileID, normalized.LegacyAlgorithms,
				normalized.SkipEcdsaHostKey, normalized.Algorithms)
		}
	}
}

// TestNormalizeVNCDraftDefaultsTheConfiguration pins the viewer defaults a new
// VNC Host starts from and proves the stored endpoint stays the real port.
func TestNormalizeVNCDraftDefaultsTheConfiguration(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label: "desk", Hostname: "desk.local", Protocol: ProtocolVNC, Port: 5901,
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.Port != 5901 {
		t.Fatalf("Port = %d, want the stored TCP port 5901", draft.Port)
	}
	if draft.VNC == nil {
		t.Fatal("a VNC Host lost its configuration")
	}
	want := DefaultVNCConfig()
	if *draft.VNC != want {
		t.Fatalf("VNC = %+v, want defaults %+v", *draft.VNC, want)
	}

	// Unknown enum values from an older or hand-edited record fall back to
	// their defaults, while an explicit Shared=false survives.
	fallback, err := NormalizeDraft(Draft{
		Label: "desk", Hostname: "desk.local", Protocol: ProtocolVNC,
		VNC: &VNCConfig{DisplayMode: "unknown", FitMode: "", CursorMode: "", Shared: true},
	})
	if err != nil {
		t.Fatalf("fallback NormalizeDraft: %v", err)
	}
	if *fallback.VNC != want {
		t.Fatalf("VNC fallback = %+v, want defaults %+v", *fallback.VNC, want)
	}
	exclusive, err := NormalizeDraft(Draft{
		Label: "desk", Hostname: "desk.local", Protocol: ProtocolVNC,
		VNC: &VNCConfig{Shared: false},
	})
	if err != nil {
		t.Fatalf("exclusive NormalizeDraft: %v", err)
	}
	if exclusive.VNC.Shared {
		t.Fatal("an explicit Shared=false was replaced by the default")
	}
}

// TestNormalizeDraftClearsVNCOutsideTheProtocol proves a hidden VNC section
// leaves no configuration or password behind when the Host is saved with
// another protocol, while the SSH password input is left alone.
func TestNormalizeDraftClearsVNCOutsideTheProtocol(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label: "web", Hostname: "example.com", Protocol: ProtocolSSH,
		Password:    "alpha-pass",
		VNCPassword: "beta-pass",
		VNC:         &VNCConfig{DisplayMode: VNCPort, FitMode: VNCClip},
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.VNC != nil || draft.VNCPassword != "" {
		t.Fatalf("SSH draft kept VNC config %+v / password %q", draft.VNC, draft.VNCPassword)
	}
	if draft.Password != "alpha-pass" {
		t.Fatalf("SSH password = %q, want it untouched", draft.Password)
	}
}

// TestNormalizeVNCNeverAcceptsTheSSHPasswordSlot proves a switched form cannot
// migrate its SSH password into a VNC Host; VNC uses only its own slot.
func TestNormalizeVNCNeverAcceptsTheSSHPasswordSlot(t *testing.T) {
	draft, err := NormalizeDraft(Draft{
		Label: "desk", Hostname: "desk.local", Protocol: ProtocolVNC,
		Password: "alpha-pass", VNCPassword: "beta-pass",
	})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.Password != "" {
		t.Fatalf("VNC draft kept the SSH password %q", draft.Password)
	}
	if draft.VNCPassword != "beta-pass" {
		t.Fatalf("VNC password = %q, want the dedicated input", draft.VNCPassword)
	}
}

// TestNormalizeVNCRejectsAnOutOfRangeDisplayEndpoint pins the display rule:
// display-number input maps to 5900-5999, while port input accepts any valid
// TCP port.
func TestNormalizeVNCRejectsAnOutOfRangeDisplayEndpoint(t *testing.T) {
	for _, port := range []int{6000, 22, 5900 + VNCDisplayMax + 1} {
		_, err := NormalizeDraft(Draft{
			Label: "desk", Hostname: "desk.local", Protocol: ProtocolVNC, Port: port,
			VNC: &VNCConfig{DisplayMode: VNCDisplayNumber},
		})
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Field != FieldPort || validation.Rule != RuleOutOfRange {
			t.Fatalf("port %d: error = %v, want FieldPort out of range", port, err)
		}
	}

	// The same port is valid when the editor presents raw-port input.
	portDraft, err := NormalizeDraft(Draft{
		Label: "desk", Hostname: "desk.local", Protocol: ProtocolVNC, Port: 6000,
		VNC: &VNCConfig{DisplayMode: VNCPort},
	})
	if err != nil {
		t.Fatalf("port-mode NormalizeDraft: %v", err)
	}
	if portDraft.Port != 6000 {
		t.Fatalf("port-mode Port = %d, want 6000", portDraft.Port)
	}
}

// TestVNCVersionDisplayMapping pins the one editor rule both the state and
// domain use to translate a display number.
func TestVNCDisplayPortMapping(t *testing.T) {
	for display := 0; display <= VNCDisplayMax; display++ {
		port, ok := VNCPortForDisplay(display)
		if !ok || port != VNCDisplayBasePort+display {
			t.Fatalf("VNCPortForDisplay(%d) = %d, %v", display, port, ok)
		}
		back, ok := VNCDisplayForPort(port)
		if !ok || back != display {
			t.Fatalf("VNCDisplayForPort(%d) = %d, %v, want %d", port, back, ok, display)
		}
	}
	if _, ok := VNCPortForDisplay(VNCDisplayMax + 1); ok {
		t.Fatal("an out-of-range display resolved to a port")
	}
	if _, ok := VNCDisplayForPort(VNCDisplayBasePort + VNCDisplayMax + 1); ok {
		t.Fatal("a port outside the display range resolved to a display")
	}
}

// TestVNCHostIdentityIncludesTheProtocol proves a VNC Host and an SSH Host on
// the same address remain distinct Vault entries.
func TestVNCHostIdentityIncludesTheProtocol(t *testing.T) {
	ssh := Host{Label: "desk", Hostname: "desk.local", Port: 5901, Protocol: ProtocolSSH}
	vnc := Host{Label: "desk", Hostname: "desk.local", Port: 5901, Protocol: ProtocolVNC}
	if ssh.MergeKey() == vnc.MergeKey() {
		t.Fatalf("SSH and VNC merge keys are equal: %q", ssh.MergeKey())
	}
	if !strings.HasPrefix(ssh.MergeKey(), "ssh\x1f") {
		t.Fatalf("SSH merge key = %q, want the protocol first", ssh.MergeKey())
	}
	if !strings.HasPrefix(vnc.MergeKey(), "vnc\x1f") {
		t.Fatalf("VNC merge key = %q, want the protocol first", vnc.MergeKey())
	}
}

// TestVNCHostValidateRefusesSSHOnlyFeatures proves a record that bypassed the
// editor still fails closed for X11 and SFTP rather than carrying settings the
// VNC transport can never apply.
func TestVNCHostValidateRefusesSSHOnlyFeatures(t *testing.T) {
	base := Host{
		Label: "desk", Hostname: "desk.local", Port: 5901,
		Protocol: ProtocolVNC, VNC: NormalizeVNCConfig(nil),
	}

	x11 := base
	x11.X11Forwarding = true
	x11.X11WarningAcknowledged = true
	var validation *ValidationError
	if err := x11.Validate(); !errors.As(err, &validation) ||
		validation.Field != FieldX11 || validation.Rule != RuleUnsupported {
		t.Fatalf("VNC X11 validation = %v, want FieldX11 unsupported", err)
	}

	sftp := base
	sftp.SFTPSudo = true
	if err := sftp.Validate(); !errors.As(err, &validation) ||
		validation.Field != FieldSFTPSudo || validation.Rule != RuleUnsupported {
		t.Fatalf("VNC SFTP validation = %v, want FieldSFTPSudo unsupported", err)
	}
}

// TestVNCHostValidateRefusesAnUnrequestableWebSocketPath proves a saved Host
// cannot carry a path the HTTP upgrade could never request, so an import fails
// at save time instead of at connect time.
func TestVNCHostValidateRefusesAnUnrequestableWebSocketPath(t *testing.T) {
	configuration := NormalizeVNCConfig(&VNCConfig{
		DisplayMode: VNCPort, WebSocket: true, WebSocketPath: "/bad path",
	})
	host := Host{
		Label: "desk", Hostname: "desk.local", Port: 6080,
		Protocol: ProtocolVNC, VNC: configuration,
	}
	var validation *ValidationError
	if err := host.Validate(); !errors.As(err, &validation) ||
		validation.Field != FieldVNCWebSocketPath || validation.Rule != RuleInvalid {
		t.Fatalf("WebSocket path validation = %v, want FieldVNCWebSocketPath invalid", err)
	}
	if _, err := NormalizeDraft(Draft{
		Label: "desk", Hostname: "desk.local", Port: 6080, Protocol: ProtocolVNC,
		VNC: &VNCConfig{DisplayMode: VNCPort, WebSocket: true, WebSocketPath: "/bad path"},
	}); !errors.As(err, &validation) || validation.Field != FieldVNCWebSocketPath {
		t.Fatalf("NormalizeDraft = %v, want the WebSocket path rejected", err)
	}

	valid := host
	valid.VNC = NormalizeVNCConfig(&VNCConfig{DisplayMode: VNCPort, WebSocket: true})
	if err := valid.Validate(); err != nil {
		t.Fatalf("the default WebSocket path did not validate: %v", err)
	}
	valid.VNC = NormalizeVNCConfig(&VNCConfig{
		DisplayMode: VNCPort, WebSocket: true, WebSocketPath: "/vnc?token=a=b",
	})
	if err := valid.Validate(); err != nil {
		t.Fatalf("a custom WebSocket path did not validate: %v", err)
	}

	// Validation normalizes first, so a hand-built Host that skipped
	// NormalizeVNCConfig still treats an empty path as the websockify default.
	handBuilt := host
	handBuilt.VNC = &VNCConfig{DisplayMode: VNCPort, WebSocket: true}
	if err := handBuilt.Validate(); err != nil {
		t.Fatalf("an empty WebSocket path did not validate: %v", err)
	}
}
