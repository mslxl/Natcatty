//go:build windows

package pty

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNatcattyPassthroughHelper(t *testing.T) {
	mode := os.Getenv("NATCATTY_PASSTHROUGH_SEQ")
	if mode == "" {
		return
	}
	switch mode {
	case "kitty":
		fmt.Print("\x1b_Ga=T,f=100;" +
			"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==" +
			"\x1b\\")
	case "sixel":
		fmt.Print("\x1bPq\"1;1;2;6#0;2;100;0;0#0~~\x1b\\")
	case "iterm":
		fmt.Print("\x1b]1337;File=name=cGljLnBuZw==;inline=1:" +
			"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==" +
			"\x07")
	}
	os.Exit(0)
}

func TestNatcattyConPTYPassthroughImages(t *testing.T) {
	if os.Getenv("NATCATTY_CONPTY_DIR") == "" {
		t.Skip("set NATCATTY_CONPTY_DIR to a winconpty pair (conpty.dll + OpenConsole.exe)")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"kitty", "sixel", "iterm"} {
		env := append(os.Environ(), "NATCATTY_PASSTHROUGH_SEQ="+mode)
		sess, err := Start(Options{
			Path: exe,
			Args: []string{exe, "-test.run=^TestNatcattyPassthroughHelper$"},
			Env:  env,
			Cols: 80,
			Rows: 24,
		})
		if err != nil {
			t.Fatalf("%s: start: %v", mode, err)
		}
		var all []byte
		buf := make([]byte, 1<<16)
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			n, readErr := sess.Read(buf)
			if n > 0 {
				all = append(all, buf[:n]...)
			}
			if readErr != nil || imagesForNatcatty(all) > 0 {
				break
			}
		}
		_ = sess.Close()
		if imagesForNatcatty(all) == 0 {
			t.Fatalf("%s: no image sequence arrived (%d bytes: %q)", mode, len(all), previewNatcatty(all))
		}
	}
}

func imagesForNatcatty(b []byte) int {
	count := 0
	for _, marker := range [][]byte{
		[]byte("\x1b_G"),
		[]byte("\x1bPq"),
		[]byte("\x1b]1337;File="),
	} {
		count += bytes.Count(b, marker)
	}
	return count
}

func previewNatcatty(b []byte) string {
	s := string(b)
	if len(s) > 120 {
		s = s[:120]
	}
	return strings.ReplaceAll(s, "\x1b", "\\e")
}
