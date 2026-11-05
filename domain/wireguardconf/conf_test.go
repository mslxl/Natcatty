package wireguardconf

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"unicode/utf16"
)

// testKey renders a deterministic 32-byte key in wg-quick's base64 form.
func testKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// testKeyN renders a distinct key for a counter, so peer-limit tests can build
// many valid peers.
func testKeyN(n int) string {
	var raw [32]byte
	raw[0] = byte(n >> 24)
	raw[1] = byte(n >> 16)
	raw[2] = byte(n >> 8)
	raw[3] = byte(n)
	return base64.StdEncoding.EncodeToString(raw[:])
}

func minimalConf() string {
	return "[Interface]\n" +
		"PrivateKey = " + testKey(1) + "\n" +
		"Address = 10.0.0.2/32, fd00::2/128\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + testKey(2) + "\n" +
		"AllowedIPs = 10.0.0.0/24, fd00::/64\n" +
		"Endpoint = vpn.example.com:51820\n"
}

func mustParse(t *testing.T, input string) (*Config, []Diagnostic) {
	t.Helper()
	config, diagnostics, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if config == nil {
		t.Fatal("Parse returned a nil Config without an error")
	}
	return config, diagnostics
}

func wantError(t *testing.T, input string, kind Kind, line int, field string) {
	t.Helper()
	config, diagnostics, err := Parse([]byte(input))
	if err == nil {
		t.Fatalf("Parse succeeded, want %s error at line %d (%s)", kind, line, field)
	}
	if config != nil {
		t.Fatalf("a failed Parse returned a partial Config: %+v", config)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("a failed Parse returned diagnostics: %+v", diagnostics)
	}
	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("error type = %T, want *ParseError", err)
	}
	if parseErr.Kind != kind || parseErr.Line != line || parseErr.Field != field {
		t.Fatalf("error = %+v, want kind %s line %d field %q", parseErr, kind, line, field)
	}
}

func haveDiagnostic(diagnostics []Diagnostic, line int, field string, code DiagnosticCode) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Line == line && diagnostic.Field == field && diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestParseMinimalConfig(t *testing.T) {
	config, diagnostics := mustParse(t, minimalConf())
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if EncodeKey(config.Interface.PrivateKey) != testKey(1) {
		t.Fatalf("private key = %q", EncodeKey(config.Interface.PrivateKey))
	}
	if config.Interface.ListenPort != 0 || config.Interface.MTU != 0 {
		t.Fatalf("interface defaults = %+v", config.Interface)
	}
	if len(config.Interface.Addresses) != 2 ||
		config.Interface.Addresses[0].String() != "10.0.0.2/32" ||
		config.Interface.Addresses[1].String() != "fd00::2/128" {
		t.Fatalf("addresses = %v", config.Interface.Addresses)
	}
	if len(config.Peers) != 1 {
		t.Fatalf("peers = %+v", config.Peers)
	}
	peer := config.Peers[0]
	if EncodeKey(peer.PublicKey) != testKey(2) || peer.PresharedKey != nil {
		t.Fatalf("peer keys = %+v", peer)
	}
	if peer.Endpoint.Host != "vpn.example.com" || peer.Endpoint.Port != 51820 ||
		peer.Endpoint.String() != "vpn.example.com:51820" {
		t.Fatalf("endpoint = %+v", peer.Endpoint)
	}
	if len(peer.AllowedIPs) != 2 || peer.AllowedIPs[0].String() != "10.0.0.0/24" ||
		peer.AllowedIPs[1].String() != "fd00::/64" {
		t.Fatalf("allowed IPs = %v", peer.AllowedIPs)
	}
	if peer.PersistentKeepalive != 0 {
		t.Fatalf("keepalive = %d", peer.PersistentKeepalive)
	}
}

