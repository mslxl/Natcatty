//go:build darwin || linux

package app

// Real-shell regressions for the two picture paths: a file lens (`cat
// logo.png`) and a program printing an inline picture (Kitty APC). Both used
// to be invisible or to leave their payload in the block's text. The shell is
// the production local zsh with the production capture hook, so the Lens
// blocks, the OSC 7 working directory and the copy are exercised for real.

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/adapters/shellhook"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// runPictureShell opens a real zsh with the capture hook, in an isolated HOME
// whose rc file keeps the prompt short and quiet.
func runPictureShell(t *testing.T) (*appsession.Session, *sessionui.Surface, *ui.Tester, string) {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed here")
	}
	home := t.TempDir()
	zshrc := "setopt prompt_subst\nPROMPT_EOL_MARK=''\nunsetopt correct\nHISTFILE=/dev/null\nSAVEHIST=0\nPS1='%% '\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("NETCATTY_USER_ZDOTDIR", "")
	t.Setenv("NETCATTY_HOOK", "")
	t.Setenv("SHELL", zsh)
	t.Setenv("PS1", "")
	t.Setenv("PROMPT_EOL_MARK", "")
	t.Setenv("STARSHIP_SHELL", "zsh")
	t.Setenv("STARSHIP_SESSION_KEY", "")
	t.Setenv("ATUIN_SESSION", "")
	t.Setenv("ATUIN_SHLVL", "")
	t.Setenv("ITERM_SHELL_INTEGRATION_INSTALLED", "")

	dir := realShellDir(t)
	service := appsession.NewService(localpty.Factory{},
		appsession.WithCaptureHook(shellhook.NewPlanner(
			shellhook.WithEnabled(func() bool { return true }),
		)))
	t.Cleanup(service.CloseAll)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	session, err := service.Open(ctx, appsession.StartSpec{
		Kind: appsession.KindLocal,
		Dir:  dir,
		Cols: 100,
		Rows: 30,
	})
	if err != nil {
		t.Fatalf("open the real shell: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	surface, err := sessionui.New(session,
		sessionui.WithRedraw(func() {}),
		sessionui.WithPastePolicy(func() (bool, int) { return false, 2 }),
		sessionui.WithDropAction(func() domainterminal.DropAction { return domainterminal.DropInsert }),
		sessionui.WithEncodingChange(func(domainterminal.Encoding) {}),
	)
	if err != nil {
		t.Fatalf("build the Session surface: %v", err)
	}
	t.Cleanup(surface.Close)
	tester := ui.NewTester(func(c *ui.Context) { surface.View(c) }, 900, 620)
	tester.SetLocale("en")
	tester.Frame()
	if !realShellWait(t, func() bool { return strings.TrimSpace(surface.ScreenText()) != "" }) {
		t.Fatal("the real shell produced no output")
	}
	tester.ClickAt(450, 560)
	return session, surface, tester, dir
}

// redPixels counts the picture's pixels in a rendered frame.
func redPixels(pic *image.RGBA) int {
	if pic == nil {
		return 0
	}
	n := 0
	b := pic.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := pic.At(x, y).RGBA()
			if r>>8 > 200 && g>>8 < 80 && bl>>8 < 80 {
				n++
			}
		}
	}
	return n
}

func writeProbePicture(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pumpTUIFrames(tester *ui.Tester, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		tester.Frame()
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRealShellShowsAFilePictureAndAnInlinePicture drives `cat logo.png` and a
// program that prints the same PNG through the Kitty graphics protocol, then
// reads the rendered frame: both must show red pixels and a caption, and the
// inline picture must not leave its base64 payload in the block text.
func TestRealShellShowsAFilePictureAndAnInlinePicture(t *testing.T) {
	session, _, tester, dir := runPictureShell(t)
	writeProbePicture(t, filepath.Join(dir, "logo.png"))

	tester.Type("cat logo.png")
	if !realShellWait(t, func() bool { return session.Snapshot().Input == "cat logo.png" }) {
		t.Fatalf("typed line = %q", session.Snapshot().Input)
	}
	tester.Key(0, ui.KeyEnter)
	if !realShellWait(t, func() bool { return !session.Snapshot().Command.Running }) {
		t.Fatalf("cat did not finish: %+v", session.Snapshot().Command)
	}
	pumpTUIFrames(tester, time.Second)
	if !tester.HasText("logo.png") {
		t.Fatalf("the file lens did not draw the picture caption: %v", tester.Texts())
	}
	if got := redPixels(tester.Image()); got < 500 {
		t.Fatalf("the file picture is not on screen: %d red pixels, texts %v", got, tester.Texts())
	}

	// The same PNG through the Kitty graphics protocol, as a program prints it.
	raw, err := os.ReadFile(filepath.Join(dir, "logo.png"))
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	kitty := "printf '\\033_Gf=100,a=T;" + encoded + "\\033\\\\'"
	tester.Type(kitty)
	if !realShellWait(t, func() bool { return session.Snapshot().Input == kitty }) {
		t.Fatalf("typed kitty line = %q", session.Snapshot().Input)
	}
	tester.Key(0, ui.KeyEnter)
	if !realShellWait(t, func() bool { return !session.Snapshot().Command.Running }) {
		t.Fatalf("printf did not finish: %+v", session.Snapshot().Command)
	}
	pumpTUIFrames(tester, time.Second)
	if got := redPixels(tester.Image()); got < 1500 {
		t.Fatalf("the inline picture is not on screen: %d red pixels, texts %v", got, tester.Texts())
	}
	// The protocol's picture has no path to caption it with, and the parser's
	// name for the sequence is not text the program printed: a terminal shows
	// the picture alone, as Kitty does.
	for _, internal := range []string{"inline image", "sixel image"} {
		if tester.HasText(internal) {
			t.Fatalf("the picture was captioned %q: %v", internal, tester.Texts())
		}
	}
	// The payload may show once: the block header echoes the line the user
	// typed. It must not be printed a second time as the command's output —
	// that is the base64 garbage the tap's string-state swallowing prevents.
	// (The unit test on Block.Raw is the strict guard for the output itself.)
	if got := strings.Count(strings.Join(tester.Texts(), "\n"), "Gf=100"); got > 1 {
		t.Fatalf("the graphics payload was printed as output (%d occurrences): %v", got, tester.Texts())
	}
}
