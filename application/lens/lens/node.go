// Package lens is Terminal's command lens in Go: a command is matched, its output
// parsed line by line, and its state turned into a view tree of native
// components that the views render.
package lens

// Node is a node of a lens view. The native UI renders it with MyGo's ui
// widgets; a web renderer would render the same tree to the DOM.
type Node interface{ isNode() }

// Span is a run of text with style tokens, as Terminal's flow.ui.span.
type Span struct {
	Text  string
	Style string // space separated: "muted", "strong error", "key", "num", "path", "link"
	Href  string
}

// Align is a table column's alignment.
type Align int

const (
	AlignStart Align = iota
	AlignCenter
	AlignEnd
)

// Truncate says where a table cell that does not fit is cut.
type Truncate int

const (
	TruncateEnd Truncate = iota
	TruncateStart
	TruncateMiddle
)

// Col is a table column, as Terminal's flow.ui.Col.
type Col struct {
	ID       string
	Head     string
	Align    Align
	Priority int     // higher numbers hide first when narrow; 0 never hides
	Grow     float32 // share of the spare width
	Truncate Truncate
}

// MeterPart is one stacked part of a meter.
type MeterPart struct {
	Value float64
	Token string
}

// Thresholds tint a meter warn/bad at or above them.
type Thresholds struct{ Warn, Bad float64 }

// MeterCell is a thin-bar cell in a table.
type MeterCell struct {
	Value      float64
	Thresholds *Thresholds
	Parts      []MeterPart
	Tone       string
}

// Cell is a table cell, as Terminal's Cell: spans, a meter, text after a mark, a
// badge, a button or a sparkline.
type Cell struct {
	Spans  []Span
	Meter  *MeterCell
	Text   *TextCell
	Badge  *BadgeCell
	Button *ButtonCell
	Spark  *SparkCell
}

// TextCell is spans after a status mark (a tone dot or an icon): Terminal's
// {text, icon, dot, tone, title} cell.
type TextCell struct {
	Text  []Span
	Icon  string
	Dot   string // tone of the 6px status dot; wins over Icon
	Tone  string // icon tone
	Title string
}

// BadgeCell is the badge look inside a table cell.
type BadgeCell struct {
	Text  string
	Tone  string
	Title string
}

// ButtonCell is a small button in a table cell; a click sends an action event
// and does not select the row.
type ButtonCell struct {
	Text     string
	Act      string
	Icon     string
	Keys     []string
	Variant  string
	Disabled bool
	Reason   string
}

// SparkCell is a sparkline the height of the text, eight cells wide unless its
// column grows.
type SparkCell struct {
	Values []float64
	Tone   string
	Min    *float64
	Max    *float64
	Title  string
}

// TreeNode is a node of a disclosure tree.
type TreeNode struct {
	Label    []Span
	Icon     string
	Open     bool
	Children []TreeNode
}

// Item is a row of a list.
type Item struct {
	Label  []Span
	Detail []Span
	Value  []Span
	Icon   string
	Tone   string
}

// Series is one point of a bar or spark series.
type Series struct {
	Label string
	Value float64
}

// Severity values a Diagnostic can carry, as Terminal's `Severity` type
// (`"error" | "warning" | "note" | "help"`, flow.exe strings 801143-801148).
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityNote    = "note"
	SeverityHelp    = "help"
)

// Suggestion is one suggested edit under a diagnostic, as Terminal's
// `{message, firstLine, code}`.
type Suggestion struct {
	Message   string
	FirstLine int
	Code      string
}

// Diagnostic is a compiler or linter diagnostic, matching Terminal's `Diag`
// (flow.exe strings 801145-801164): severity (default error), code, message,
// as-printed path, 1-based line/column, a snippet with the line number of its
// first line, notes, and suggested edits. Terminal renders it with
// `flow.ui.diagnostic`, which composes card + row(code badge) + text(message,
// path:line:col) + code(snippet) + list(notes/suggestions).
type Diagnostic struct {
	Severity     string // error, warning, note, help; empty means error
	Code         string
	Message      string
	Path         string
	Line, Col    int
	Snippet      string
	SnippetFirst int
	Notes        []string
	Suggestions  []Suggestion
}

// Nodes.

type Text struct{ Spans []Span }

type Column struct {
	Children []Node
	Gap      float32
}

type Row struct{ Children []Node }

// Lines stacks children with no gap: Terminal's flow.ui.lines, the lines of one
// text stream (as opposed to col's small gaps).
type Lines struct{ Children []Node }

// StatItem is one tile of a Stats well: what is counted, the large figure, an
// optional line under it, a tone for the value and a tooltip.
type StatItem struct {
	Label []Span
	Value []Span
	Sub   []Span
	Tone  string
	Title string
}

// Stats is a well of stat tiles side by side, wrapping when narrow.
type Stats struct{ Items []StatItem }

// El is Terminal's HTML-subset element (flow.ui.el): a tag, a class, data/aria
// attributes, optional text and children; an input carries its type, name,
// value, checked and disabled state.
type El struct {
	Tag      string
	Class    string
	Attrs    map[string]string
	Text     string
	Children []Node
	Input    string // "checkbox" or "radio" (input only)
	Name     string
	Value    string
	Checked  bool
	Disabled bool
}

// TSP is any TSP node (Terminal's flow.ui.node): tabs, overlay, and future kinds.
type TSP struct {
	Kind     string
	Props    map[string]any
	Children []Node
}

// Button is Terminal's flow.ui.button: a chip, the main action, a quiet action or a
// destructive one; disabled and busy buttons send nothing.
type Button struct {
	ID       string
	Text     string
	Icon     string
	Keys     []string
	Variant  string // default, primary, quiet, danger
	Size     string // md, sm
	On       bool
	Disabled bool
	Reason   string
	Busy     bool
	Title    string
	Act      string
}

