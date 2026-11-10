package pluginview

// Rich text. A rich_text node is one host-rendered paragraph made of
// text_span children. Each span carries its own style, and a span with a link
// is an inline host link: MyGo opens it in the host browser. The ABI carries
// only text and closed properties, never HTML or markup, so stored markup-like
// text renders literally and the host owns every glyph, style, and selection.

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
)

// richText renders the paragraph and reports a changed selection. MyGo's
// selection scope is a Selectable box around the paragraphs, so the host
// wraps the host-rendered paragraph in one. The selection is host-owned and
// never reaches the guest until its fingerprint changes.
func (s *Surface) richText(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	children := append([]domainnativeui.NodeID(nil), node.Children...)
	element := ui.Box(c).Selectable().Children(func() {
		var paragraph ui.Element
		if len(children) == 0 {
			paragraph = ui.RichText(c, ui.Span{Text: node.Text})
		} else {
			paragraph = ui.RichText(c)
		}
		paragraph = s.styleText(node, paragraph)
		if raw, ok := node.Prop(domainnativeui.PropTextToken); ok && raw.Kind == domainnativeui.PropInt {
			paragraph = paragraph.TextColor(tokenColor(c, domainnativeui.Token(raw.Int)))
		}
		if len(children) > 0 {
			paragraph = paragraph.Children(func() {
				for _, id := range children {
					span, ok := tree.Nodes[id]
					if !ok || span == nil || span.BoolProp(domainnativeui.PropHidden, false) {
						continue
					}
					s.textSpan(c, span)
				}
			})
		}
	})
	s.followRichSelection(node, element)
	return element
}

// textSpan builds one inline run. Plain spans are inline text; a span with a
// link is an inline link that keeps the paragraph's flow and its own style.
func (s *Surface) textSpan(c *ui.Context, span *domainnativeui.Node) {
	if span.Text == "" {
		return
	}
	var element ui.Element
	if link := span.StringProp(domainnativeui.PropLink); link != "" {
		element = ui.Link(c, span.Text, link)
	} else {
		element = ui.Text(c, span.Text)
	}
	element = s.styleText(span, element)
	if raw, ok := span.Prop(domainnativeui.PropTextToken); ok && raw.Kind == domainnativeui.PropInt {
		element = element.TextColor(tokenColor(c, domainnativeui.Token(raw.Int)))
	}
}

// styleText applies the shared rich-text style properties to one paragraph
// or span.
func (s *Surface) styleText(node *domainnativeui.Node, element ui.Element) ui.Element {
	if size := node.FloatProp(domainnativeui.PropFontSize, 0); size > 0 {
		element = element.FontSize(float32(size))
	}
	if weight := node.IntProp(domainnativeui.PropFontWeight, 0); weight == 1 || weight >= 600 {
		element = element.Bold()
	}
	if node.BoolProp(domainnativeui.PropItalic, false) {
		element = element.Italic()
	}
	if node.BoolProp(domainnativeui.PropUnderline, false) {
		element = element.Underline()
	}
	if node.BoolProp(domainnativeui.PropStrikethrough, false) {
		element = element.Strikethrough()
	}
	if family, ok := s.fontFamily(node.StringProp(domainnativeui.PropFontAsset)); ok {
		element = element.Font(family)
	}
	return element
}

// followRichSelection queues one selection event when the selected text
// changes. The first frame establishes the baseline without an event, so
// merely rendering a paragraph is not a selection.
func (s *Surface) followRichSelection(node *domainnativeui.Node, element ui.Element) {
	parts := element.SelectedTexts()
	fingerprint := ""
	var selected strings.Builder
	for i, part := range parts {
		if i > 0 {
			fingerprint += "|"
			selected.WriteString("\n")
		}
		fingerprint += strconv.FormatUint(part.ID, 10) + ":" +
			strconv.Itoa(part.Start) + ":" + strconv.Itoa(part.End)
		selected.WriteString(runeSlice(part.Text, part.Start, part.End))
	}
	previous, seen := s.richSelections[node.ID]
	if !seen {
		s.richSelections[node.ID] = fingerprint
		return
	}
	if previous == fingerprint {
		return
	}
	s.richSelections[node.ID] = fingerprint
	s.emit(node.ID, domainnativeui.Event{
		Kind: domainnativeui.EventSelection,
		Text: selected.String(),
	})
}

// runeSlice returns runes [start, end) of text without splitting a rune.
func runeSlice(text string, start, end int) string {
	runes := []rune(text)
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start > end || start >= len(runes) {
		return ""
	}
	return string(runes[start:end])
}
