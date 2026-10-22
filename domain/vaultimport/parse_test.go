package vaultimport

import (
	"crypto/aes"
	"crypto/des"
	"crypto/sha512"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(content)
}

func findIssue(issues []Issue, code IssueCode) *Issue {
	for index := range issues {
		if issues[index].Code == code {
			return &issues[index]
		}
	}
	return nil
}

func requireIssue(t *testing.T, result Result, code IssueCode) Issue {
	t.Helper()
	item := findIssue(result.Issues, code)
	if item == nil {
		t.Fatalf("missing issue %q in %+v", code, result.Issues)
	}
	return *item
}

func TestParseCSVFixtureHandlesBOMCRLFAndQuoting(t *testing.T) {
	text := fixture(t, "netcatty.csv")
	if !strings.HasPrefix(text, "\xef\xbb\xbf") {
		t.Fatal("fixture must start with a UTF-8 BOM")
	}
	if !strings.Contains(text, "\r\n") {
		t.Fatal("fixture must use CRLF row endings")
	}

	result := Parse(FormatCSV, text, Options{})
	if len(result.Records) != 5 {
		t.Fatalf("records = %d, want 5: %+v", len(result.Records), result.Records)
	}

	web := result.Records[0]
	if web.Row != 2 || web.Hostname != "10.0.0.10" || web.Protocol != "ssh" ||
		web.Port != 2222 || web.Username != "deploy" || web.Password != "s3cr3t" {
		t.Fatalf("unexpected first record: %+v", web)
	}
	if web.Label != `Web, "edge"` {
		t.Fatalf("label = %q, want quoted label", web.Label)
	}
	if web.Notes != "First line\nSecond line" {
		t.Fatalf("notes = %q, want embedded newline", web.Notes)
	}
	if web.Group != "Acme/Prod" || strings.Join(web.Tags, ",") != "web,prod" {
		t.Fatalf("group/tags = %q/%v", web.Group, web.Tags)
	}
	if web.KeyPath != "=weird/id_ed25519" {
		t.Fatalf("key path = %q, want decoded marked value", web.KeyPath)
	}
	if web.Passphrase != "pa ss" {
		t.Fatalf("passphrase = %q", web.Passphrase)
	}
	if web.Proxy == nil || web.Proxy.Type != "socks5" || web.Proxy.Host != "127.0.0.1" ||
		web.Proxy.Port != 1080 || web.Proxy.Username != "proxyuser" || web.Proxy.Password != "p@ss" {
		t.Fatalf("proxy = %+v", web.Proxy)
	}

	formula := result.Records[1]
	if formula.Label != `=HYPERLINK("http://example.test")` {
		t.Fatalf("formula label must not be evaluated or changed, got %q", formula.Label)
	}
	if formula.Notes != "=SUM(A1:A2)" {
		t.Fatalf("formula guard must be stripped on import, got %q", formula.Notes)
	}

	db := result.Records[2]
	if db.Protocol != "telnet" || db.Proxy == nil || db.Proxy.Type != "http" ||
		db.Proxy.Username != "user" || db.Proxy.Password != "pass" || db.Proxy.Port != 3128 {
		t.Fatalf("unexpected telnet record: %+v", db)
	}

	invalidHost := requireIssue(t, result, IssueInvalidHostname)
	if invalidHost.Row != 5 {
		t.Fatalf("invalid hostname row = %d, want 5", invalidHost.Row)
	}
	invalidPort := requireIssue(t, result, IssueInvalidPort)
	if invalidPort.Row != 6 || invalidPort.Value != "notaport" {
		t.Fatalf("invalid port issue = %+v", invalidPort)
	}
	duplicate := requireIssue(t, result, IssueDuplicateInFile)
	if duplicate.Row != 7 {
		t.Fatalf("duplicate row = %d, want 7", duplicate.Row)
	}
}

