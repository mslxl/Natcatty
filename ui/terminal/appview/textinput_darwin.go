//go:build darwin

package appview

// takesPlatformTextInput reports whether the window only turns a key into text
// for an element that declares a text input. AppKit sends a key down and, only
// when the window's input context is active, the characters the key types;
// with nothing declared the terminal received the key down and no text, so a
// Local Terminal could not be typed into at all. The client both supplies the
// text and gives the system somewhere to compose input-method text.
//
// The headless ui.Tester cannot catch a missing declaration: Type and TypeKey
// inject InputText directly, bypassing the window's key-to-text step. Every
// typing test therefore stayed green while a real window delivered no text.
// TestTerminalTakesPlatformTextInput asserts the declaration itself for this
// reason; do not replace it with a typing test.
const takesPlatformTextInput = true
