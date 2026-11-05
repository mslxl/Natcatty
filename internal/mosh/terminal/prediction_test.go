// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import (
	"strings"
	"testing"
)

// predictionFixture returns an engine with an always-visible overlay and a
// mutable clock. Tests drive the same calls the renderer and transport make,
// in stock's order.
func predictionFixture() (*PredictionEngine, *uint64) {
	now := uint64(0)
	engine := NewPredictionEngine(func() uint64 { return now })
	engine.SetDisplayPreference(DisplayAlways)
	return engine, &now
}

// answerFixture is a framebuffer with text on row 0 and the cursor at col.
func answerFixture(width, height int, text string, col, row int) *Framebuffer {
	fb := NewFramebuffer(width, height)
	for i, r := range []rune(text) {
		if i >= width {
			break
		}
		fb.Cell(row, i).Contents = string(r)
	}
	fb.DS.SetCursor(col, row)
	return fb
}

// settle runs one cull so the engine learns the framebuffer size and starts
// its epoch, then treats the current epoch as confirmed. Tests that are about
// prediction geometry use it to reach the state stock reaches after the first
// echo; the confirmation tests exercise that transition explicitly.
func settle(engine *PredictionEngine, fb *Framebuffer) {
	engine.Cull(fb)
	engine.confirmedEpoch = engine.predictionEpoch
}

func TestPredictionDefaultsAndModes(t *testing.T) {
	engine := NewPredictionEngine(func() uint64 { return 0 })
	if got := engine.DisplayPreference(); got != DisplayAdaptive {
		t.Fatalf("default display preference = %d, want adaptive", got)
	}
	if engine.PredictOverwrite() {
		t.Fatal("stock default must not predict overwrite")
	}
	if engine.Active() {
		t.Fatal("fresh engine reports active predictions")
	}
	if got := engine.WaitTime(); got != int(^uint(0)>>1) {
		t.Fatalf("fresh WaitTime = %d, want max", got)
	}

	fb := NewFramebuffer(6, 2)
	fb.DS.SetCursor(0, 0)

	// Never: no parser input, no overlay, no display change.
	engine.SetDisplayPreference(DisplayNever)
	engine.NewUserByte('a', fb)
	if engine.Active() || engine.WaitTime() != int(^uint(0)>>1) {
		t.Fatal("never mode stored a prediction")
	}
	display := fb.Clone()
	engine.Cull(display)
	engine.Apply(display)
	if !display.Equal(fb) {
		t.Fatal("never mode changed the framebuffer")
	}

	// Always shows a confirmed prediction without any SRTT trigger.
	engine, _ = predictionFixture()
	settle(engine, fb)
	engine.SetLocalFrameSent(0)
	engine.NewUserByte('a', fb)
	display = fb.Clone()
	engine.Apply(display)
	if got := rowRunes(display, 0); !strings.HasPrefix(got, "a") {
		t.Fatalf("always mode row = %q, want a prediction", got)
	}
	if engine.WaitTime() != 50 {
		t.Fatalf("active prediction WaitTime = %d, want 50", engine.WaitTime())
	}

	// Adaptive with a fast link and no glitch does not draw.
	engine, _ = predictionFixture()
	engine.SetDisplayPreference(DisplayAdaptive)
	engine.SetSendInterval(20)
	settle(engine, fb)
	engine.SetLocalFrameSent(0)
	engine.NewUserByte('a', fb)
	display = fb.Clone()
	engine.Apply(display)
	if !display.Equal(fb) {
		t.Fatal("adaptive mode predicted on a fast link")
	}

	// A slow interval turns the SRTT trigger on and draws it.
	engine.SetSendInterval(31)
	engine.Cull(fb)
	display = fb.Clone()
	engine.Apply(display)
	if got := rowRunes(display, 0); !strings.HasPrefix(got, "a") {
		t.Fatalf("adaptive slow-link row = %q, want a prediction", got)
	}

	// The trigger has hysteresis: a fast interval alone is not enough while a
	// prediction is outstanding.
	engine.SetSendInterval(20)
	engine.Cull(fb)
	display = fb.Clone()
	engine.Apply(display)
	if got := rowRunes(display, 0); !strings.HasPrefix(got, "a") {
		t.Fatalf("adaptive row after a fast tick = %q, want the pending prediction", got)
	}
}

