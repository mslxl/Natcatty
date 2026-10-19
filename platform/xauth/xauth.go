// Package xauth resolves the local X11 display, connects to its Unix socket or
// TCP port, and reads the matching MIT-MAGIC-COOKIE-1 from xauth. It is the
// only OS-aware part of X11 forwarding; the SSH adapter reaches it through a
// small interface so tests can substitute an in-process X server.
package xauth

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// X11PortBase is the TCP port of display 0.
const X11PortBase = 6000

// AuthProtocol is the only X11 authorization protocol the original supports.
const AuthProtocol = "MIT-MAGIC-COOKIE-1"

const xauthTimeout = 2 * time.Second

var (
	displayPattern         = regexp.MustCompile(`^(.*):(\d+)(?:\.(\d+))?$`)
	displayNumberPattern   = regexp.MustCompile(`:(\d+)(?:\.\d+)?$`)
	socketNumberPattern    = regexp.MustCompile(`/X(\d+)$`)
	authCookieEntryPattern = regexp.MustCompile(`\b` + regexp.QuoteMeta(AuthProtocol) + `\b\s+([0-9a-fA-F]+)`)
)

// Target is one local X server endpoint: either a Unix socket path or a TCP
// host and port.
type Target struct {
	Path string
	Host string
	Port int
}

// String renders the target for diagnostics.
func (t Target) String() string {
	if t.Path != "" {
		return t.Path
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// Resolve returns the local display target. An empty specification uses
// DISPLAY, then the platform default (localhost:0 on Windows, :0 elsewhere).
func Resolve(display string) Target {
	return resolve(display, runtime.GOOS, os.Getenv("DISPLAY"))
}

func resolve(display, platform, envDisplay string) Target {
	raw := strings.TrimSpace(display)
	if raw == "" {
		raw = strings.TrimSpace(envDisplay)
	}
	if raw == "" {
		if platform == "windows" {
			raw = "localhost:0"
		} else {
			raw = ":0"
		}
	}
	if raw == ":" {
		raw = ":0"
	}
	if strings.HasPrefix(raw, "/") {
		return Target{Path: raw}
	}

	match := displayPattern.FindStringSubmatch(raw)
	if match == nil {
		if platform == "windows" {
			return Target{Host: raw, Port: X11PortBase}
		}
		return Target{Path: raw}
	}

	host := match[1]
	number, _ := strconv.Atoi(match[2])
	port := number
	if number < 100 {
		port = X11PortBase + number
	}

	if strings.EqualFold(host, "unix") && platform != "windows" {
		return Target{Path: fmt.Sprintf("/tmp/.X11-unix/X%d", number)}
	}
	if host == "" {
		if platform == "windows" {
			return Target{Host: "localhost", Port: port}
		}
		return Target{Path: fmt.Sprintf("/tmp/.X11-unix/X%d", number)}
	}
	if strings.HasPrefix(host, "/") {
		return Target{Path: host}
	}
	return Target{Host: host, Port: port}
}

// Local reads a display's credentials from xauth and dials the X server.
type Local struct {
	// Display is the DISPLAY-style override. Empty uses DISPLAY or :0.
	Display string
}

// New returns a local X11 seam for display.
func New(display string) *Local { return &Local{Display: display} }

// Target reports the resolved local display target for diagnostics.
func (l *Local) Target() string { return Resolve(l.Display).String() }

// Dial connects to the local X server.
func (l *Local) Dial(ctx context.Context) (net.Conn, error) {
	target := Resolve(l.Display)
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if target.Path != "" {
		return dialer.DialContext(ctx, "unix", target.Path)
	}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
}

// AuthCookie returns the local MIT-MAGIC-COOKIE-1 for the display, or nil when
// xauth is unavailable or has no matching entry. A missing cookie is not fatal:
// forwarding is attempted without a rewrite, matching the original.
func (l *Local) AuthCookie(ctx context.Context) []byte {
	display := l.display()
	xauthCtx, cancel := context.WithTimeout(ctx, xauthTimeout)
	defer cancel()

	command := xauthCommand()
	cmd := exec.CommandContext(xauthCtx, command, "list")
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	return ParseCookie(string(output), display)
}

// display returns the DISPLAY value used for the xauth lookup, matching the
// original's ":0" fallback.
func (l *Local) display() string {
	value := strings.TrimSpace(l.Display)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("DISPLAY"))
	}
	if value == "" {
		return ":0"
	}
	return value
}

// xauthCommand is defined by the build-tagged command_*.go files: XQuartz
// installs xauth outside PATH on macOS.

// ParseCookie selects the MIT-MAGIC-COOKIE-1 entry for display from xauth list
// output. The cookie is returned as raw bytes.
func ParseCookie(output, display string) []byte {
	requested, haveRequested := displayNumber(display)
	for _, entry := range strings.Split(output, "\n") {
		match := authCookieEntryPattern.FindStringSubmatch(entry)
		if match == nil {
			continue
		}
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}
		if haveRequested {
			number, ok := displayNumber(fields[0])
			if !ok || number != requested {
				continue
			}
		}
		cookie, err := hex.DecodeString(match[1])
		if err != nil {
			continue
		}
		return cookie
	}
	return nil
}

// displayNumber extracts the display number from a DISPLAY value or a socket
// path. A TCP-style display number at or above the port base is normalized.
func displayNumber(display string) (int, bool) {
	value := strings.TrimSpace(display)
	if value == "" {
		return 0, false
	}
	if value == ":" {
		value = ":0"
	}
	match := displayNumberPattern.FindStringSubmatch(value)
	if match == nil {
		match = socketNumberPattern.FindStringSubmatch(value)
	}
	if match == nil {
		return 0, false
	}
	number, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	if number >= X11PortBase {
		number -= X11PortBase
	}
	return number, true
}