func TestParseAcceptsWgQuickSyntax(t *testing.T) {
	t.Run("comments, blank lines, CRLF, and no trailing newline", func(t *testing.T) {
		input := "# leading comment\r\n" +
			"\r\n" +
			"[Interface] # trailing comment\r\n" +
			"PrivateKey = " + testKey(1) + "\r\n" +
			"Address = 10.0.0.2\r\n" +
			"\r\n" +
			"[Peer]\r\n" +
			"PublicKey = " + testKey(2) + "\r\n" +
			"AllowedIPs = 10.0.0.0/24"
		config, diagnostics := mustParse(t, input)
		if len(diagnostics) != 0 {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
		if len(config.Interface.Addresses) != 1 ||
			config.Interface.Addresses[0].String() != "10.0.0.2/32" {
			t.Fatalf("bare address = %v", config.Interface.Addresses)
		}
		if len(config.Peers) != 1 || len(config.Peers[0].AllowedIPs) != 1 {
			t.Fatalf("peers = %+v", config.Peers)
		}
	})

	t.Run("section and key names are case-insensitive", func(t *testing.T) {
		input := "[interface]\n" +
			"privatekey = " + testKey(1) + "\n" +
			"address = 10.0.0.2\n" +
			"[PEER]\n" +
			"PUBLICKEY = " + testKey(2) + "\n" +
			"allowedips = 10.0.0.0/24\n" +
			"ENDPOINT = [fd00::1]:51820\n"
		config, _ := mustParse(t, input)
		if len(config.Peers) != 1 || config.Peers[0].Endpoint.String() != "[fd00::1]:51820" {
			t.Fatalf("IPv6 endpoint = %+v", config.Peers[0].Endpoint)
		}
	})

	t.Run("base64 padding is split after the first equals", func(t *testing.T) {
		config, _ := mustParse(t, minimalConf())
		if !strings.HasSuffix(EncodeKey(config.Interface.PrivateKey), "=") {
			t.Fatal("key should keep its base64 padding")
		}
	})

	t.Run("repeated Interface merges and a scalar last value wins", func(t *testing.T) {
		input := "[Interface]\n" +
			"PrivateKey = " + testKey(1) + "\n" +
			"Address = 10.0.0.2\n" +
			"[Interface]\n" +
			"ListenPort = 51820\n" +
			"PrivateKey = " + testKey(3) + "\n" +
			"[Peer]\n" +
			"PublicKey = " + testKey(2) + "\n" +
			"AllowedIPs = 10.0.0.0/24\n"
		config, diagnostics := mustParse(t, input)
		if EncodeKey(config.Interface.PrivateKey) != testKey(3) ||
			config.Interface.ListenPort != 51820 ||
			len(config.Interface.Addresses) != 1 {
			t.Fatalf("merged interface = %+v", config.Interface)
		}
		if !haveDiagnostic(diagnostics, 6, "PrivateKey", WarnDuplicateScalar) {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
	})

	t.Run("repeated PublicKey in one peer keeps the last value", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\nPublicKey = " + testKey(3) + "\n" +
			"AllowedIPs = 10.0.0.0/24\n"
		config, diagnostics := mustParse(t, input)
		if got := EncodeKey(config.Peers[0].PublicKey); got != testKey(3) {
			t.Fatalf("PublicKey = %q, want the last value", got)
		}
		if !haveDiagnostic(diagnostics, 6, "PublicKey", WarnDuplicateScalar) {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
	})

	t.Run("repeated AllowedIPs append across lines", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\n" +
			"AllowedIPs = 10.0.0.0/24\n" +
			"AllowedIPs = 10.1.0.0/24, 0.0.0.0/0\n"
		config, diagnostics := mustParse(t, input)
		if len(config.Peers[0].AllowedIPs) != 3 {
			t.Fatalf("allowed IPs = %v", config.Peers[0].AllowedIPs)
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Code == WarnDuplicateScalar {
				t.Fatalf("AllowedIPs must append, not warn: %+v", diagnostics)
			}
		}
	})

	t.Run("numeric boundaries and off values", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"ListenPort = 0\nMTU = 576\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\nAllowedIPs = 0.0.0.0/0\n" +
			"PersistentKeepalive = off\n"
		config, _ := mustParse(t, input)
		if config.Interface.ListenPort != 0 || config.Interface.MTU != 576 ||
			config.Peers[0].PersistentKeepalive != 0 {
			t.Fatalf("config = %+v", config)
		}

		input = "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\nMTU = 65535\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\nAllowedIPs = 0.0.0.0/0\n" +
			"PersistentKeepalive = 65535\n"
		config, _ = mustParse(t, input)
		if config.Interface.MTU != 65535 || config.Peers[0].PersistentKeepalive != 65535 {
			t.Fatalf("upper bounds = %+v", config)
		}
	})

	t.Run("preshared key and ignored fields", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"FwMark = 0x1f\nTable = auto\nSaveConfig = TRUE\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\nAllowedIPs = 10.0.0.0/24\n" +
			"PresharedKey = " + testKey(4) + "\n"
		config, diagnostics := mustParse(t, input)
		if config.Peers[0].PresharedKey == nil ||
			EncodeKey(*config.Peers[0].PresharedKey) != testKey(4) {
			t.Fatalf("preshared key = %+v", config.Peers[0].PresharedKey)
		}
		for _, field := range []string{"FwMark", "Table", "SaveConfig"} {
			if !haveDiagnostic(diagnostics, 4, field, WarnIgnoredField) &&
				!haveDiagnostic(diagnostics, 5, field, WarnIgnoredField) &&
				!haveDiagnostic(diagnostics, 6, field, WarnIgnoredField) {
				t.Fatalf("%s ignored diagnostic missing: %+v", field, diagnostics)
			}
		}
	})

	t.Run("DNS keeps IP servers and warns on search domains", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"DNS = 10.0.0.1, 2606:4700:4700::1111, corp.example\n"
		config, diagnostics := mustParse(t, input)
		if len(config.Interface.DNS) != 2 ||
			config.Interface.DNS[0].String() != "10.0.0.1" ||
			config.Interface.DNS[1].String() != "2606:4700:4700::1111" {
			t.Fatalf("DNS = %v", config.Interface.DNS)
		}
		if !haveDiagnostic(diagnostics, 4, "DNS", WarnSearchDomain) {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
	})

	t.Run("empty hooks are no-ops with a warning", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"PreUp =\nPostUp =\n"
		_, diagnostics := mustParse(t, input)
		if !haveDiagnostic(diagnostics, 4, "PreUp", WarnEmptyHook) ||
			!haveDiagnostic(diagnostics, 5, "PostUp", WarnEmptyHook) {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
	})

	t.Run("host bits are masked with a warning", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2/24\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\nAllowedIPs = 10.0.0.1/24\n"
		config, diagnostics := mustParse(t, input)
		if config.Peers[0].AllowedIPs[0].String() != "10.0.0.0/24" {
			t.Fatalf("allowed IP = %v", config.Peers[0].AllowedIPs)
		}
		// Address keeps its host bits: the netstack uses only the host address.
		if config.Interface.Addresses[0].String() != "10.0.0.2/24" {
			t.Fatalf("address = %v", config.Interface.Addresses)
		}
		if !haveDiagnostic(diagnostics, 6, "AllowedIPs", WarnHostBits) {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
	})

	t.Run("empty AllowedIPs is a no-traffic warning", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\nAllowedIPs =\n"
		config, diagnostics := mustParse(t, input)
		if len(config.Peers[0].AllowedIPs) != 0 {
			t.Fatalf("allowed IPs = %v", config.Peers[0].AllowedIPs)
		}
		if !haveDiagnostic(diagnostics, 6, "AllowedIPs", WarnEmptyAllowedIPs) {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
	})
}