func TestPredictionConfirmationCorrectionAndEchoAck(t *testing.T) {
	engine, now := predictionFixture()
	*now = 0
	local := answerFixture(6, 2, "", 0, 0)
	engine.SetLocalFrameSent(1)
	engine.SetLocalFrameLateAcked(0)

	// The first burst waits for a confirmation before it is displayed, so the
	// client never speculates across an unverified screen position.
	engine.NewUserByte('a', local)
	display := local.Clone()
	engine.Apply(display)
	if !display.Equal(local) {
		t.Fatalf("unconfirmed prediction was drawn: %q", rowRunes(display, 0))
	}
	if !engine.Active() {
		t.Fatal("pending prediction is not active")
	}

	// The server confirms 'a' and its EchoAck reaches the frame the prediction
	// traveled in. The prediction earns confirmation credit.
	server := answerFixture(6, 2, "a", 1, 0)
	engine.SetLocalFrameLateAcked(2)
	engine.Cull(server)
	if engine.Active() {
		t.Fatal("confirmed prediction was not reset")
	}

	// Predictions in the confirmed epoch now display immediately, before the
	// server has seen them.
	local = server.Clone()
	engine.SetLocalFrameSent(2)
	engine.NewUserByte('b', local)
	display = local.Clone()
	engine.Apply(display)
	if got := rowRunes(display, 0); !strings.HasPrefix(got, "ab") {
		t.Fatalf("confirmed-epoch row = %q, want ab", got)
	}
	if col, _ := display.DS.Cursor(); col != 2 {
		t.Fatalf("predicted cursor col = %d, want 2", col)
	}

	// The server's state arrives saying something else, but its EchoAck is
	// still behind the frame carrying 'b'. The prediction is Pending, not
	// wrong, so the display keeps the speculative character.
	serverX := answerFixture(6, 2, "aX", 2, 0)
	engine.SetLocalFrameLateAcked(2)
	engine.Cull(serverX)
	display = serverX.Clone()
	engine.Apply(display)
	if got := rowRunes(display, 0); !strings.HasPrefix(got, "ab") {
		t.Fatalf("pending EchoAck row = %q, want the prediction ab", got)
	}

	// Once the EchoAck catches up, the server state is authoritative and the
	// wrong prediction is corrected.
	engine.SetLocalFrameLateAcked(3)
	engine.Cull(serverX)
	display = serverX.Clone()
	engine.Apply(display)
	if got := rowRunes(display, 0); !strings.HasPrefix(got, "aX") {
		t.Fatalf("corrected row = %q, want aX", got)
	}
	if engine.Active() {
		t.Fatal("corrected prediction survived")
	}

	// A matching server cell confirms without changing the display.
	engine, _ = predictionFixture()
	server = answerFixture(6, 2, "a", 1, 0)
	local = answerFixture(6, 2, "", 0, 0)
	engine.SetLocalFrameSent(0)
	engine.SetLocalFrameLateAcked(0)
	engine.NewUserByte('a', local)
	if !engine.Active() {
		t.Fatal("matching prediction was not stored")
	}
	baseline := engine.confirmedEpoch
	engine.Cull(server)
	if !engine.overlays[0].overlayCells[0].active {
		t.Fatal("matching prediction before its EchoAck was confirmed too early")
	}
	if engine.confirmedEpoch != baseline {
		t.Fatal("an unprocessed EchoAck advanced the confirmed epoch")
	}
	engine.SetLocalFrameLateAcked(1)
	engine.Cull(server)
	if engine.Active() {
		t.Fatal("matching prediction was not confirmed after its EchoAck")
	}
}

