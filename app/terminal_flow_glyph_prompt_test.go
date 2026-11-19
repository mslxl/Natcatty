package app

// A regression test for the reported "the cursor moves up with every Enter"
// behavior. The live area draws its rows as RichText, one element per terminal
// row, but positions the cursor overlay at row*cellHeight with cellHeight
// measured from a plain "M" in monospace. A row holding a glyph the font has
// to fall back for (here the snowflake in the user's nix-shell prompt) gets a
// taller line box than that measurement, so every such row drawn above the
// cursor adds a little error. Empty Enters extend one live run, so the error
// accumulates until the cursor sits a whole row (or more) above the input.

import (
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/session"
	shellui "github.com/mslxl/natcatty/ui/shell"
)

// TestTerminalFlowCursorStaysOnGlyphPromptRow presses Enter on an empty line
// many times with a two-line prompt. Once the flow is pinned to its end the
// newest prompt stays put, so the cursor must stay on that prompt's row too.
// The ASCII head is the control: it uses the same two-line shape but no glyph
// outside the measured font, and it must not drift.
func TestTerminalFlowCursorStaysOnGlyphPromptRow(t *testing.T) {
	const (
		window = 460
		width  = 1200
		enters = 100
	)
	for _, tc := range []struct {
		name string
		head string
	}{
		{name: "ascii-control", head: "READY"},
		{name: "glyph-head", head: "❄ ~ via impure (nix-shell-env)"},
		{name: "nerd-glyph-head", head: " ~ via impure (nix-shell-env)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := "PS1=$'" + tc.head + "\\nP${__flow_n}> '"
			st := newFlowWindowStateWithPrompt(t, prompt)
			if err := st.NewTab(); err != nil {
				t.Fatalf("new tab: %v", err)
			}
			waitForRealShellSurfaces(t, st, 1)
			tester := ui.NewTester(shellui.View(st), width, window)
			tester.SetLocale("en")
			tester.Frame()
			// Focus the terminal: the cursor overlay only draws while focused.
			tester.ClickAt(width/2, window/2)
			tester.Frame()
			waitRealSnapshot(t, st, 0, func(s session.Snapshot) bool {
				return s.Connection == session.ConnectionStatusActive && s.Stats.OutputBytes > 0
			})

			type sample struct {
				enter  int
				prompt float32
				top    int
				delta  int
			}
			var samples []sample
			check := func(index int) {
				t.Helper()
				rect := flowWaitPrompt(t, st, tester, index, 20*time.Second)
				if rect.H <= 0 || rect.Y+rect.H > window {
					t.Fatalf("prompt P%d> is not fully inside the %d-DIP window: rect=(%.1f,%.1f %.1fx%.1f)",
						index, window, rect.X, rect.Y, rect.W, rect.H)
				}
				// Before the flow fills, the prompt itself moves down every Enter;
				// only the pinned frames can show a relative drift.
				if rect.Y < 400 {
					return
				}
				top, bottom, ok := flowCursorBandNear(tester.Image(), int(rect.X)-2, int(rect.Y))
				if !ok {
					t.Fatalf("no cursor is drawn with prompt P%d> at (%.1f,%.1f)", index, rect.X, rect.Y)
				}
				if bottom >= window {
					t.Fatalf("cursor runs past the bottom of the %d-DIP window: rows %d..%d, prompt at %.1f",
						window, top, bottom, rect.Y)
				}
				samples = append(samples, sample{enter: index, prompt: rect.Y, top: top, delta: top - int(rect.Y)})
			}

			check(1)
			for step := 0; step < enters; step++ {
				tester.Key(0, ui.KeyEnter)
				check(2 + step)
			}
			if len(samples) < 10 {
				t.Fatalf("only %d pinned prompt samples; the flow never settled", len(samples))
			}
			var series []string
			for _, s := range samples {
				series = append(series, fmt.Sprintf("prompt P%d>: y=%.1f cursorTop=%d delta=%d", s.enter, s.prompt, s.top, s.delta))
			}
			t.Logf("%s: %d pinned samples, first %s, last %s", tc.name, len(samples), series[0], series[len(series)-1])
			for _, s := range samples {
				if s.delta < -3 || s.delta > 3 {
					t.Fatalf("%s: the cursor drifts off the input row while the flow is pinned:\n%s",
						tc.name, strings.Join(series, "\n"))
				}
			}
		})
	}
}

// flowCursorBandNear returns the contiguous red band whose top is closest to
// wantY, so a cursor row is measured even if the image carries other red.
func flowCursorBandNear(img *image.RGBA, minX, wantY int) (top, bottom int, ok bool) {
	rows := flowCursorRows(img, minX)
	if len(rows) == 0 {
		return 0, 0, false
	}
	bestDist := 1 << 30
	for i := 0; i < len(rows); {
		start, end := rows[i], rows[i]
		for i++; i < len(rows) && rows[i] == end+1; i++ {
			end = rows[i]
		}
		dist := start - wantY
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist, top, bottom, ok = dist, start, end, true
		}
	}
	return top, bottom, ok
}
