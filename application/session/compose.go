package session

import (
	"strings"
	"time"
)

// ComposeLineMode selects how a composed command with several lines reaches
// the shell. Paste is the original default: one write containing the whole
// text. Line by line mirrors the original snippet "line delay" mode: each line
// is submitted as its own write.
type ComposeLineMode string

const (
	// ComposePaste sends the composed text as one write.
	ComposePaste ComposeLineMode = "paste"
	// ComposeLineByLine submits each line separately.
	ComposeLineByLine ComposeLineMode = "line_by_line"
)

// ComposeLineDelay is the original auto-run multi-line pacing
// (AUTO_RUN_SNIPPET_LINE_DELAY_MS).
const ComposeLineDelay = 250 * time.Millisecond

// ComposeOptions are the original input transformations the compose bar
// applies before sending.
type ComposeOptions struct {
	// Trim removes leading and trailing whitespace from the whole draft.
	Trim bool
	// DecodeEscapes interprets \n, \r, \t, \e, and \\ before sending.
	DecodeEscapes bool
	// LineMode defaults to ComposePaste.
	LineMode ComposeLineMode
	// NoAutoRun omits the trailing carriage return. The original compose bar
	// always submits with Enter, so the default (false) auto-runs.
	NoAutoRun bool
}

// ComposePreview is the transformed compose draft. Text is what the user can
// preview; Chunks are the exact bytes each target receives, in order.
type ComposePreview struct {
	Text     string
	Chunks   [][]byte
	LineMode ComposeLineMode
	AutoRun  bool
}

// Bytes returns the number of bytes sent to each target.
func (p ComposePreview) Bytes() int {
	total := 0
	for _, chunk := range p.Chunks {
		total += len(chunk)
	}
	return total
}

// Empty reports that the draft produced nothing to send.
func (p ComposePreview) Empty() bool { return len(p.Chunks) == 0 }

// PreviewText renders Text with control characters visible, so the preview
// shows what Escape decoding and line joining produced.
func (p ComposePreview) PreviewText() string {
	replacer := strings.NewReplacer(
		"\r", `\r`,
		"\n", `\n`,
		"\t", `\t`,
		"\x1b", `\e`,
	)
	return replacer.Replace(p.Text)
}

// TransformCompose applies the original compose transformations:
// zero-width input sanitation, optional escape decoding, CRLF/CR line-ending
// normalization, optional trimming, and then either one paste write with the
// submit carriage return or one write per line.
func TransformCompose(text string, options ComposeOptions) ComposePreview {
	transformed := SanitizeTerminalInput(text)
	if options.DecodeEscapes {
		transformed = decodeTerminalTextEscapes(transformed)
	}
	transformed = normalizeLineEndings(transformed)
	if options.Trim {
		transformed = strings.TrimSpace(transformed)
	}

	mode := options.LineMode
	if mode != ComposeLineByLine {
		mode = ComposePaste
	}
	autoRun := !options.NoAutoRun

	preview := ComposePreview{Text: transformed, LineMode: mode, AutoRun: autoRun}
	if transformed == "" {
		// The original compose bar does not submit an empty draft.
		return preview
	}

	if mode == ComposePaste {
		payload := transformed
		if autoRun {
			payload += "\r"
		}
		preview.Chunks = [][]byte{[]byte(payload)}
		return preview
	}

	// Line by line: the final newline is the submit action, not an empty line.
	body := transformed
	if autoRun {
		body = strings.TrimSuffix(body, "\n")
	}
	if body == "" {
		return preview
	}
	for _, line := range strings.Split(body, "\n") {
		if autoRun {
			preview.Chunks = append(preview.Chunks, []byte(line+"\r"))
		} else {
			preview.Chunks = append(preview.Chunks, []byte(line+"\n"))
		}
	}
	return preview
}

// SanitizeTerminalInput removes zero-width formatting characters that IMEs
// emit, preserving ZWNJ and ZWJ, which carry meaning.
func SanitizeTerminalInput(data string) string {
	return strings.Map(func(character rune) rune {
		switch character {
		case 0x00ad, 0x200b, 0x200e, 0x200f,
			0x2060, 0x2061, 0x2062, 0x2063, 0x2064, 0xfeff:
			return -1
		}
		return character
	}, data)
}

// decodeTerminalTextEscapes implements the original Shift+Enter text escapes:
// \n, \r, \t, \e, and \\ decode; any other backslash sequence stays literal.
func decodeTerminalTextEscapes(text string) string {
	var decoded strings.Builder
	for index := 0; index < len(text); index++ {
		character := text[index]
		if character != '\\' || index >= len(text)-1 {
			decoded.WriteByte(character)
			continue
		}
		switch text[index+1] {
		case 'n':
			decoded.WriteByte('\n')
			index++
		case 'r':
			decoded.WriteByte('\r')
			index++
		case 't':
			decoded.WriteByte('\t')
			index++
		case 'e':
			decoded.WriteByte('\x1b')
			index++
		case '\\':
			decoded.WriteByte('\\')
			index++
		default:
			decoded.WriteByte(character)
		}
	}
	return decoded.String()
}

// normalizeLineEndings implements the original helper: CRLF and a bare CR
// become LF.
func normalizeLineEndings(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}