func TestCSVFormulaGuardRoundTrips(t *testing.T) {
	cases := []struct{ value, guarded string }{
		{"=1+1", "'=1+1"},
		{"+call()", "'+call()"},
		{"-2", "'-2"},
		{"@user", "'@user"},
		{"\tcmd", "'\tcmd"},
		{"safe", "safe"},
		{"'quoted", "'quoted"},
	}
	for _, test := range cases {
		if got := NeutralizeFormula(test.value); got != test.guarded {
			t.Errorf("NeutralizeFormula(%q) = %q, want %q", test.value, got, test.guarded)
		}
		if got := StripFormulaGuard(test.guarded); got != test.value {
			t.Errorf("StripFormulaGuard(%q) = %q, want %q", test.guarded, got, test.value)
		}
	}

	if got := EscapeCSVField(`a,"b"`+"\n", true); got != `"a,""b"""`+"\n" {
		// The trailing newline itself forces quoting, so the expected value
		// begins and ends with a quote.
		want := "\"a,\"\"b\"\"\n\""
		if got != want {
			t.Fatalf("EscapeCSVField = %q, want %q", got, want)
		}
	}
	if got := EscapeCSVField("=SUM(A1)", false); got != "=SUM(A1)" {
		t.Fatalf("credential field must stay byte-exact, got %q", got)
	}
	if got := EscapeCSVField("=SUM(A1)", true); got != "'=SUM(A1)" {
		t.Fatalf("guarded field = %q", got)
	}
}

func TestMarkedFieldEncoding(t *testing.T) {
	encoded := EncodeMarkedField("=weird/id_ed25519", MarkerKeyPath)
	if encoded != MarkerKeyPath+"%3Dweird%2Fid_ed25519" {
		t.Fatalf("encoded = %q", encoded)
	}
	if decoded := DecodeMarkedField(encoded, MarkerKeyPath); decoded != "=weird/id_ed25519" {
		t.Fatalf("decoded = %q", decoded)
	}
	if decoded := DecodeMarkedField("plain", MarkerKeyPath); decoded != "plain" {
		t.Fatalf("plain value changed: %q", decoded)
	}
}

func TestParseCSVKeepsCRLFInsideQuotedField(t *testing.T) {
	text := "Hostname,Notes,Label\r\n" +
		"web.example.test,\"line1\r\nline2\",\"quoted, label\"\r\n" +
		"db.example.test,plain,second\r\n"
	result := Parse(FormatCSV, text, Options{})
	if len(result.Issues) != 0 {
		t.Fatalf("issues = %+v", result.Issues)
	}
	if len(result.Records) != 2 {
		t.Fatalf("records = %d, want 2: %+v", len(result.Records), result.Records)
	}
	if got := result.Records[0].Notes; got != "line1\r\nline2" {
		t.Fatalf("notes = %q, want embedded CRLF", got)
	}
	if got := result.Records[0].Label; got != "quoted, label" {
		t.Fatalf("label = %q", got)
	}
	if result.Records[1].Hostname != "db.example.test" || result.Records[1].Notes != "plain" {
		t.Fatalf("second record = %+v", result.Records[1])
	}
}

func TestParseCSVReportsRowsWithoutAHostname(t *testing.T) {
	text := "Hostname,Label,Notes\n,Local shell,encoded shell spec\n"
	result := Parse(FormatCSV, text, Options{})
	item := requireIssue(t, result, IssueMissingHostname)
	if item.Row != 2 || item.Severity != SeverityWarning {
		t.Fatalf("issue = %+v, want a warning on row 2", item)
	}
	if len(result.Records) != 0 {
		t.Fatalf("records = %+v, want none", result.Records)
	}
}

func TestParseCSVRejectsUnknownProtocolInsteadOfCoercingToSSH(t *testing.T) {
	text := "Hostname,Protocol,Port\nserial.example.test,serial,22\n"
	result := Parse(FormatCSV, text, Options{})
	item := requireIssue(t, result, IssueUnsupportedProtocol)
	if item.Row != 2 || item.Value != "serial" {
		t.Fatalf("issue = %+v", item)
	}
	if len(result.Records) != 0 {
		t.Fatalf("unknown protocol became %+v", result.Records)
	}
}

func TestParseCSVReportsALocalShellSpecItCannotStore(t *testing.T) {
	text := "Hostname,Protocol,LocalShell\nshell.example.test,local," +
		MarkerLocalShell + "%7B%22shell%22%3A%22pwsh%22%7D\n"
	result := Parse(FormatCSV, text, Options{})
	item := requireIssue(t, result, IssueUnsupportedEntry)
	if item.Row != 2 || item.Field != "localshell" {
		t.Fatalf("issue = %+v", item)
	}
	if len(result.Records) != 1 || result.Records[0].Protocol != "local" {
		t.Fatalf("records = %+v", result.Records)
	}
}

