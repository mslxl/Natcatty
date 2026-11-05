// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

// These tests compare the Go terminal model and display encoder against an
// oracle compiled from the stock mosh 1.4.0 terminal sources. They are opt-in
// because they need the parity Docker image (see testdata/mosh-parity):
//
//   NATCATTY_MOSH_PARITY=1 go test ./internal/mosh/terminal -run StockParity
//
// The oracle reads terminal octets and remote resizes and emits the exact
// Display::new_frame bytes stock mosh-client would write for each step.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const parityImageEnv = "NATCATTY_MOSH_PARITY_IMAGE"

type terminalEvent struct {
	input  string
	resize [2]int // width, height; used when non-zero
}

func parityImage() string {
	if image := os.Getenv(parityImageEnv); image != "" {
		return image
	}
	return "natcatty/mosh-parity:1.4.0"
}

// terminalParityResult is one oracle run: the exact display bytes for the
// initial frame and each event, plus a deterministic dump of the screen each
// frame leaves behind.
type terminalParityResult struct {
	frames  []string
	screens []string
}

func localTerminalFrames(width, height int, events []terminalEvent) []string {
	return localTerminalResult(width, height, events).frames
}

func localTerminalResult(width, height int, events []terminalEvent) terminalParityResult {
	terminal := NewComplete(width, height)
	display := NewDisplay(DefaultDisplayOptions())
	last := terminal.CloneFramebuffer()
	result := terminalParityResult{
		frames:  []string{display.NewFrame(false, last, terminal.Framebuffer())},
		screens: []string{dumpFramebuffer(terminal.Framebuffer())},
	}
	for _, event := range events {
		if event.resize[0] != 0 {
			terminal.Resize(event.resize[0], event.resize[1])
		} else {
			terminal.Act(event.input)
		}
		result.frames = append(result.frames, display.NewFrame(true, last, terminal.Framebuffer()))
		last = terminal.CloneFramebuffer()
		result.screens = append(result.screens, dumpFramebuffer(terminal.Framebuffer()))
	}
	return result
}

func hexBytes(value string) string { return hex.EncodeToString([]byte(value)) }

func boolDigit(value bool) int {
	if value {
		return 1
	}
	return 0
}