func TestParseDiagnosticMatrix(t *testing.T) {
	base := func(interfaceExtra, peerExtra string) string {
		return "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			interfaceExtra +
			"[Peer]\nPublicKey = " + testKey(2) + "\n" + peerExtra
	}
	tests := []struct {
		name  string
		input string
		line  int
		field string
		code  DiagnosticCode
	}{
		{"FwMark", base("FwMark = 0\n", ""), 4, "FwMark", WarnIgnoredField},
		{"Table", base("Table = off\n", ""), 4, "Table", WarnIgnoredField},
		{"Table number", base("Table = 1234\n", ""), 4, "Table", WarnIgnoredField},
		{"SaveConfig", base("SaveConfig = false\n", ""), 4, "SaveConfig", WarnIgnoredField},
		{"search domain", base("DNS = 1.1.1.1, corp.example\n", ""), 4, "DNS", WarnSearchDomain},
		{"host bits", base("", "AllowedIPs = 10.0.0.1/24\n"), 6, "AllowedIPs", WarnHostBits},
		{"empty allowed IPs", base("", "AllowedIPs =\n"), 6, "AllowedIPs", WarnEmptyAllowedIPs},
		{"duplicate ListenPort", base("ListenPort = 1\nListenPort = 2\n", ""), 5, "ListenPort", WarnDuplicateScalar},
		{"duplicate Endpoint", base("", "Endpoint = a.example:1\nEndpoint = b.example:2\n"),
			7, "Endpoint", WarnDuplicateScalar},
		{"empty hook", base("PostDown =\n", ""), 4, "PostDown", WarnEmptyHook},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, diagnostics := mustParse(t, test.input)
			if config == nil {
				t.Fatal("nil config")
			}
			if !haveDiagnostic(diagnostics, test.line, test.field, test.code) {
				t.Fatalf("diagnostics = %+v, want line %d field %s code %s",
					diagnostics, test.line, test.field, test.code)
			}
		})
	}
}