func TestParseSecureCRTReportsASessionWithoutAHostname(t *testing.T) {
	text := "S:\"Session Name\"=\"No host\"\nS:\"Username\"=\"root\"\n"
	result := Parse(FormatSecureCRT, text, Options{FileName: "sessions.ini"})
	item := requireIssue(t, result, IssueMissingHostname)
	if item.Row != 1 {
		t.Fatalf("issue = %+v, want row 1", item)
	}
	if len(result.Records) != 0 {
		t.Fatalf("records = %+v, want none", result.Records)
	}
}

func TestParseCSVRejectsMalformedQuoting(t *testing.T) {
	result := Parse(FormatCSV, fixture(t, "malformed.csv"), Options{})
	item := requireIssue(t, result, IssueCSVQuoting)
	if item.Severity != SeverityError || item.Row != 2 {
		t.Fatalf("issue = %+v, want error on line 2", item)
	}
}

func TestParseCSVMissingHostnameColumn(t *testing.T) {
	result := Parse(FormatCSV, "Name,Port\nweb,22\n", Options{})
	item := requireIssue(t, result, IssueCSVHeader)
	if item.Severity != SeverityError {
		t.Fatalf("severity = %q", item.Severity)
	}
}

func TestParseCSVAcceptsTabSeparated(t *testing.T) {
	result := Parse(FormatCSV, "Hostname\tPort\tUsername\nweb.example.test\t2222\tdeploy\n", Options{})
	if len(result.Records) != 1 || result.Records[0].Hostname != "web.example.test" ||
		result.Records[0].Port != 2222 || result.Records[0].Username != "deploy" {
		t.Fatalf("records = %+v", result.Records)
	}
}

func TestParsePuTTYFixture(t *testing.T) {
	result := Parse(FormatPuTTY, fixture(t, "putty.reg"), Options{})
	if len(result.Records) != 2 {
		t.Fatalf("records = %d, want 2: %+v", len(result.Records), result)
	}
	web := result.Records[0]
	if web.Label != "Prod Web" || web.Hostname != "web.prod.example.test" ||
		web.Username != "deploy" || web.Port != 2222 || web.Protocol != "ssh" {
		t.Fatalf("web record = %+v", web)
	}
	legacy := result.Records[1]
	if legacy.Label != "Legacy" || legacy.Port != 23 || legacy.Protocol != "telnet" {
		t.Fatalf("legacy record = %+v", legacy)
	}
	missing := requireIssue(t, result, IssueMissingHostname)
	if missing.Row == 0 {
		t.Fatalf("missing hostname issue has no row: %+v", missing)
	}
}

func TestParseMobaXtermFixture(t *testing.T) {
	result := Parse(FormatMobaXterm, fixture(t, "mobaxterm.ini"), Options{})
	if len(result.Records) != 3 {
		t.Fatalf("records = %d, want 3: %+v", len(result.Records), result)
	}
	web := result.Records[0]
	if web.Label != "web-1" || web.Hostname != "web1.example.test" ||
		web.Username != "deploy" || web.Port != 22 || web.Group != "Ops/Servers" {
		t.Fatalf("web record = %+v", web)
	}
	db := result.Records[1]
	if db.Port != 2200 || db.Username != "postgres" {
		t.Fatalf("db record = %+v", db)
	}
	lab := result.Records[2]
	if lab.Label != "lab-1" || lab.Group != "Lab" {
		t.Fatalf("lab record = %+v", lab)
	}
	unsupported := requireIssue(t, result, IssueUnsupportedProtocol)
	if unsupported.Detail != "91" {
		t.Fatalf("unsupported issue = %+v, want RDP type 91", unsupported)
	}
	requireIssue(t, result, IssuePasswordUndecrypt)
}

func encryptMobaWeak(plaintext string, key []byte) string {
	working := append([]byte(nil), key...)
	out := make([]byte, 0, len(plaintext)*2)
	for _, value := range []byte(plaintext) {
		out = append(out, working[value&0x0f])
		rotateWeakKey(working)
		out = append(out, working[value>>4])
		rotateWeakKey(working)
	}
	return string(out)
}

