package vault

import (
	"errors"
	"strings"
	"testing"
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

func TestHostValidationAcceptsANormalizedHost(t *testing.T) {
	host := Host{
		ID:       "01HOST",
		Label:    "web",
		Protocol: ProtocolSSH,
		Hostname: "example.com",
		Port:     22,
		Tags:     []string{"prod"},
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