func TestPredictionEchoAckGatesMatchingAndWrongCells(t *testing.T) {
	engine, now := predictionFixture()
	*now = 0
	fb := answerFixture(6, 2, "", 0, 0)
	settle(engine, fb)
	engine.SetLocalFrameSent(4)
	engine.NewUserByte('q', fb)

	// The server framebuffer happens to match, but the EchoAck has not reached
	// the prediction's frame: stock keeps it Pending rather than crediting it.
	server := answerFixture(6, 2, "q", 1, 0)
	baseline := engine.confirmedEpoch
	engine.SetLocalFrameLateAcked(4)
	engine.Cull(server)
	if !engine.Active() {
		t.Fatal("a matching cell was confirmed before its EchoAck")
	}
	if engine.confirmedEpoch != baseline {
		t.Fatal("an unprocessed EchoAck advanced the confirmed epoch")
	}

	engine.SetLocalFrameLateAcked(5)
	engine.Cull(server)
	if engine.Active() {
		t.Fatal("cell was not confirmed after its EchoAck")
	}
	if engine.confirmedEpoch != engine.predictionEpoch {
		t.Fatalf("confirmed epoch = %d, want %d", engine.confirmedEpoch, engine.predictionEpoch)
	}

	// A cursor prediction obeys the same rule.
	engine, now = predictionFixture()
	*now = 0
	fb = answerFixture(6, 2, "", 1, 0)
	settle(engine, fb)
	engine.SetLocalFrameSent(1)
	engine.NewUserByte('\x1b', fb)
	engine.NewUserByte('[', fb)
	engine.NewUserByte('C', fb)
	display := fb.Clone()
	engine.Apply(display)
	if col, _ := display.DS.Cursor(); col != 2 {
		t.Fatalf("predicted cursor col = %d, want 2", col)
	}
	// The server cursor has not moved and its EchoAck is behind: Pending.
	engine.SetLocalFrameLateAcked(1)
	engine.Cull(fb)
	display = fb.Clone()
	engine.Apply(display)
	if col, _ := display.DS.Cursor(); col != 2 {
		t.Fatalf("pending cursor col = %d, want 2", col)
	}
	// Once the EchoAck reaches the frame, the wrong cursor prediction is
	// discarded and the server cursor is authoritative.
	engine.SetLocalFrameLateAcked(2)
	engine.Cull(fb)
	display = fb.Clone()
	engine.Apply(display)
	if col, _ := display.DS.Cursor(); col != 1 {
		t.Fatalf("corrected cursor col = %d, want the server's 1", col)
	}
}

func TestPredictionOverwriteInsertion(t *testing.T) {
	base := answerFixture(6, 2, "abcdef", 2, 0)
	run := func(overwrite bool) string {
		engine, _ := predictionFixture()
		settle(engine, base)
		engine.SetPredictOverwrite(overwrite)
		engine.SetLocalFrameSent(0)
		engine.NewUserByte('X', base)
		display := base.Clone()
		engine.Apply(display)
		return rowRunes(display, 0)
	}

	if got := run(false); got != "abXcdf" {
		t.Fatalf("insert prediction row = %q, want abXcdf", got)
	}
	if got := run(true); got != "abXdef" {
		t.Fatalf("overwrite prediction row = %q, want abXdef", got)
	}
}