func TestParseMobaXtermWeakCipherPassword(t *testing.T) {
	key := craftMobaConnectionKey("systemuser", "systemhost", "deploy", "web.example.test")
	ciphertext := encryptMobaWeak("weaker-secret", key)
	text := "[Bookmarks]\n" +
		"SubRep=Lab\n" +
		"web=#109#0%web.example.test%22%deploy%%-1%-1%%%%%0%0%0%%%-1%0%0%0%%1080%%0%0%1%#MobaFont%10%0%0%-1#0# #-1\n" +
		"[Misc]\nMPSetAccount=systemuser\nMPSetComputer=systemhost\n" +
		"[Passwords]\ndeploy@web.example.test=" + ciphertext + "\n"

	result := Parse(FormatMobaXterm, text, Options{})
	if len(result.Records) != 1 {
		t.Fatalf("records = %+v", result.Records)
	}
	if result.Records[0].Password != "weaker-secret" {
		t.Fatalf("password = %q, want weak-cipher plaintext", result.Records[0].Password)
	}
	if findIssue(result.Issues, IssuePasswordUndecrypt) != nil {
		t.Fatalf("unexpected undecryptable issue: %+v", result.Issues)
	}
}

func TestParseMobaXtermMasterPassword(t *testing.T) {
	digest := sha512.Sum512([]byte("hunter2"))
	key := digest[:32]
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, 16)
	block.Encrypt(iv, iv)
	ciphertext, ok := aesCFB8Encrypt(key, iv, []byte("master-secret"))
	if !ok {
		t.Fatal("encrypt failed")
	}
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	text := "[Bookmarks]\n" +
		"SubRep=Lab\n" +
		"web=#109#0%web.example.test%22%deploy%%-1%-1%%%%%0%0%0%%%-1%0%0%0%%1080%%0%0%1%#MobaFont%10%0%0%-1#0# #-1\n" +
		"[Passwords]\ndeploy@web.example.test=" + encoded + "\n"

	result := Parse(FormatMobaXterm, text, Options{MasterPassword: "hunter2"})
	if len(result.Records) != 1 || result.Records[0].Password != "master-secret" {
		t.Fatalf("records = %+v issues=%+v", result.Records, result.Issues)
	}

	wrong := Parse(FormatMobaXterm, text, Options{MasterPassword: "wrong"})
	if wrong.Records[0].Password != "" {
		t.Fatalf("wrong master password must not yield plaintext: %+v", wrong.Records[0])
	}
	requireIssue(t, wrong, IssuePasswordUndecrypt)
}

func TestParseMobaXtermRequiresMasterPassword(t *testing.T) {
	text := "[SessPass]\n[Bookmarks]\n" +
		"subnet=#109#0%web.example.test%22%deploy%%-1%-1%%%%%0%0%0%%%-1%0%0%0%%1080%%0%0%1%#MobaFont%10%0%0%-1#0# #-1\n" +
		"[Passwords]\ndeploy@web.example.test=AAAA\n"
	result := Parse(FormatMobaXterm, text, Options{})
	requireIssue(t, result, IssueMasterPassword)
}

func encodeFinalShellForTest(t *testing.T, header []byte, plaintext string) string {
	t.Helper()
	key, ok := deriveFinalShellKey(header)
	if !ok {
		t.Fatal("derive key failed")
	}
	block, err := des.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(plaintext)
	padding := 8 - len(body)%8
	for index := 0; index < padding; index++ {
		body = append(body, byte(padding))
	}
	out := append([]byte(nil), header...)
	for offset := 0; offset < len(body); offset += 8 {
		encrypted := make([]byte, 8)
		block.Encrypt(encrypted, body[offset:offset+8])
		out = append(out, encrypted...)
	}
	return base64.StdEncoding.EncodeToString(out)
}

func TestParseFinalShellFixture(t *testing.T) {
	result := Parse(FormatFinalShell, fixture(t, "finalshell.json"), Options{})
	if len(result.Records) != 1 {
		t.Fatalf("records = %+v issues=%+v", result.Records, result.Issues)
	}
	record := result.Records[0]
	if record.Hostname != "fs.example.test" || record.Port != 2222 ||
		record.Username != "tester" || record.Label != "FinalShell Web" ||
		record.Notes != "Imported from FinalShell" || record.Protocol != "ssh" {
		t.Fatalf("record = %+v", record)
	}
}

