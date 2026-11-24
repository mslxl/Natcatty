package phoneshell

// What a finger is allowed to open. The positive cases are the two actions a
// phone offers — a URL in the system browser and an archive name filled as an
// extraction command. The negative cases are the ones that must never reach
// the system opener: local paths, unsupported schemes (file, ftp, javascript,
// ssh), version numbers, and plain words.

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func rowOf(text string) []ui.Span { return []ui.Span{{Text: text}} }

func TestTapLinksOpenOnlyURLsAndArchives(t *testing.T) {
	cases := []struct {
		name string
		text string
		// target is the substring the tap lands in.
		target string
		want   string
		kind   linkKind
	}{
		{name: "https URL", text: "see https://example.com/docs now", target: "https://", want: "https://example.com/docs", kind: linkURL},
		{name: "http URL inside punctuation", text: "(http://example.com)", target: "http://e", want: "http://example.com", kind: linkURL},
		{name: "mail address", text: "write mailto:me@example.com", target: "mailto:", want: "mailto:me@example.com", kind: linkURL},
		{name: "tar archive", text: "download bundle.tar.gz.", target: "bundle", want: "bundle.tar.gz", kind: linkArchive},
		{name: "zip archive in punctuation", text: "saved (release.zip).", target: "release", want: "release.zip", kind: linkArchive},
		{name: "absolute tar path", text: "wrote /tmp/logs.tar.xz", target: "logs", want: "/tmp/logs.tar.xz", kind: linkArchive},

		{name: "absolute local path", text: "open /etc/passwd", target: "/etc", want: "", kind: linkNone},
		{name: "relative script", text: "run ./scripts/deploy.sh", target: "deploy", want: "", kind: linkNone},
		{name: "file scheme", text: "file:///etc/passwd", target: "file://", want: "", kind: linkNone},
		{name: "ftp scheme", text: "ftp://example.com/pub", target: "ftp://", want: "", kind: linkNone},
		{name: "ssh scheme", text: "ssh://host.example.com", target: "ssh://", want: "", kind: linkNone},
		{name: "javascript scheme", text: "javascript:alert(1)", target: "javascript", want: "", kind: linkNone},
		{name: "version number", text: "installed v1.2.3", target: "1.2.3", want: "", kind: linkNone},
		{name: "archive-like option", text: "-bundle.tar", target: "bundle", want: "", kind: linkNone},
		{name: "plain word", text: "hello world", target: "hello", want: "", kind: linkNone},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			col := strings.Index(testCase.text, testCase.target)
			if col < 0 {
				t.Fatalf("test text %q does not contain %q", testCase.text, testCase.target)
			}
			got, kind := linkAtRow(rowOf(testCase.text), col)
			if got != testCase.want || kind != testCase.kind {
				t.Fatalf("linkAtRow(%q, %d) = %q, %v; want %q, %v",
					testCase.text, col, got, kind, testCase.want, testCase.kind)
			}
		})
	}
}

// A wide rune takes two cells. A tap on the second half of the rune, or on a
// later cell of a token after one, must still name the same token: phones
// print CJK file names and the archive rule has to see them whole.
func TestTapLinkCellArithmeticKeepsWideRunesWhole(t *testing.T) {
	text := "下载 bundle.zip 完成"
	token, kind := linkAtRow(rowOf(text), 7)
	if token != "bundle.zip" || kind != linkArchive {
		t.Fatalf("tap at col 7 = %q, %v; want bundle.zip, archive", token, kind)
	}
	// The space after the archive name names no token.
	if token, kind := linkAtRow(rowOf(text), 17); kind != linkNone {
		t.Fatalf("tap on the space after the archive = %q, %v; want none", token, kind)
	}
	// The second cell of the wide first rune still names its own token, which
	// is not a link.
	if _, kind := linkAtRow(rowOf(text), 1); kind != linkNone {
		t.Fatalf("tap on the wide rune = %v; want none", kind)
	}
}

func TestLinkNoticeNamesTheTargetAndReason(t *testing.T) {
	if got := linkNoticeText("https://example.com", nil); got == "" || !strings.Contains(got, "https://example.com") {
		t.Fatalf("notice = %q, want the target named", got)
	}
	got := linkNoticeText("https://example.com", errNoOpener)
	if !strings.Contains(got, "no Session service") {
		t.Fatalf("notice = %q, want the reason", got)
	}
}