func TestPredictionNewlineAndUnknownPrint(t *testing.T) {
	engine, _ := predictionFixture()
	fb := answerFixture(5, 2, "ab", 2, 0)
	settle(engine, fb)
	engine.SetLocalFrameSent(0)
	engine.NewUserByte('\r', fb)
	if len(engine.cursors) == 0 {
		t.Fatal("carriage return did not keep a cursor prediction")
	}
	cursor := engine.cursors[len(engine.cursors)-1]
	if cursor.row != 1 || cursor.col != 0 {
		t.Fatalf("predicted cursor after CR = (%d,%d), want (1,0)", cursor.row, cursor.col)
	}

	// On the last row stock does not predict scrolling; it predicts a blank
	// row instead.
	engine, _ = predictionFixture()
	last := answerFixture(5, 1, "ab", 2, 0)
	settle(engine, last)
	engine.SetLocalFrameSent(0)
	engine.NewUserByte('\r', last)
	if len(engine.overlays) == 0 {
		t.Fatal("last-row CR did not store a blank-row prediction")
	}
	for i := range engine.overlays[0].overlayCells {
		cell := &engine.overlays[0].overlayCells[i]
		if !cell.active || !cell.replacement.IsBlank() {
			t.Fatalf("last-row cell %d = active %v contents %q, want an active blank",
				i, cell.active, cell.replacement.Contents)
		}
	}

	// A wide character is treated as an unknown print: it becomes tentative
	// and stores no cell, because prediction only handles width-one graphemes.
	engine, _ = predictionFixture()
	wide := answerFixture(6, 2, "", 0, 0)
	settle(engine, wide)
	engine.SetLocalFrameSent(0)
	before := engine.predictionEpoch
	engine.NewUserByte(0xe7, wide) // first byte of a UTF-8 sequence
	engine.NewUserByte(0x95, wide)
	engine.NewUserByte(0x8c, wide) // 界
	if engine.predictionEpoch <= before {
		t.Fatal("wide character did not become tentative")
	}
	for _, row := range engine.overlays {
		for i := range row.overlayCells {
			if row.overlayCells[i].active {
				t.Fatalf("wide character stored a cell prediction at (%d,%d)", row.rowNum, i)
			}
		}
	}
}

func TestPredictionFlaggingHysteresis(t *testing.T) {
	engine, _ := predictionFixture()
	engine.SetDisplayPreference(DisplayAdaptive)
	fb := answerFixture(6, 2, "", 0, 0)
	engine.Cull(fb)
	engine.confirmedEpoch = engine.predictionEpoch
	engine.SetLocalFrameSent(0)
	engine.SetSendInterval(81) // above FLAG_TRIGGER_HIGH
	engine.NewUserByte('u', fb)

	display := fb.Clone()
	engine.Apply(display)
	cell := display.Cell(0, 0)
	if cell.Contents != "u" {
		t.Fatalf("flagged prediction cell = %q", cell.Contents)
	}
	if !cell.Renditions.GetAttribute(AttrUnderlined) {
		t.Fatal("slow link did not underline the prediction")
	}

	// A fast interval cures flagging even while the prediction is pending.
	engine.SetSendInterval(50)
	engine.Cull(fb)
	display = fb.Clone()
	engine.Apply(display)
	cell = display.Cell(0, 0)
	if cell.Contents != "u" {
		t.Fatalf("prediction vanished while pending: %q", cell.Contents)
	}
	if cell.Renditions.GetAttribute(AttrUnderlined) {
		t.Fatal("flagging survived a fast interval")
	}
}

func TestPredictionNeverChangesServerFramebuffer(t *testing.T) {
	engine, _ := predictionFixture()
	server := answerFixture(6, 2, "server", 0, 0)
	settle(engine, server)
	before := server.Clone()
	engine.SetLocalFrameSent(0)
	engine.NewUserByte('x', server)
	engine.NewUserByte('\r', server)
	display := server.Clone()
	engine.Apply(display)
	engine.Cull(server)
	if !server.Equal(before) {
		t.Fatal("prediction mutated the server-authoritative framebuffer")
	}
	if display.Equal(before) {
		t.Fatal("overlay did not produce any speculative display (test is vacuous)")
	}
}

func TestPredictionResizeResets(t *testing.T) {
	engine, _ := predictionFixture()
	fb := answerFixture(6, 2, "", 0, 0)
	settle(engine, fb)
	engine.SetLocalFrameSent(0)
	engine.NewUserByte('a', fb)
	if !engine.Active() {
		t.Fatal("no prediction to reset")
	}
	engine.Cull(NewFramebuffer(4, 1))
	if engine.Active() {
		t.Fatal("a size change did not drop predictions")
	}
	if engine.lastWidth != 4 || engine.lastHeight != 1 {
		t.Fatalf("tracked size = %dx%d, want 4x1", engine.lastWidth, engine.lastHeight)
	}
}