func TestFinalShellPasswordRoundTrip(t *testing.T) {
	header := []byte{0x04, 0, 0, 0, 0, 5, 0, 0}
	encoded := encodeFinalShellForTest(t, header, "fs-secret")
	text := `{"name":"fs","host":"fs.example.test","port":22,"user_name":"root","conection_type":100,"password":"` + encoded + `"}`
	result := Parse(FormatFinalShell, text, Options{})
	if len(result.Records) != 1 || result.Records[0].Password != "fs-secret" {
		t.Fatalf("records = %+v issues=%+v", result.Records, result.Issues)
	}

	if _, ok := decodeFinalShellPassword("bm90IGEgY2lwaGVy"); ok {
		t.Fatal("garbage must not decrypt")
	}
	if _, ok := decodeFinalShellPassword("!!!not base64!!!"); ok {
		t.Fatal("invalid base64 must not decrypt")
	}
}

func TestFinalShellNestedAndMalformed(t *testing.T) {
	nested := `{"children":[{"host":"a.example.test","port":22,"conection_type":100},` +
		`{"host":"b.example.test","port":"2200","conection_type":100},` +
		`{"host":"rdp.example.test","conection_type":91}]}`
	result := Parse(FormatFinalShell, nested, Options{})
	if len(result.Records) != 2 {
		t.Fatalf("records = %+v issues=%+v", result.Records, result.Issues)
	}
	if result.Records[1].Port != 2200 {
		t.Fatalf("string port = %+v", result.Records[1])
	}
	requireIssue(t, result, IssueUnsupportedProtocol)

	malformed := Parse(FormatFinalShell, "{\"host\":", Options{})
	item := requireIssue(t, malformed, IssueInvalidJSON)
	if item.Severity != SeverityError {
		t.Fatalf("severity = %q", item.Severity)
	}
}

func TestParseSecureCRTFixture(t *testing.T) {
	result := Parse(FormatSecureCRT, fixture(t, "securecrt.ini"), Options{})
	if len(result.Records) != 2 {
		t.Fatalf("records = %d, want 2: %+v issues=%+v", len(result.Records), result.Records, result.Issues)
	}
	ssh := result.Records[0]
	if ssh.Label != "Prod Router" || ssh.Hostname != "router.example.test" ||
		ssh.Username != "admin" || ssh.Port != 2222 || ssh.Protocol != "ssh" {
		t.Fatalf("ssh record = %+v", ssh)
	}
	telnet := result.Records[1]
	if telnet.Label != "Legacy Telnet" || telnet.Port != 23 || telnet.Protocol != "telnet" {
		t.Fatalf("telnet record = %+v", telnet)
	}
	invalid := requireIssue(t, result, IssueInvalidHostname)
	if invalid.Value != "invalid host" {
		t.Fatalf("invalid issue = %+v", invalid)
	}
}

