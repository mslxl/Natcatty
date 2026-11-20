package sessionui

import (
	"context"
	"strings"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
)

// grayPixelPNG is a 1x1 PNG, enough for the graphics parser and the decoder.
const grayPixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func newPictureFlowSurface(t *testing.T) (*Surface, *terminalFlowTransport) {
	t.Helper()
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)
	return surface, transport
}

func waitForPictures(t *testing.T, surface *Surface, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(surface.flow.imagesSnapshot()) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d floater picture(s); got %d", want, len(surface.flow.imagesSnapshot()))
}

// TestTerminalFlowPutsAPictureInTheBlockThatPrintedIt is the regression for a
// program's inline picture: the bytes arrive inside the command's own output,
// often together with the completion mark, and they used to become a floating
// strip above the live screen (or, worse, base64 text in the block). The
// picture must belong to the command's block so it stays with its output.
func TestTerminalFlowPutsAPictureInTheBlockThatPrintedIt(t *testing.T) {
	surface, transport := newPictureFlowSurface(t)

	// One read: the typed line, the start mark, the image and the completion
	// mark. A fast program produces exactly this, and the image arrives after
	// the tap has already closed the block it belongs to.
	transport.send("\x1b]1337;NetcattyExec;zsh;L3RtcA==;c2hvdyBpbWFnZQ==\x07" +
		"\x1b]133;A\x07\x1b]133;C\x07" +
		"\x1b_Gf=100,a=T;" + grayPixelPNG + "\x1b\\" +
		"\x1b]133;D;0\x07")

	waitForFinishedTerminalBlock(t, surface, 1)
	blocks := surface.flow.store.Snapshot()
	if len(blocks[0].Images) != 1 {
		t.Fatalf("the picture did not join its block: %+v (floaters %d)", blocks[0].Images, len(surface.flow.imagesSnapshot()))
	}
	if data := blocks[0].Images[0].Data; !strings.HasPrefix(string(data), "\x89PNG") {
		t.Fatalf("block picture is not PNG data: %q", data)
	}
	if len(surface.flow.imagesSnapshot()) != 0 {
		t.Fatalf("the picture also stayed in the floating strip: %d", len(surface.flow.imagesSnapshot()))
	}
	if strings.Contains(blocks[0].Raw, "Gf=100") || strings.Contains(blocks[0].Raw, grayPixelPNG) {
		t.Fatalf("the graphics payload leaked into the block text: %q", blocks[0].Raw)
	}
}

// TestTerminalFlowKeepsAPromptPictureInTheStrip covers a picture printed
// outside any command: there is no block to own it, so it stays in the flow's
// strip above the live screen.
func TestTerminalFlowKeepsAPromptPictureInTheStrip(t *testing.T) {
	surface, transport := newPictureFlowSurface(t)
	transport.send("\x1b_Gf=100,a=T;" + grayPixelPNG + "\x1b\\")
	waitForPictures(t, surface, 1)
}
