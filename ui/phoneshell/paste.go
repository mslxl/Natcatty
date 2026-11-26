package phoneshell

// The phone's one paste path. A paste can arrive three ways — the accessory
// bar's Paste button under a finger, an external keyboard's paste chord, or
// the system's Paste command and on-screen keyboard — and all of them hand the
// clipboard to pasteData. That is what keeps a multi-line paste from running
// as commands through one entry while another asks first: the shared
// PasteProtection rule and its single confirmation row sit on the one path.
//
// Clipboard reads name text and PNG explicitly. MyGo's mobile backend treats
// an empty format list as "nothing to read" and returns empty data, so the
// phone must ask for the representations it can use. Naming them also keeps an
// unrelated HTML or file representation from making a paste look non-empty.

import (
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// phoneClipboardFormats are the representations a phone paste reads: the text
// a shell pastes and the PNG an image-aware program can take.
var phoneClipboardFormats = []transfer.Format{transfer.Text, transfer.PNG}

// readPhoneClipboard reads the clipboard's text and PNG representations. An
// empty clipboard gives empty data, which callers treat as "nothing to paste".
// It takes the persistent Services so a caller between frames can read the
// clipboard without keeping a build's Context.
func readPhoneClipboard(services ui.Services) transfer.Data {
	return services.ReadClipboardData(phoneClipboardFormats...)
}

// pasteData is the phone's only paste decision. It applies the shared
// PasteProtection rule to the clipboard's text: a payload that needs
// confirmation is held on acc and the one confirmation row asks first; every
// other payload goes straight to the terminal's own paste path, which applies
// bracketed-paste mode so a multi-line paste is still wrapped for programs
// that ask for it.
//
// It reports whether it took the data, so a keyboard hook can say the key was
// handled.
func pasteData(services ui.Services, screen TerminalScreen, acc *accessoryState,
	data transfer.Data, guard func(string) bool) bool {

	if screen == nil || len(data.Items()) == 0 {
		return false
	}
	if text, err := data.Read(transfer.Text); err == nil && guard != nil && guard(string(text)) {
		if acc != nil {
			acc.pendingPaste = string(text)
			acc.pendingData = data
		}
		services.Invalidate()
		return true
	}
	screen.PasteData(data)
	return true
}

// heldPaste returns the data a held paste will send. A confirmation created
// before the data was kept (a test's pending text) falls back to its text.
func heldPaste(acc *accessoryState) transfer.Data {
	if acc == nil {
		return transfer.Data{}
	}
	if len(acc.pendingData.Items()) > 0 {
		return acc.pendingData
	}
	if acc.pendingPaste != "" {
		return transfer.TextData(acc.pendingPaste)
	}
	return transfer.Data{}
}