// Image is a picture Terminal shows as an image surface: the bytes as read, the
// path it came from and an alt text. Terminal's file surfaces and inline graphics
// both become one.
type Image struct {
	Data []byte
	Path string
	Alt  string
	// Growing says Data is still arriving: the command that prints the picture
	// has not finished, so the bytes are a prefix of the file. The renderer then
	// draws as much of the picture as the prefix allows and keeps growing it,
	// instead of waiting for the end (or calling a partial file broken).
	Growing bool
	// Total is how many bytes the picture is expected to be, when the lens knows
	// (a file's size from stat). Zero when unknown. It drives the progress
	// reading shown while the picture arrives.
	Total int64
	// Complete is how many bytes of Data are the picture proper, when trailing
	// bytes were cut (see viewfile's trimImageBytes). It lets the growing view
	// tell "the file's end has arrived" from "more is still coming".
	Complete int
	// PlaceholderSize is the picture's pixel size, when the lens could read it
	// from a prefix the decoder rejected (GIF's header names it). The block then
	// holds that shape with a stand-in and a progress reading, instead of
	// collapsing to a line of text and jumping when the picture lands.
	PlaceholderSize [2]int
}

// CardProps are the head extras Terminal's flow.ui.card accepts besides its head:
// a leading icon, dim metadata after the head text, and the id of a child drawn
// at the head's end instead of the body.
type CardProps struct {
	Icon string
	Meta []Span
	End  string
}

type Card struct {
	Head      []Span
	Icon      string
	Meta      []Span
	End       string
	Tone      string
	Collapsed bool
	Children  []Node
}

type Section struct {
	Head      []Span
	Collapsed bool
	Children  []Node
}

type KV struct{ Items [][2][]Span }

type Badge struct {
	Text string
	Tone string
}

// TableOpts are Terminal's flow.ui.table options: rows take clicks, a row id is
// drawn selected, and empty is shown muted and centred when there are no rows.
type TableOpts struct {
	Selectable bool
	Selected   string
	Empty      []Span
}

// RowOpts is Terminal's TableRow metadata: an id for events, disabled (with an
// optional tooltip reason), right-click menu action names, and a tooltip.
type RowOpts struct {
	ID       string
	Disabled bool
	Reason   string
	Menu     []string
	Title    string
}

type Table struct {
	ID   string // events name it as their id
	Cols []Col
	Rows [][]Cell
	Opts TableOpts
	// RowOpts names rows and adds their disabled/menu/title state; it may be
	// shorter than Rows, and missing entries are plain rows.
	RowOpts []RowOpts
}

type Tree struct{ Nodes []TreeNode }

type List struct {
	Items []Item
	// Filter draws a filter box above the rows and narrows them as it is typed,
	// the way Terminal's lenses add an `input` from flow.ui.el and filter on change.
	Filter bool
}

type Code struct {
	Text  string
	Lang  string
	Start int
	Clamp int // max lines before "N more"; 0 = no clamp
}

type Diff struct {
	Text    string
	Path    string
	Split   bool
	Added   int // `+` lines, shown as +N in the head (Terminal's "ins" tone)
	Removed int // `-` lines, shown as −N in the head (U+2212, Terminal's "del" tone)
}

type Markdown struct{ Text string }

type Meter struct {
	Value      float64
	Label      []Span
	Thresholds *Thresholds
	Parts      []MeterPart
}

type Progress struct {
	Value float64
	Known bool // false draws the indeterminate bar
	Label []Span
}

type Bars struct{ Series []Series }
type Spark struct{ Series []Series }

type TestSummary struct {
	Passed, Failed, Skipped int
	Took                    string
}

type Overflow struct{ Hidden int }

type ANSI struct{ Text string }

// Rule is a separator with an optional label, as Terminal's flow.ui.rule: a hairline
// on each side of the label, or one line when there is no label.
type Rule struct{ Label []Span }

type Key struct{ Text string }

func (Text) isNode()        {}
func (Column) isNode()      {}
func (Row) isNode()         {}
func (Lines) isNode()       {}
func (Stats) isNode()       {}
func (El) isNode()          {}
func (TSP) isNode()         {}
func (Button) isNode()      {}
func (Image) isNode()       {}
func (Card) isNode()        {}
func (Section) isNode()     {}
func (KV) isNode()          {}
func (Badge) isNode()       {}
func (Table) isNode()       {}
func (Tree) isNode()        {}
func (List) isNode()        {}
func (Code) isNode()        {}
func (Diff) isNode()        {}
func (Rule) isNode()        {}
func (Markdown) isNode()    {}
func (Meter) isNode()       {}
func (Progress) isNode()    {}
func (Bars) isNode()        {}
func (Spark) isNode()       {}
func (TestSummary) isNode() {}
func (Overflow) isNode()    {}
func (ANSI) isNode()        {}
func (Key) isNode()         {}
func (Diagnostic) isNode()  {}

// Helpers the lenses use to build views concisely.

// T is a plain text node.
func T(s string) Node { return Text{Spans: []Span{{Text: s}}} }

// S is a styled text node.
func S(style, s string) Node { return Text{Spans: []Span{{Text: s, Style: style}}} }

// Spans builds a text node from spans.
func Spans(sp ...Span) Node { return Text{Spans: sp} }

// Cols stacks nodes vertically.
func Cols(n ...Node) Node { return Column{Children: n} }

// Rows places nodes in a horizontal row.
func Rows(n ...Node) Node { return Row{Children: n} }