func TestParseRejectsInvalidInput(t *testing.T) {
	validPeer := "[Peer]\nPublicKey = " + testKey(2) + "\nAllowedIPs = 10.0.0.0/24\n"
	base := func(interfaceExtra, peerExtra string) string {
		return "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			interfaceExtra + validPeer + peerExtra
	}
	tests := []struct {
		name  string
		input string
		kind  Kind
		line  int
		field string
	}{
		{"empty file", "", ErrMissingRequired, 1, "PrivateKey"},
		{"comments only", "# nothing\n\n", ErrMissingRequired, 1, "PrivateKey"},
		{"missing equals", "[Interface]\nPrivateKey\n", ErrSyntax, 2, "key"},
		{"key outside a section", "PrivateKey = " + testKey(1) + "\n", ErrSyntax, 1, "PrivateKey"},
		{"key with internal whitespace", "[Interface]\nPrivate Key = " + testKey(1) + "\n",
			ErrSyntax, 2, "key"},
		{"unknown key in Interface", base("Bogus = 1\n", ""), ErrUnknownKey, 4, "Bogus"},
		{"unknown key in Peer", base("", "Bogus = 1\n"), ErrUnknownKey, 7, "Bogus"},
		{"peer key in Interface", base("PublicKey = "+testKey(2)+"\n", ""),
			ErrUnknownKey, 4, "PublicKey"},
		{"interface key in Peer", base("", "MTU = 1420\n"), ErrUnknownKey, 7, "MTU"},
		{"unknown section", "[WireGuard]\nPrivateKey = " + testKey(1) + "\n",
			ErrUnknownSection, 1, "section"},
		{"missing PrivateKey", "[Interface]\nAddress = 10.0.0.2\n" + validPeer,
			ErrMissingRequired, 1, "PrivateKey"},
		{"missing peer PublicKey", "[Interface]\nPrivateKey = " + testKey(1) +
			"\nAddress = 10.0.0.2\n[Peer]\nAllowedIPs = 10.0.0.0/24\n",
			ErrMissingRequired, 4, "PublicKey"},
		{"duplicate peer PublicKey", "[Interface]\nPrivateKey = " + testKey(1) +
			"\nAddress = 10.0.0.2\n[Peer]\nPublicKey = " + testKey(2) +
			"\n[Peer]\nPublicKey = " + testKey(2) + "\n",
			ErrDuplicatePeer, 7, "PublicKey"},
		{"invalid base64 character", "[Interface]\nPrivateKey = !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!\n",
			ErrSyntax, 2, "PrivateKey"},
		{"base64 wrong length", "[Interface]\nPrivateKey = " + strings.Repeat("A", 43) + "\n",
			ErrSyntax, 2, "PrivateKey"},
		{"base64 decodes to 31 bytes", "[Interface]\nPrivateKey = " +
			base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31)) + "\n",
			ErrSyntax, 2, "PrivateKey"},
		{"invalid private key empty", "[Interface]\nPrivateKey =\n", ErrSyntax, 2, "PrivateKey"},
		{"invalid address prefix", base("Address = 10.0.0.0/33\n", ""), ErrSyntax, 4, "Address"},
		{"address is a hostname", base("Address = vpn.example.com\n", ""), ErrSyntax, 4, "Address"},
		{"empty address", base("Address =\n", ""), ErrSyntax, 4, "Address"},
		{"empty DNS", base("DNS =\n", ""), ErrSyntax, 4, "DNS"},
		{"invalid allowed IP prefix", base("", "AllowedIPs = 10.0.0.0/33\n"),
			ErrSyntax, 7, "AllowedIPs"},
		{"allowed IP is a hostname", base("", "AllowedIPs = vpn.example.com\n"),
			ErrSyntax, 7, "AllowedIPs"},
		{"consecutive AllowedIPs comma", base("", "AllowedIPs = 10.0.0.0/8, , 192.168.0.0/16\n"),
			ErrSyntax, 7, "AllowedIPs"},
		{"trailing AllowedIPs comma", base("", "AllowedIPs = 10.0.0.0/8,\n"),
			ErrSyntax, 7, "AllowedIPs"},
		{"incremental AllowedIPs add", base("", "AllowedIPs = +10.0.0.0/8\n"),
			ErrUnsupported, 7, "AllowedIPs"},
		{"incremental AllowedIPs remove", base("", "AllowedIPs = -10.0.0.0/8\n"),
			ErrUnsupported, 7, "AllowedIPs"},
		{"hook PreUp", base("PreUp = iptables -A INPUT -j DROP\n", ""),
			ErrUnsupported, 4, "PreUp"},
		{"hook PostDown", base("PostDown = rm -f /tmp/x\n", ""),
			ErrUnsupported, 4, "PostDown"},
		{"invalid listen port", base("ListenPort = http\n", ""), ErrSyntax, 4, "ListenPort"},
		{"listen port too large", base("ListenPort = 65536\n", ""), ErrSyntax, 4, "ListenPort"},
		{"listen port negative", base("ListenPort = -1\n", ""), ErrSyntax, 4, "ListenPort"},
		{"MTU too small", base("MTU = 575\n", ""), ErrSyntax, 4, "MTU"},
		{"MTU zero", base("MTU = 0\n", ""), ErrSyntax, 4, "MTU"},
		{"MTU too large", base("MTU = 65536\n", ""), ErrSyntax, 4, "MTU"},
		{"keepalive too large", base("", "PersistentKeepalive = 65536\n"),
			ErrSyntax, 7, "PersistentKeepalive"},
		{"keepalive is a service", base("", "PersistentKeepalive = maybe\n"),
			ErrSyntax, 7, "PersistentKeepalive"},
		{"endpoint missing port", base("", "Endpoint = vpn.example.com\n"),
			ErrSyntax, 7, "Endpoint"},
		{"endpoint service port", base("", "Endpoint = vpn.example.com:http\n"),
			ErrSyntax, 7, "Endpoint"},
		{"endpoint port zero", base("", "Endpoint = vpn.example.com:0\n"),
			ErrSyntax, 7, "Endpoint"},
		{"unbracketed IPv6 endpoint", base("", "Endpoint = fd00::1:51820\n"),
			ErrSyntax, 7, "Endpoint"},
		{"bracketed hostname", base("", "Endpoint = [vpn.example.com]:51820\n"),
			ErrSyntax, 7, "Endpoint"},
		{"bracketed IPv6 without port", base("", "Endpoint = [fd00::1]\n"),
			ErrSyntax, 7, "Endpoint"},
		{"empty public key", base("", "AllowedIPs = 10.0.0.0/24\nPublicKey =\n"),
			ErrSyntax, 8, "PublicKey"},
		{"empty preshared key", base("", "PresharedKey =\n"), ErrSyntax, 7, "PresharedKey"},
		{"invalid encoding", "[Interface]\nPrivateKey = \xff\n", ErrEncoding, 1, "encoding"},
		{"NUL byte", "[Interface]\nPrivateKey = " + testKey(1) + "\x00\n", ErrEncoding, 1, "encoding"},
		{"UTF-16 odd length", string([]byte{0xFF, 0xFE, 0x41}), ErrEncoding, 1, "encoding"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wantError(t, test.input, test.kind, test.line, test.field)
		})
	}
}

