package terminal

// VTCorpus is the fixed escape-sequence corpus the phone terminal diagnostic
// shows and the rendering tests assert. It is shared so the same bytes prove
// the same grid, style runs and cursor on the desktop and, later, Android.
//
//	row 0: hello
//	row 1: red green true inv      (palette red, bold green, RGB, inverse)
//	row 2: blue-bg plain           (palette background)
//	row 3: the cursor, moved to column 9 as a steady bar
const VTCorpus = "hello\r\n" +
	"\x1b[31mred\x1b[0m \x1b[1;32mgreen\x1b[0m \x1b[38;2;1;2;3mtrue\x1b[0m \x1b[7minv\x1b[0m\r\n" +
	"\x1b[44mblue-bg\x1b[0m plain\r\n" +
	"\x1b[4;10H\x1b[6 q\x1b[?25h"

// VTCorpusText is the text VTCorpus writes, as Terminal.Text reports it.
const VTCorpusText = "hello\nred green true inv\nblue-bg plain"

// VTCorpusCursor is where VTCorpus leaves the cursor, in cells: row 3,
// column 9.
const (
	VTCorpusCursorX = 9
	VTCorpusCursorY = 3
)