func TestSecureCRTFilenameFallback(t *testing.T) {
	text := "S:\"Hostname\"=\"web.example.test\"\nS:\"Protocol Name\"=\"SSH2\"\n"
	cases := []struct {
		name  string
		file  string
		label string
	}{
		{"windows path", `C:\sessions\web-host.ini`, "web-host"},
		{"posix path", "/sessions/web-host.ini", "web-host"},
		{"mixed separators", `/sessions\web-host.ini`, "web-host"},
		{"mixed separators reversed", `C:\sessions/web-host.ini`, "web-host"},
		{"bare filename", "web-host.ini", "web-host"},
		{"bare name without extension", "web-host", "web-host"},
		{"trailing separator", `C:\sessions\`, "web.example.test"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := Parse(FormatSecureCRT, text, Options{FileName: test.file})
			if len(result.Records) != 1 || result.Records[0].Label != test.label {
				t.Fatalf("filename %q: records = %+v, want label %q",
					test.file, result.Records, test.label)
			}
		})
	}
}

func TestParseSSHConfigFixture(t *testing.T) {
	result := Parse(FormatSSHConfig, fixture(t, "ssh_config"), Options{})
	if len(result.Records) != 5 {
		t.Fatalf("records = %d, want 5: %+v issues=%+v", len(result.Records), result.Records, result.Issues)
	}
	web := result.Records[0]
	if web.Label != "web-1" || web.Hostname != "web1.example.test" ||
		web.Username != "deploy" || web.Port != 2222 ||
		web.KeyPath != "~/.ssh/id_ed25519" {
		t.Fatalf("web record = %+v", web)
	}
	if !web.UseSSHAgent || !web.X11Forwarding {
		t.Fatalf("agent/x11 not set: %+v", web)
	}
	for _, record := range result.Records {
		if record.Hostname == "ignored.example.test" {
			t.Fatalf("wildcard host must be skipped: %+v", record)
		}
	}
	if len(result.Records[2].JumpRefs) != 1 || result.Records[2].JumpRefs[0] != 1 {
		t.Fatalf("app jump refs = %v", result.Records[2].JumpRefs)
	}
	requireIssue(t, result, IssueCyclicJump)
	for _, record := range result.Records {
		if len(record.JumpRefs) > 0 {
			for _, ref := range record.JumpRefs {
				if ref == 2 && record.Label == "loop-b" {
					t.Fatalf("cycle edge was kept: %+v", result.Records)
				}
			}
		}
	}
}

func TestSSHConfigInlineJump(t *testing.T) {
	text := "Host app\n  HostName app.example.test\n  ProxyJump deploy@bastion.example.test:2200\n"
	result := Parse(FormatSSHConfig, text, Options{})
	if len(result.Records) != 2 {
		t.Fatalf("records = %+v", result.Records)
	}
	if !result.Records[1].InlineJump || result.Records[1].Hostname != "bastion.example.test" {
		t.Fatalf("inline jump = %+v", result.Records[1])
	}
	requireIssue(t, result, IssueInvalidJump)
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		fixture string
		want    Format
	}{
		{"netcatty.csv", FormatCSV},
		{"putty.reg", FormatPuTTY},
		{"mobaxterm.ini", FormatMobaXterm},
		{"finalshell.json", FormatFinalShell},
		{"securecrt.ini", FormatSecureCRT},
		{"ssh_config", FormatSSHConfig},
	}
	for _, test := range cases {
		if got := Detect(fixture(t, test.fixture)); got != test.want {
			t.Errorf("Detect(%s) = %q, want %q", test.fixture, got, test.want)
		}
	}
	if got := Detect("  \n  "); got != "" {
		t.Fatalf("empty Detect = %q", got)
	}
	if got := Detect("<html>hello</html>"); got != "" {
		t.Fatalf("unknown Detect = %q", got)
	}
}

func TestParseUnsupportedFormat(t *testing.T) {
	result := Parse(Format("nope"), "", Options{})
	item := requireIssue(t, result, IssueUnsupportedEntry)
	if item.Severity != SeverityError {
		t.Fatalf("severity = %q", item.Severity)
	}
}

func TestParsePuTTYPlainSessionFile(t *testing.T) {
	text := "[my session]\r\nHostName=plain.example.test\r\nUserName=root\r\nProtocol=telnet\r\n"
	result := Parse(FormatPuTTY, text, Options{})
	if len(result.Records) != 1 {
		t.Fatalf("records = %+v issues=%+v", result.Records, result.Issues)
	}
	record := result.Records[0]
	if record.Label != "my session" || record.Hostname != "plain.example.test" ||
		record.Port != 23 || record.Protocol != "telnet" {
		t.Fatalf("record = %+v", record)
	}
}

func TestParsePuTTYIgnoresNonSessionRegistryKeys(t *testing.T) {
	result := Parse(FormatPuTTY, fixture(t, "putty_root.reg"), Options{})
	if len(result.Records) != 1 || result.Records[0].Hostname != "good.example.test" {
		t.Fatalf("records = %+v", result.Records)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("issues = %+v, want none", result.Issues)
	}
}

func TestJSONJumpRefsSkipInvalidHostsWithoutShifting(t *testing.T) {
	document := `{"kind":"netcatty.vault","version":1,"hosts":[
		{"id":"bad","label":"bad","hostname":"","port":22},
		{"id":"bastion","hostname":"bastion.example.test","port":22},
		{"id":"client","hostname":"client.example.test","port":22,
		 "hostChain":{"hostIds":["bastion"]}}
	]}`
	result := Parse(FormatNetcattyJSON, document, Options{})
	if len(result.Records) != 2 {
		t.Fatalf("records = %d, issues = %+v", len(result.Records), result.Issues)
	}
	client := result.Records[1]
	if len(client.JumpRefs) != 1 || client.JumpRefs[0] != 0 {
		t.Fatalf("JumpRefs = %v, want [0]", client.JumpRefs)
	}
}
