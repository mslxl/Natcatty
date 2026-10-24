package sftp

import (
	"strings"
	"testing"
)

func TestScpControlLineVectors(t *testing.T) {
	line, err := buildFileControlLine(0o644, 11, "hello.txt")
	if err != nil || string(line) != "C0644 11 hello.txt\n" {
		t.Fatalf("buildFileControlLine = %q, %v", line, err)
	}
	if _, err := buildFileControlLine(0o644, 1, "a/b"); err == nil {
		t.Fatal("a path separator in a basename was accepted")
	}
	if _, err := buildFileControlLine(0o644, 1, ".."); err == nil {
		t.Fatal("a parent entry was accepted as an SCP basename")
	}
	if _, err := sanitizeScpBasename("a\\b"); err != nil {
		t.Fatalf("a backslash is a valid POSIX name: %v", err)
	}
	if _, err := buildFileControlLine(0o644, -1, "x"); err == nil {
		t.Fatal("a negative size was accepted")
	}
}

func TestParseControlLineVectors(t *testing.T) {
	file, err := parseControlLine("C0644 3 my file.txt")
	if err != nil {
		t.Fatalf("parse file: %v", err)
	}
	if file.Kind != "file" || file.Mode != 0o644 || file.Size != 3 || file.Name != "my file.txt" {
		t.Fatalf("file = %+v", file)
	}
	dir, err := parseControlLine("D0755 0 nested")
	if err != nil || dir.Kind != "directory" || dir.Name != "nested" {
		t.Fatalf("directory = %+v, %v", dir, err)
	}
	if control, err := parseControlLine("E"); err != nil || control.Kind != "end" {
		t.Fatalf("end = %+v, %v", control, err)
	}
	if control, err := parseControlLine("T1700000000 0 1700000000 0"); err != nil || control.Kind != "time" {
		t.Fatalf("time = %+v, %v", control, err)
	}
	for _, bad := range []string{
		"", "X0644 1 x", "C0644 one x", "C0644 1 a/b", "C0644 1 ..",
		"C0644 1 x\ny",
	} {
		if _, err := parseControlLine(bad); err == nil {
			t.Errorf("parseControlLine(%q) accepted an invalid line", bad)
		}
	}
}

func TestScpShellQuotingAndCommands(t *testing.T) {
	quoted, err := shellQuote("a'b", false)
	if err != nil || quoted != `'a'\''b'` {
		t.Fatalf("shellQuote = %q, %v", quoted, err)
	}
	if _, err := shellQuote("a\x00b", false); err == nil {
		t.Fatal("a NUL argument was accepted")
	}
	if _, err := shellQuote("", false); err == nil {
		t.Fatal("an empty path argument was accepted")
	}
	if err := assertSafeRemotePath("a\nb"); err == nil {
		t.Fatal("a newline in a remote path was accepted")
	}

	listCmd, err := buildListCommand("/tmp/a b")
	if err != nil {
		t.Fatalf("list command: %v", err)
	}
	statCmd, err := buildStatCommand("/tmp/a b")
	if err != nil {
		t.Fatalf("stat command: %v", err)
	}
	mkdirCmd, err := buildMkdirCommand("/tmp/a b", false)
	if err != nil {
		t.Fatalf("mkdir command: %v", err)
	}
	mkdirAllCmd, err := buildMkdirCommand("/tmp/a b", true)
	if err != nil {
		t.Fatalf("mkdir -p command: %v", err)
	}
	renameCmd, err := buildRenameCommand("/tmp/a", "/tmp/b")
	if err != nil {
		t.Fatalf("rename command: %v", err)
	}
	chmodCmd, err := buildChmodCommand("/tmp/a", 0o640)
	if err != nil {
		t.Fatalf("chmod command: %v", err)
	}
	sinkCmd, err := buildScpSinkCommand("/tmp")
	if err != nil {
		t.Fatalf("sink command: %v", err)
	}
	sourceCmd, err := buildScpSourceCommand("/tmp/a")
	if err != nil {
		t.Fatalf("source command: %v", err)
	}
	commands := map[string]string{
		"list":     listCmd,
		"stat":     statCmd,
		"mkdir":    mkdirCmd,
		"mkdirall": mkdirAllCmd,
		"rename":   renameCmd,
		"chmod":    chmodCmd,
		"sink":     sinkCmd,
		"source":   sourceCmd,
	}
	if !strings.Contains(commands["list"], "cd '/tmp/a b' || exit 1;") {
		t.Fatalf("list command = %q", commands["list"])
	}
	if !strings.Contains(commands["stat"], "p='/tmp/a b'") {
		t.Fatalf("stat command = %q", commands["stat"])
	}
	if commands["mkdir"] != "mkdir -- '/tmp/a b'" {
		t.Fatalf("mkdir = %q", commands["mkdir"])
	}
	if commands["mkdirall"] != "mkdir -p -- '/tmp/a b'" {
		t.Fatalf("mkdirall = %q", commands["mkdirall"])
	}
	if commands["rename"] != "mv -- '/tmp/a' '/tmp/b'" {
		t.Fatalf("rename = %q", commands["rename"])
	}
	if commands["chmod"] != "chmod 0640 -- '/tmp/a'" {
		t.Fatalf("chmod = %q", commands["chmod"])
	}
	if commands["sink"] != "scp -t -- '/tmp'" {
		t.Fatalf("sink = %q", commands["sink"])
	}
	if commands["source"] != "scp -f -- '/tmp/a'" {
		t.Fatalf("source = %q", commands["source"])
	}
	if !strings.Contains(commands["list"], "for f in * .[!.]* ..?*; do") {
		t.Fatalf("the listing does not cover dotfiles and odd names: %q", commands["list"])
	}
	if !strings.Contains(commands["list"], "stat -c %s") {
		t.Fatalf("the listing does not measure sizes: %q", commands["list"])
	}
	if strings.Contains(commands["list"], "wc -c") {
		t.Fatalf("the listing must not open files: %q", commands["list"])
	}
	if buildHomeCommand() != `printf "%s\n" "$HOME"` {
		t.Fatalf("home command = %q", buildHomeCommand())
	}
}