func TestParseLimits(t *testing.T) {
	t.Run("file size", func(t *testing.T) {
		wantError(t, strings.Repeat("a", MaxFileBytes+1), ErrLimit, 1, "file")
	})
	t.Run("line length", func(t *testing.T) {
		wantError(t, strings.Repeat("a", MaxLineBytes+1), ErrLimit, 1, "line")
	})
	t.Run("peer count", func(t *testing.T) {
		var builder strings.Builder
		builder.WriteString("[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n")
		for index := 0; index <= MaxPeers; index++ {
			builder.WriteString("[Peer]\nPublicKey = " + testKeyN(index+1) + "\n")
		}
		wantError(t, builder.String(), ErrLimit, 4+MaxPeers*2, "Peer")
	})
	t.Run("address count", func(t *testing.T) {
		values := make([]string, 0, MaxAddresses+1)
		for index := 0; index <= MaxAddresses; index++ {
			values = append(values, fmt.Sprintf("10.0.0.%d/32", index))
		}
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = " +
			strings.Join(values, ", ") + "\n"
		wantError(t, input, ErrLimit, 3, "Address")
	})
	t.Run("DNS count", func(t *testing.T) {
		values := make([]string, 0, MaxDNSServers+1)
		for index := 0; index <= MaxDNSServers; index++ {
			values = append(values, fmt.Sprintf("10.0.0.%d", index))
		}
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nDNS = " +
			strings.Join(values, ", ") + "\n"
		wantError(t, input, ErrLimit, 3, "DNS")
	})
	t.Run("DNS search domain count", func(t *testing.T) {
		values := make([]string, 0, MaxDNSServers+1)
		for index := 0; index <= MaxDNSServers; index++ {
			values = append(values, fmt.Sprintf("host%d.example", index))
		}
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nDNS = " +
			strings.Join(values, ", ") + "\n"
		wantError(t, input, ErrLimit, 3, "DNS")
	})
	t.Run("AllowedIPs count", func(t *testing.T) {
		values := make([]string, 0, MaxAllowedIPs+1)
		for index := 0; index <= MaxAllowedIPs; index++ {
			values = append(values, "10.0.0.0/8")
		}
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\nAllowedIPs = " +
			strings.Join(values, ",") + "\n"
		wantError(t, input, ErrLimit, 6, "AllowedIPs")
	})
	t.Run("endpoint host length", func(t *testing.T) {
		input := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
			"[Peer]\nPublicKey = " + testKey(2) + "\nEndpoint = " +
			strings.Repeat("a", MaxEndpointHostBytes+1) + ":51820\n"
		wantError(t, input, ErrLimit, 6, "Endpoint")
	})
}