// dumpFramebuffer mirrors the stock driver's screen_dump field order exactly.
// The oracle and this function are the two halves of the committed golden
// terminal screens.
func dumpFramebuffer(fb *Framebuffer) string {
	var out strings.Builder
	width, height := fb.DS.Size()
	col, row := fb.DS.Cursor()
	fmt.Fprintf(&out, "size\t%d\t%d\n", width, height)
	fmt.Fprintf(&out, "cursor\t%d\t%d\n", col, row)
	fmt.Fprintf(&out, "modes\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
		boolDigit(fb.DS.CursorVisible), boolDigit(fb.DS.ReverseVideo),
		boolDigit(fb.DS.BracketedPaste), int(fb.DS.MouseReportingMode),
		boolDigit(fb.DS.MouseFocusEvent), boolDigit(fb.DS.MouseAlternateScroll),
		int(fb.DS.MouseEncodingMode), boolDigit(fb.DS.ApplicationModeCursorKeys),
		boolDigit(fb.DS.AutoWrapMode), boolDigit(fb.DS.OriginMode),
		boolDigit(fb.DS.InsertMode), boolDigit(fb.DS.NextPrintWillWrap),
		int(fb.DS.BackgroundRendition()))
	scrollTop, scrollBottom := fb.DS.ScrollingRegion()
	fmt.Fprintf(&out, "scroll\t%d\t%d\n", scrollTop, scrollBottom)
	fmt.Fprintf(&out, "bell\t%d\n", fb.BellCount)
	fmt.Fprintf(&out, "title_init\t%d\n", boolDigit(fb.TitleInitialized))
	fmt.Fprintf(&out, "title\t%s\n", hexBytes(string(fb.WindowTitle)))
	fmt.Fprintf(&out, "icon\t%s\n", hexBytes(string(fb.IconName)))
	fmt.Fprintf(&out, "clipboard\t%s\n", hexBytes(string(fb.Clipboard)))
	for y, line := range fb.Rows {
		fmt.Fprintf(&out, "row\t%d\t%d", y, boolDigit(line.Wrap()))
		for i := range line.Cells {
			cell := &line.Cells[i]
			var content strings.Builder
			cell.PrintGrapheme(&content)
			fmt.Fprintf(&out, "\t%d,%d,%d,%s,%s",
				boolDigit(cell.Empty()), boolDigit(cell.Wide), boolDigit(cell.Fallback),
				hexBytes(cell.Renditions.SGR()), hexBytes(content.String()))
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func encodeParityEvents(width, height int, events []terminalEvent) []byte {
	var buf bytes.Buffer
	writeU32 := func(v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		buf.Write(b[:])
	}
	writeU32(uint32(width))
	writeU32(uint32(height))
	writeU32(uint32(len(events)))
	for _, event := range events {
		if event.resize[0] != 0 {
			buf.WriteByte(1)
			writeU32(uint32(event.resize[0]))
			writeU32(uint32(event.resize[1]))
			continue
		}
		buf.WriteByte(0)
		writeU32(uint32(len(event.input)))
		buf.WriteString(event.input)
	}
	return buf.Bytes()
}

func decodeParityResult(t *testing.T, data []byte) terminalParityResult {
	t.Helper()
	readU32 := func() uint32 {
		t.Helper()
		if len(data) < 4 {
			t.Fatal("short parity stream")
		}
		v := binary.LittleEndian.Uint32(data)
		data = data[4:]
		return v
	}
	readBlob := func(what string, index int) string {
		t.Helper()
		length := readU32()
		if uint64(length) > uint64(len(data)) {
			t.Fatalf("%s %d length %d exceeds %d bytes", what, index, length, len(data))
		}
		blob := string(data[:length])
		data = data[length:]
		return blob
	}
	frameCount := readU32()
	result := terminalParityResult{frames: make([]string, frameCount)}
	for i := range result.frames {
		result.frames[i] = readBlob("frame", i)
	}
	screenCount := readU32()
	if screenCount != frameCount {
		t.Fatalf("screen count = %d, want %d frames", screenCount, frameCount)
	}
	result.screens = make([]string, screenCount)
	for i := range result.screens {
		result.screens[i] = readBlob("screen", i)
	}
	if len(data) != 0 {
		t.Fatalf("%d trailing bytes in parity stream", len(data))
	}
	return result
}

func decodeParityFrames(t *testing.T, data []byte) []string {
	return decodeParityResult(t, data).frames
}

func referenceTerminalResult(t *testing.T, width, height int, events []terminalEvent) terminalParityResult {
	t.Helper()
	if os.Getenv("NATCATTY_MOSH_PARITY") == "" {
		t.Skip("set NATCATTY_MOSH_PARITY=1 to run stock mosh 1.4.0 comparisons")
	}
	cmd := exec.Command("docker", "run", "--rm", "-i", parityImage())
	cmd.Stdin = bytes.NewReader(encodeParityEvents(width, height, events))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("reference driver: %v\n%s", err, stderr.String())
	}
	return decodeParityResult(t, out)
}

func referenceTerminalFrames(t *testing.T, width, height int, events []terminalEvent) []string {
	return referenceTerminalResult(t, width, height, events).frames
}

func requireStockFrames(t *testing.T, name string, width, height int, events []terminalEvent) {
	t.Helper()
	want := referenceTerminalResult(t, width, height, events)
	got := localTerminalResult(width, height, events)
	if len(got.frames) != len(want.frames) {
		t.Fatalf("%s: frame count = %d, want %d", name, len(got.frames), len(want.frames))
	}
	for i := range want.frames {
		if got.frames[i] != want.frames[i] {
			t.Fatalf("%s: frame %d differs\n got %q\nwant %q", name, i, got.frames[i], want.frames[i])
		}
	}
	for i := range want.screens {
		if got.screens[i] != want.screens[i] {
			t.Fatalf("%s: screen %d differs\n got:\n%s\nwant:\n%s",
				name, i, got.screens[i], want.screens[i])
		}
	}
	if os.Getenv("NATCATTY_MOSH_PARITY_UPDATE") != "" && !strings.HasPrefix(name, "random-") {
		writeParityGolden(t, name, width, height, events, want)
	}
}

const goldenParityDir = "testdata/parity/goldens"

type goldenEvent struct {
	Input  []byte `json:"input,omitempty"`
	Resize []int  `json:"resize,omitempty"`
}

type goldenFile struct {
	Name    string        `json:"name"`
	Width   int           `json:"width"`
	Height  int           `json:"height"`
	Events  []goldenEvent `json:"events"`
	Frames  [][]byte      `json:"frames"`
	Screens []string      `json:"screens"`
}

func writeParityGolden(t *testing.T, name string, width, height int,
	events []terminalEvent, result terminalParityResult) {
	t.Helper()
	if err := os.MkdirAll(goldenParityDir, 0o755); err != nil {
		t.Fatalf("create golden directory: %v", err)
	}
	golden := goldenFile{
		Name: name, Width: width, Height: height,
		Frames:  make([][]byte, len(result.frames)),
		Screens: result.screens,
	}
	for _, event := range events {
		entry := goldenEvent{}
		if event.resize[0] != 0 {
			entry.Resize = []int{event.resize[0], event.resize[1]}
		} else {
			entry.Input = []byte(event.input)
		}
		golden.Events = append(golden.Events, entry)
	}
	for i := range result.frames {
		golden.Frames[i] = []byte(result.frames[i])
	}
	data, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	data = append(data, '\n')
	path := filepath.Join(goldenParityDir, name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write golden %s: %v", path, err)
	}
}

// TestGoldenTerminalScreens runs the committed stock-oracle corpus without
// Docker. The frames are Display::new_frame's exact bytes and the screens are
// the stock terminal state after each event, so this covers wide characters,
// combining marks, unknown SGR, resize/reflow, scrollback, bracketed paste,
// mouse modes, cursor visibility, application cursor keys, and alternate
// screen handling.
func TestGoldenTerminalScreens(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(goldenParityDir, "*.json"))
	if err != nil {
		t.Fatalf("glob goldens: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no committed parity goldens")
	}
	for _, path := range paths {
		path := path
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			var golden goldenFile
			if err := json.Unmarshal(data, &golden); err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			events := make([]terminalEvent, len(golden.Events))
			for i, event := range golden.Events {
				if len(event.Resize) == 2 {
					events[i].resize = [2]int{event.Resize[0], event.Resize[1]}
				} else {
					events[i].input = string(event.Input)
				}
			}
			got := localTerminalResult(golden.Width, golden.Height, events)
			if len(got.frames) != len(golden.Frames) {
				t.Fatalf("frame count = %d, want %d", len(got.frames), len(golden.Frames))
			}
			for i := range golden.Frames {
				if got.frames[i] != string(golden.Frames[i]) {
					t.Fatalf("frame %d differs\n got %q\nwant %q",
						i, got.frames[i], golden.Frames[i])
				}
			}
			if len(got.screens) != len(golden.Screens) {
				t.Fatalf("screen count = %d, want %d", len(got.screens), len(golden.Screens))
			}
			for i := range golden.Screens {
				if got.screens[i] != golden.Screens[i] {
					t.Fatalf("screen %d differs\n got:\n%s\nwant:\n%s",
						i, got.screens[i], golden.Screens[i])
				}
			}
		})
	}
}