func TestParseListRecords(t *testing.T) {
	// base64("hello.txt") = aGVsbG8udHh0, base64("with space") = d2l0aCBzcGFjZQ==
	stdout := strings.Join([]string{
		"f|-rw-r--r--|12|1700000000|aGVsbG8udHh0|alice",
		"d|drwxr-xr-x|4096|1700000001|d2l0aCBzcGFjZQ==|root",
		"l|lrwxrwxrwx|0|1700000002|bGluaw==|alice",
		"bogus",
		"f|-rw-------|3|1700000003|!!bad!!|",
		"f|-rw-------|3|1700000003|Lg==|",
	}, "\n")
	records := parseListRecords(stdout)
	if len(records) != 3 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Name != "hello.txt" || records[0].Type != "file" ||
		records[0].Size != 12 || records[0].Mode&0o777 != 0o644 || records[0].Owner != "alice" {
		t.Fatalf("file record = %+v", records[0])
	}
	if records[1].Name != "with space" || records[1].Type != "directory" {
		t.Fatalf("directory record = %+v", records[1])
	}
	if records[2].Type != "symlink" {
		t.Fatalf("symlink record = %+v", records[2])
	}
	if got := records[0].ModTime.Unix(); got != 1700000000 {
		t.Fatalf("mtime = %d", got)
	}
}

func TestParseLsLaFallback(t *testing.T) {
	stdout := strings.Join([]string{
		"total 8",
		"-rw-r--r-- 1 alice staff 5 Jan  1 00:00 notes.txt",
		"drwxr-xr-x 2 root  root  4096 Jan  1 00:00 docs",
		"lrwxrwxrwx 1 root  root     4 Jan  1 00:00 link -> /etc",
	}, "\n")
	records := parseLsLaOutput(stdout)
	if len(records) != 3 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Name != "notes.txt" || records[0].Size != 5 || records[0].Mode&0o777 != 0o644 {
		t.Fatalf("file = %+v", records[0])
	}
	if records[1].Type != "directory" {
		t.Fatalf("directory = %+v", records[1])
	}
	if records[2].Name != "link" || records[2].Type != "symlink" {
		t.Fatalf("link = %+v", records[2])
	}
}

func TestParseStatRecord(t *testing.T) {
	record, err := parseStatRecord("f|rwsr-sr-t|12|1700000000|/tmp/a\n", "", 0, true)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if record.Type != "file" || record.Mode&0o777 != 0o755 || record.Size != 12 {
		t.Fatalf("record = %+v", record)
	}
	if record.Mode&0o4000 == 0 || record.Mode&0o2000 == 0 || record.Mode&0o1000 == 0 {
		t.Fatalf("special bits = %04o", record.Mode)
	}

	if _, err := parseStatRecord("", "ENOENT\n", 2, true); err == nil {
		t.Fatal("a missing path was not reported")
	} else if failure, ok := err.(*scpError); !ok || failure.Code != "ENOENT" {
		t.Fatalf("missing path error = %#v", err)
	}
	if _, err := parseStatRecord("", "", 0, true); err == nil {
		t.Fatal("an empty response was not reported")
	}
	if _, err := parseStatRecord("garbage", "", 0, true); err == nil {
		t.Fatal("a malformed record was accepted")
	}
	if _, err := parseStatRecord("-rwxr-xr-x|?|?|0|/tmp/a", "banner", 3, true); err == nil {
		t.Fatal("a failed command with plausible output was accepted")
	}
}

func TestLsModeToBits(t *testing.T) {
	tests := map[string]uint32{
		"-rw-r--r--": 0o644,
		"drwxr-xr-x": 0o755,
		"lrwxrwxrwx": 0o777,
		"-rwsr-sr-t": 0o7755,
		"?":          0,
		"":           0,
	}
	for text, want := range tests {
		if got := lsModeToBits(text); got != want {
			t.Errorf("lsModeToBits(%q) = %04o, want %04o", text, got, want)
		}
	}
}