func TestParseNeverEchoesSecretValues(t *testing.T) {
	const privateSecret = "SUPER-SECRET-PRIVATE-VALUE"
	const presharedSecret = "SUPER-SECRET-PRESHARED-VALUE"
	const hookSecret = "SUPER-SECRET-HOOK-VALUE"
	tests := []struct {
		name   string
		input  string
		secret string
		kind   Kind
	}{
		{
			name: "invalid private key",
			input: "[Interface]\nPrivateKey = " + privateSecret + "\nAddress = 10.0.0.2\n" +
				"[Peer]\nPublicKey = " + testKey(2) + "\n",
			secret: privateSecret,
			kind:   ErrSyntax,
		},
		{
			name: "invalid preshared key",
			input: "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
				"[Peer]\nPublicKey = " + testKey(2) + "\nPresharedKey = " + presharedSecret + "\n",
			secret: presharedSecret,
			kind:   ErrSyntax,
		},
		{
			name: "hook value",
			input: "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n" +
				"PostUp = " + hookSecret + "\n",
			secret: hookSecret,
			kind:   ErrUnsupported,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, diagnostics, err := Parse([]byte(test.input))
			if err == nil {
				t.Fatal("Parse succeeded")
			}
			parseErr, ok := err.(*ParseError)
			if !ok || parseErr.Kind != test.kind {
				t.Fatalf("error = %v, want kind %s", err, test.kind)
			}
			for _, text := range []string{err.Error(), fmt.Sprintf("%+v", parseErr)} {
				if strings.Contains(text, test.secret) {
					t.Fatalf("error text leaked a secret: %q", text)
				}
			}
			if len(diagnostics) != 0 {
				t.Fatalf("diagnostics = %+v", diagnostics)
			}
		})
	}
}