func TestStockParityTerminalModel(t *testing.T) {
	scenarios := []struct {
		name   string
		width  int
		height int
		events []terminalEvent
	}{
		{
			name: "wide-combining-unknown-sgr", width: 12, height: 4,
			events: []terminalEvent{
				{input: "A界B é"},
				{input: "\r\ńX"},
				{input: "\033[91mA\033[38;5;196mB\033[38;2;1;2;3mC\033[0mD"},
				{input: "\033[1;3;4;5;7;8mZ\033[22;23;24;25;27;28mY"},
				{input: "\033[38;5;300mQ\033[48;2;255;0;128mR"},
				// Stock packs true-color components into a 25-bit field
				// without masking each byte, so the out-of-range components
				// bleed into their neighbours.
				{input: "\033[38;2;1;2;300mS\033[48;2;255;2;511mT\033[0m"},
				{input: "\033[38;2;0;511;0mU"},
			},
		},
		{
			name: "width-edges", width: 20, height: 6,
			events: []terminalEvent{
				// glibc Zs separators are printable width 1, unlike
				// unicode.IsPrint, which rejects every non-ASCII space.
				{input: "A B C D E F"},
				{input: "\r\nG H I J K L M N"},
				{input: "\r\nO P Q　R"},
				// Hangul leading Jamo are wide; medial/final are zero width.
				{input: "\r\nSᄀᅠᆨTᅟᇿU"},
				// Private-use code points are printable width 1, including
				// the supplementary private-use planes.
				{input: "\r\n\U000F0000\U000FFFFD\U00100000\U0010FFFDU"},
				// Combining and format characters remain zero width.
				{input: "\r\nX́⃝️\U000E0100​⁠￹Y"},
			},
		},
		{
			name: "wrapping", width: 5, height: 4,
			events: []terminalEvent{
				{input: "abcdefgh"},
				{input: "\r\n界界界"},
				{input: "12界3"},
				{input: "\bX"},
				{input: "\033[?7l0123456789\033[?7h"},
				{input: "\r\n12345"},
			},
		},
		{
			name: "resize-reflow", width: 10, height: 4,
			events: []terminalEvent{
				{input: "0123456789\r\nabcdefghij\r\n界界界界界"},
				{resize: [2]int{6, 3}},
				{resize: [2]int{12, 5}},
				{input: "XYZ"},
				{resize: [2]int{4, 6}},
				{input: "hello world"},
				{resize: [2]int{8, 2}},
			},
		},
		{
			name: "scrollback", width: 8, height: 3,
			events: []terminalEvent{
				{input: "line1\r\nline2\r\nline3\r\nline4\r\nline5"},
				{input: "\033[1;2r\033[2;1H\nX"},
				{input: "\033[r\033[S\033[T"},
				{input: "\033[2;1H\033[L\033[2;1H\033[M"},
				{input: "\033[H\033[J\033[1;1Habc\033[K\033[1;2H\033[1K"},
			},
		},
		{
			name: "tabs-insert-delete", width: 12, height: 3,
			events: []terminalEvent{
				{input: "a\tb\tc"},
				{input: "\033[3g\033[4G\033H\tX"},
				{input: "\r\n123456789\033[4G\033[2@AB\033[2P"},
				{input: "\033[1;5H\033[3X"},
				{input: "\033[2Z"},
			},
		},
		{
			name: "modes", width: 10, height: 5,
			events: []terminalEvent{
				{input: "\033[?2004hPASTE\033[?2004l"},
				{input: "\033[?1000h\033[?1006h\033[?1004h"},
				{input: "\033[?1002h\033[?1015h"},
				{input: "\033[?1000l\033[?1006l"},
				{input: "\033[?1003h\033[?1005h\033[?1004l"},
				{input: "\033[?25lHIDDEN\033[?25h"},
				{input: "\033[?5hREV\033[?5l"},
				{input: "\033[?1hAPP\033[?1l"},
				{input: "\033[?1049hALT\033[?1049l"},
				{input: "\033[?47hALT47\033[?47l"},
			},
		},
		{
			name: "title-clipboard-bell", width: 10, height: 3,
			events: []terminalEvent{
				{input: "\033]0;TITLE界\007"},
				{input: "\033]1;ICON\007\033]2;WINDOW\007"},
				{input: "\033]52;c;Y2xpcGJvYXJk\007"},
				{input: "\a"},
				{input: "\033]2;WINDOW2\a"},
				{input: "\033]52;c;\a"},
			},
		},
		{
			name: "cursor-motion-erase", width: 9, height: 4,
			events: []terminalEvent{
				{input: "abc\033[2;3Hdef\033[1A\033[2C\033[0GZ\033[0d"},
				{input: "\033[2;2H\033[4@\033[1P\033[2X"},
				{input: "\033[?6h\033[2;4r\033[HXX\033[?6l"},
				{input: "\033[3;3H\0337\033[1;1H\0338"},
				{input: "\033[#8\033[3;3H"},
			},
		},
		{
			name: "combining-edges", width: 6, height: 3,
			events: []terminalEvent{
				{input: "́Ẫ"},
				{input: "\r\n\033[K̄"},
				{input: "\r\n界̅"},
				{input: "\r\n\033[1;6H界"},
				{resize: [2]int{3, 2}},
				{input: "̆"},
				{resize: [2]int{8, 3}},
				{input: "̇"},
			},
		},
		{
			name: "invalid-utf8", width: 8, height: 3,
			events: []terminalEvent{
				{input: "\xffA\xc3B\xe2\x82C\xf0\x9f\x92D"},
				{input: "\xed\xa0\x80\xf4\x90\x80\x80"},
				{input: "\xc0\xaf\xe0\x80\xaf"},
				{input: "\x80\x80\x80"},
			},
		},
		{
			name: "c1-controls", width: 8, height: 3,
			events: []terminalEvent{
				{input: "\x84X\x85Y\x88\x8dZ"},
				{input: "\033D1\033E2\033H3"},
				{input: "\033[?62c\033[>1;10;0c\033[5n\033[6n"},
				{input: "\033[1;2;3;4mX"},
				{input: "\033[38;5mY\033[38;2;1;2mZ"},
			},
		},
		{
			name: "osc-chunked-and-cancel", width: 8, height: 3,
			events: []terminalEvent{
				{input: "\033]2;par"},
				{input: "tial\007A"},
				{input: "\033]0;x\033\\B"},
				{input: "\033]52;c;data\007"},
				{input: "\033]52;c;more"},
				{input: "\007"},
			},
		},
	}

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			requireStockFrames(t, scenario.name, scenario.width, scenario.height, scenario.events)
		})
	}
}

