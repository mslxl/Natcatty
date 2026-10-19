package xauth

import (
	"encoding/hex"
	"testing"
)

func TestResolveMapsUnixDisplays(t *testing.T) {
	tests := []struct {
		name     string
		display  string
		platform string
		env      string
		want     Target
	}{
		{"unix number", ":2", "linux", "", Target{Path: "/tmp/.X11-unix/X2"}},
		{"bare colon", ":", "darwin", "", Target{Path: "/tmp/.X11-unix/X0"}},
		{"environment fallback", "", "linux", ":3", Target{Path: "/tmp/.X11-unix/X3"}},
		{"unix prefix", "unix:1", "linux", "", Target{Path: "/tmp/.X11-unix/X1"}},
		{"socket path", "/private/tmp/com.apple.launchd.test/org.xquartz:0", "darwin", "", Target{Path: "/private/tmp/com.apple.launchd.test/org.xquartz:0"}},
		{"tcp display", "localhost:1", "windows", "", Target{Host: "localhost", Port: 6001}},
		{"windows default", "", "windows", "", Target{Host: "localhost", Port: 6000}},
		{"unmanaged host", "xserver", "windows", "", Target{Host: "xserver", Port: 6000}},
		{"high display number", "host:6010", "linux", "", Target{Host: "host", Port: 6010}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolve(test.display, test.platform, test.env); got != test.want {
				t.Fatalf("resolve(%q, %q, %q) = %+v, want %+v",
					test.display, test.platform, test.env, got, test.want)
			}
		})
	}
}

func TestTargetStringRendersPathOrAddress(t *testing.T) {
	if got := (Target{Path: "/tmp/.X11-unix/X0"}).String(); got != "/tmp/.X11-unix/X0" {
		t.Fatalf("path target = %q", got)
	}
	if got := (Target{Host: "localhost", Port: 6000}).String(); got != "localhost:6000" {
		t.Fatalf("tcp target = %q", got)
	}
}

func TestParseCookieSelectsTheRequestedDisplay(t *testing.T) {
	cookie0 := "00000000000000000000000000000000"
	cookie10 := "10101010101010101010101010101010"
	output := "host/unix:10  MIT-MAGIC-COOKIE-1  " + cookie10 + "\n" +
		"host/unix:0  MIT-MAGIC-COOKIE-1  " + cookie0 + "\n"

	got := ParseCookie(output, ":0")
	want, _ := hex.DecodeString(cookie0)
	if string(got) != string(want) {
		t.Fatalf("cookie for :0 = %x, want %s", got, cookie0)
	}
}

func TestParseCookieMatchesSocketPaths(t *testing.T) {
	cookie0 := "00000000000000000000000000000000"
	cookie1 := "11111111111111111111111111111111"
	output := "host/unix:0  MIT-MAGIC-COOKIE-1  " + cookie0 + "\n" +
		"host/unix:1  MIT-MAGIC-COOKIE-1  " + cookie1 + "\n"

	got := ParseCookie(output, "/tmp/.X11-unix/X1")
	want, _ := hex.DecodeString(cookie1)
	if string(got) != string(want) {
		t.Fatalf("cookie for socket 1 = %x, want %s", got, cookie1)
	}
}

func TestParseCookieFallsBackToTheFirstEntry(t *testing.T) {
	cookie := "abcdefabcdefabcdefabcdefabcdefab"
	if got := ParseCookie("unknown  MIT-MAGIC-COOKIE-1  "+cookie, ""); got == nil {
		t.Fatal("ParseCookie ignored the first entry for an unknown display")
	}
}

func TestParseCookieReturnsNilWithoutAMatch(t *testing.T) {
	if got := ParseCookie("host/unix:0  XDM-AUTHORIZATION-1  abcdef", ":1"); got != nil {
		t.Fatalf("cookie = %x, want nil", got)
	}
}
