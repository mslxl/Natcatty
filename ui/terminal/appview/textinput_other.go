//go:build !darwin

package appview

// takesPlatformTextInput reports whether the window only turns a key into text
// for an element that declares a text input. Windows (WM_CHAR) and GTK (the
// input method's commit) send the text a key types to the focused element on
// their own, so the terminal keeps the ordinary key path there: declaring a
// text input would also associate the native IME context with the window on
// Windows, which this terminal deliberately does not ask for.
const takesPlatformTextInput = false