// TestStockParityRandom exercises arbitrary byte streams to catch parser state
// machine, UTF-8, and display-diff divergences.
func TestStockParityRandom(t *testing.T) {
	if os.Getenv("NATCATTY_MOSH_PARITY") == "" {
		t.Skip("set NATCATTY_MOSH_PARITY=1 to run stock mosh 1.4.0 comparisons")
	}
	alphabet := []string{
		"a", "Z", " ", "~", "\r", "\n", "\t", "\b", "\a", "\x00",
		"界", "é", "́", "é", "Ω", "😀",
		"\033", "\033[", "\033]", "\007", "\x9b", "\x9d", "\x9c", "\x90", "\x98",
		"[", "]", ";", "?", ">", "0", "1", "2", "7", "8", "9", "A", "B", "C", "D", "H", "J", "K", "L", "M", "P", "S", "T", "X", "Z", "m", "r", "h", "l", "n", "c", "d", "f", "g",
		"\xff", "\xc3", "\xe2\x82", "\xf0\x9f",
	}
	seed := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}
	for scenario := 0; scenario < 25; scenario++ {
		width := 4 + int(next()%12)
		height := 2 + int(next()%5)
		var events []terminalEvent
		for step := 0; step < 12; step++ {
			if next()%7 == 0 {
				events = append(events, terminalEvent{resize: [2]int{
					2 + int(next()%12), 1 + int(next()%5)}})
				continue
			}
			length := int(next() % 24)
			var input bytes.Buffer
			for i := 0; i < length; i++ {
				input.WriteString(alphabet[next()%uint64(len(alphabet))])
			}
			events = append(events, terminalEvent{input: input.String()})
		}
		name := fmt.Sprintf("random-%d-%dx%d", scenario, width, height)
		requireStockFrames(t, name, width, height, events)
	}
}
