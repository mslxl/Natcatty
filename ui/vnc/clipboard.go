package vnc

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

// MaxClipboardBytes is the largest clipboard text the viewer sends to a VNC
// server. The protocol's Latin-1 clipboard has no inherent bound, but a huge
// paste is rarely intentional and a rejection is clearer than a stalled
// stream. The limit is checked before anything is written, so oversized text
// is never silently truncated.
const MaxClipboardBytes = 1 << 20

// ClipboardResult classifies a clipboard action. It is exported so the chrome
// and tests can name the exact outcome the user sees.
type ClipboardResult int

const (
	// ClipboardSent reports that local text reached the server.
	ClipboardSent ClipboardResult = iota
	// ClipboardPulled reports that the last remote text reached the local
	// clipboard.
	ClipboardPulled
	// ClipboardEmpty reports that there was no local text to send.
	ClipboardEmpty
	// ClipboardTooLarge reports text over MaxClipboardBytes; nothing was sent.
	ClipboardTooLarge
	// ClipboardNotLatin1 reports a rune outside U+0000..U+00FF; nothing was
	// sent, because the RFB baseline clipboard cannot represent it.
	ClipboardNotLatin1
	// ClipboardNoRemoteText reports that the server has not sent any
	// ServerCutText yet.
	ClipboardNoRemoteText
	// ClipboardViewOnly reports that view-only mode refused to send local
	// text. Clipboard pull still works, so only the send direction is blocked.
	ClipboardViewOnly
	// ClipboardFailed reports that no display was open or the write failed.
	ClipboardFailed
)

// EncodeLatin1 converts UTF-8 text into the RFB baseline clipboard encoding.
// The result is ClipboardSent on success, or the exact reason the text cannot
// be sent. It never truncates: a rune above U+00FF reports
// ClipboardNotLatin1, and text over MaxClipboardBytes reports
// ClipboardTooLarge before any byte is returned.
func EncodeLatin1(text string) ([]byte, ClipboardResult) {
	if text == "" {
		return nil, ClipboardEmpty
	}
	// Never size the buffer from the untrusted input before the limit is
	// checked: a multi-gigabyte paste would otherwise allocate its full
	// length just to reject it. The loop still stops at the same limit and
	// returns the same result.
	capacity := len(text)
	if capacity > MaxClipboardBytes {
		capacity = MaxClipboardBytes
	}
	encoded := make([]byte, 0, capacity)
	for _, r := range text {
		if r > 0xFF {
			return nil, ClipboardNotLatin1
		}
		if len(encoded) >= MaxClipboardBytes {
			return nil, ClipboardTooLarge
		}
		encoded = append(encoded, byte(r))
	}
	return encoded, ClipboardSent
}

// DecodeLatin1 converts RFB clipboard bytes (stored in a Go string) into the
// UTF-8 text the local clipboard holds. Every byte is a code point, as the
// baseline protocol requires.
func DecodeLatin1(text string) string {
	if text == "" {
		return ""
	}
	runes := make([]rune, len(text))
	for i := 0; i < len(text); i++ {
		runes[i] = rune(text[i])
	}
	return string(runes)
}

// SendClipboard writes text to the server's clipboard after Latin-1
// conversion. The result is stored for the chrome's localized notice. View-only
// refuses the write before encoding or sending, so the wire stays untouched
// while clipboard pull keeps working.
func (s *Surface) SendClipboard(text string) ClipboardResult {
	if !s.canSendInput() {
		s.setClipboardResult(ClipboardViewOnly)
		return ClipboardViewOnly
	}
	encoded, result := EncodeLatin1(text)
	if result != ClipboardSent {
		s.setClipboardResult(result)
		return result
	}
	if s.session == nil {
		s.setClipboardResult(ClipboardFailed)
		return ClipboardFailed
	}
	if err := s.session.SendCutText(string(encoded)); err != nil {
		s.setClipboardResult(ClipboardFailed)
		return ClipboardFailed
	}
	s.setClipboardResult(ClipboardSent)
	return ClipboardSent
}

// PullClipboard writes the last remote clipboard text to the local clipboard
// through write. It reports ClipboardNoRemoteText when the server has not sent
// any text yet.
func (s *Surface) PullClipboard(write func(string)) ClipboardResult {
	s.mu.Lock()
	text, hasText := s.remoteText, s.hasRemoteText
	s.mu.Unlock()
	if !hasText {
		s.setClipboardResult(ClipboardNoRemoteText)
		return ClipboardNoRemoteText
	}
	if write == nil {
		s.setClipboardResult(ClipboardFailed)
		return ClipboardFailed
	}
	write(text)
	s.setClipboardResult(ClipboardPulled)
	return ClipboardPulled
}

// LastRemoteText returns the most recent ServerCutText decoded from Latin-1,
// or "" before any arrives.
func (s *Surface) LastRemoteText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remoteText
}

// HasRemoteText reports whether the server has sent clipboard text.
func (s *Surface) HasRemoteText() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasRemoteText
}

// ClipboardMessage is the localized sentence for a clipboard outcome.
func (s *Surface) ClipboardMessage(result ClipboardResult) string {
	locale := s.locale
	if locale == "" {
		locale = "en"
	}
	m := locales.In(locale)
	switch result {
	case ClipboardSent:
		return m.SessionVNCClipboardSent()
	case ClipboardPulled:
		return m.SessionVNCClipboardPulled()
	case ClipboardEmpty:
		return m.SessionVNCClipboardEmpty()
	case ClipboardTooLarge:
		return m.SessionVNCClipboardTooLarge()
	case ClipboardNotLatin1:
		return m.SessionVNCClipboardNotLatin1()
	case ClipboardNoRemoteText:
		return m.SessionVNCClipboardNoRemoteText()
	case ClipboardViewOnly:
		return m.SessionVNCClipboardViewOnly()
	default:
		return m.SessionVNCClipboardFailed()
	}
}

// setClipboardResult records the latest outcome for the notice overlay.
func (s *Surface) setClipboardResult(result ClipboardResult) {
	s.mu.Lock()
	s.clipboardResult = &result
	s.mu.Unlock()
	s.requestRedraw()
}

// clipboardFailed reports whether the notice is a failure, so the chrome
// can color it with the warning tone instead of success.
func clipboardFailed(result ClipboardResult) bool {
	switch result {
	case ClipboardSent, ClipboardPulled:
		return false
	default:
		return true
	}
}

// syncRemoteClipboard puts a freshly arrived ServerCutText on the local
// clipboard. It runs on the UI thread because the clipboard is a UI service;
// the display consumer only marks the text pending.
func (s *Surface) syncRemoteClipboard(c *ui.Context) {
	s.mu.Lock()
	pending := s.clipboardPending
	text := s.remoteText
	s.clipboardPending = false
	s.mu.Unlock()
	if pending {
		c.WriteClipboard(text)
	}
}
