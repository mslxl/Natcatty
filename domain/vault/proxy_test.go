package vault

import "testing"

func TestNormalizeProxyProfileDraftAcceptsEveryProxyType(t *testing.T) {
	http, err := NormalizeProxyProfileDraft(ProxyProfileDraft{
		Label:  " office ",
		Config: ProxyConfig{Type: " HTTP ", Host: " proxy.example.com ", Port: 8080, Username: "alice"},
	})
	if err != nil {
		t.Fatalf("HTTP profile: %v", err)
	}
	if http.Label != "office" || http.Config.Type != ProxyHTTP ||
		http.Config.Host != "proxy.example.com" || http.Config.Port != 8080 ||
		http.Config.Username != "alice" {
		t.Fatalf("normalized HTTP profile = %+v", http)
	}

	socks, err := NormalizeProxyProfileDraft(ProxyProfileDraft{
		Label:  "socks",
		Config: ProxyConfig{Type: ProxySOCKS5, Host: "socks.example.com", Port: 1080},
	})
	if err != nil {
		t.Fatalf("SOCKS5 profile: %v", err)
	}
	if socks.Config.Type != ProxySOCKS5 || socks.Config.Port != 1080 {
		t.Fatalf("normalized SOCKS5 profile = %+v", socks)
	}

	command, err := NormalizeProxyProfileDraft(ProxyProfileDraft{
		Label:  "bastion",
		Config: ProxyConfig{Type: ProxyCommand, Command: " ssh -W %h:%p bastion "},
	})
	if err != nil {
		t.Fatalf("command profile: %v", err)
	}
	if command.Config.Command != "ssh -W %h:%p bastion" {
		t.Fatalf("command = %q", command.Config.Command)
	}
}

func TestNormalizeProxyProfileDraftRejectsIncompleteProfiles(t *testing.T) {
	cases := []struct {
		name  string
		draft ProxyProfileDraft
		field Field
		rule  Rule
	}{
		{"missing label", ProxyProfileDraft{Config: ProxyConfig{Type: ProxyHTTP, Host: "p", Port: 8080}}, FieldLabel, RuleRequired},
		{"missing config", ProxyProfileDraft{Label: "empty"}, FieldProxy, RuleRequired},
		{"missing host", ProxyProfileDraft{Label: "p", Config: ProxyConfig{Type: ProxyHTTP, Port: 8080}}, FieldProxy, RuleRequired},
		{"bad port", ProxyProfileDraft{Label: "p", Config: ProxyConfig{Type: ProxyHTTP, Host: "p", Port: 70000}}, FieldProxy, RuleOutOfRange},
		{"missing command", ProxyProfileDraft{Label: "p", Config: ProxyConfig{Type: ProxyCommand}}, FieldProxy, RuleRequired},
		{"http with command", ProxyProfileDraft{Label: "p", Config: ProxyConfig{Type: ProxyHTTP, Host: "p", Port: 1, Command: "nc"}}, FieldProxy, RuleInvalid},
		{"unknown type", ProxyProfileDraft{Label: "p", Config: ProxyConfig{Type: "telnet", Host: "p", Port: 1}}, FieldProxy, RuleUnsupported},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeProxyProfileDraft(test.draft)
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