func TestParseUTF16BOM(t *testing.T) {
	source := "[Interface]\r\nPrivateKey = " + testKey(1) + "\r\nAddress = 10.0.0.2\r\n" +
		"[Peer]\r\nPublicKey = " + testKey(2) + "\r\nAllowedIPs = 10.0.0.0/24\r\n"
	units := utf16.Encode([]rune(source))
	for _, order := range []struct {
		name string
		bom  []byte
		bo   binary.ByteOrder
	}{
		{"little endian", []byte{0xFF, 0xFE}, binary.LittleEndian},
		{"big endian", []byte{0xFE, 0xFF}, binary.BigEndian},
	} {
		t.Run(order.name, func(t *testing.T) {
			data := append([]byte(nil), order.bom...)
			for _, unit := range units {
				var raw [2]byte
				order.bo.PutUint16(raw[:], unit)
				data = append(data, raw[:]...)
			}
			config, diagnostics, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(diagnostics) != 0 {
				t.Fatalf("diagnostics = %+v", diagnostics)
			}
			if len(config.Peers) != 1 || EncodeKey(config.Peers[0].PublicKey) != testKey(2) {
				t.Fatalf("config = %+v", config)
			}
		})
	}
}

func TestEndpointStringBracketsIPv6(t *testing.T) {
	tests := []struct {
		endpoint Endpoint
		want     string
	}{
		{Endpoint{Host: "vpn.example.com", Port: 51820}, "vpn.example.com:51820"},
		{Endpoint{Host: "10.0.0.1", Port: 51820}, "10.0.0.1:51820"},
		{Endpoint{Host: "fd00::1", Port: 51820}, "[fd00::1]:51820"},
	}
	for _, test := range tests {
		if got := test.endpoint.String(); got != test.want {
			t.Errorf("Endpoint%+v.String() = %q, want %q", test.endpoint, got, test.want)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(minimalConf()))
	f.Add([]byte(""))
	f.Add([]byte("[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2\n"))
	f.Add([]byte("[Peer]\nPublicKey = x\n"))
	f.Add([]byte{0xFF, 0xFE, 0x00})
	f.Add([]byte("Address = 10.0.0.1/24\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		config, diagnostics, err := Parse(data)
		if err != nil {
			if config != nil {
				t.Fatalf("failed Parse returned a Config: %+v", config)
			}
			if len(diagnostics) != 0 {
				t.Fatalf("failed Parse returned diagnostics: %+v", diagnostics)
			}
			return
		}
		if config == nil {
			t.Fatal("successful Parse returned a nil Config")
		}
		if len(config.Peers) > MaxPeers ||
			len(config.Interface.Addresses) > MaxAddresses ||
			len(config.Interface.DNS) > MaxDNSServers {
			t.Fatalf("limits were not enforced: %d peers, %d addresses, %d DNS",
				len(config.Peers), len(config.Interface.Addresses), len(config.Interface.DNS))
		}
		if config.Interface.MTU != 0 && (config.Interface.MTU < 576) {
			t.Fatalf("MTU below the minimum: %d", config.Interface.MTU)
		}
		seen := make(map[[32]byte]bool)
		for _, peer := range config.Peers {
			if seen[peer.PublicKey] {
				t.Fatal("duplicate peer public key survived parsing")
			}
			seen[peer.PublicKey] = true
			if len(peer.AllowedIPs) > MaxAllowedIPs {
				t.Fatalf("peer has %d AllowedIPs", len(peer.AllowedIPs))
			}
			for _, prefix := range peer.AllowedIPs {
				if prefix != prefix.Masked() {
					t.Fatalf("AllowedIPs entry is not masked: %v", prefix)
				}
				if !prefix.IsValid() {
					t.Fatalf("invalid prefix: %v", prefix)
				}
			}
			if peer.Endpoint.Host != "" {
				if peer.Endpoint.Port == 0 {
					t.Fatal("endpoint kept a zero port")
				}
				if _, err := netip.ParseAddr(peer.Endpoint.Host); err != nil &&
					strings.ContainsAny(peer.Endpoint.Host, " \t[]/\\") {
					t.Fatalf("endpoint host is malformed: %q", peer.Endpoint.Host)
				}
			}
		}
		for _, address := range config.Interface.Addresses {
			if !address.IsValid() {
				t.Fatalf("invalid address: %v", address)
			}
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Line < 1 || diagnostic.Field == "" || diagnostic.Code == "" {
				t.Fatalf("malformed diagnostic: %+v", diagnostic)
			}
		}
	})
}
