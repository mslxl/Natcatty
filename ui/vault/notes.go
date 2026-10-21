package vault

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/markdown"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// notesSection lists Note Groups and Vault Notes. A search query flattens the
// tree into matches across title, Markdown source, and group path.
func notesSection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(0, 8).Wrap().Children(func() {
		ui.Text(c, m.VaultNoteListTitle(s.NoteCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultNoteGroupNew()).Clicked() {
			s.BeginCreateNoteGroup()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultNoteExportAll()).Clicked() {
			s.ExportAllNotes()
			c.Invalidate()
		}
		if ui.PrimaryButton(c, m.VaultNoteNew()).Clicked() {
			s.BeginCreateNote()
			c.Invalidate()
		}
	})

	query := s.NoteSearch()
	field := ui.SearchField(c, &query).
		Placeholder(m.VaultNoteSearchPlaceholder()).
		Label(m.VaultNoteSearchLabel())
	if field.Changed() {
		s.SetNoteSearch(query)
	}
	if s.SearchingNotes() {
		matches := s.NotesMatchingSearch()
		if len(matches) == 0 {
			ui.Text(c, m.VaultNoteSearchEmpty(strings.TrimSpace(s.NoteSearch()))).
				FontSize(12).TextColor(t.TextMuted)
			return
		}
		for _, note := range matches {
			noteRow(c, s, note, m, t, 0, true)
		}
		return
	}

	if s.NoteCount() == 0 && s.NoteGroupCount() == 0 {
		ui.Text(c, m.VaultNoteEmpty()).FontSize(12).TextColor(t.TextMuted)
		return
	}

	groups := s.NoteGroups()
	for _, group := range groups {
		noteGroupRow(c, s, group, m, t)
		for _, note := range s.NotesInGroup(group.Path) {
			noteRow(c, s, note, m, t, domainvault.NoteGroupDepth(group.Path)+1, false)
		}
	}
	root := s.NotesInGroup("")
	if len(root) > 0 {
		ui.Text(c, m.VaultNoteRoot()).FontSize(12).TextColor(t.TextMuted)
		for _, note := range root {
			noteRow(c, s, note, m, t, 0, false)
		}
	}
}

func noteGroupRow(c *ui.Context, s *vaultstate.State, group domainvault.NoteGroup, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).PaddingStart(groupIndent(domainvault.NoteGroupDepth(group.Path))).
		Wrap().Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, group.Path).Bold()
			ui.Text(c, m.VaultNoteGroupNoteCount(s.NoteGroupNoteCount(group.Path))).
				FontSize(12).TextColor(t.TextMuted)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditNoteGroup(group.Path)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteNoteGroup(group.Path)
			c.Invalidate()
		}
	})
}

func noteRow(c *ui.Context, s *vaultstate.State, note domainvault.Note, m locales.Messages, t *ui.Theme, depth int, showGroup bool) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).PaddingStart(groupIndent(depth)).
		Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
				ui.Text(c, note.Title).Bold()
				ui.Column(c).Grow(1)
				if ui.PrimaryButton(c, m.VaultNoteOpen()).Clicked() {
					s.BeginViewNote(note.ID)
					c.Invalidate()
				}
				if ui.Button(c, m.VaultHostEdit()).Clicked() {
					s.BeginEditNote(note.ID)
					c.Invalidate()
				}
				if ui.Button(c, m.VaultHostDelete()).Clicked() {
					s.RequestDeleteNote(note.ID)
					c.Invalidate()
				}
			})
			if showGroup && note.Group != "" {
				ui.Text(c, note.Group).FontSize(12).TextColor(t.TextMuted)
			}
		})
	})
}

func noteDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteNote() == "" {
		return
	}
	title := ""
	if note, ok := s.NoteByID(s.PendingDeleteNote()); ok {
		title = note.Title
	}
	if title == "" {
		s.CancelDeleteNote()
		return
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Wrap().Background(t.Surface).Children(func() {
		ui.Text(c, m.VaultNoteDeleteConfirm(title)).Grow(1).TextColor(t.Danger)
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteNote()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteNote()
			c.Invalidate()
		}
	})
}

func noteGroupDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	path := s.PendingDeleteNoteGroup()
	if path == "" {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Wrap().Background(t.Surface).Children(func() {
		ui.Text(c, m.VaultNoteGroupDeleteConfirm(path)).Grow(1).TextColor(t.Danger)
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteNoteGroup()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteNoteGroup()
			c.Invalidate()
		}
	})
}

// noteExportBanner shows where the last export went, or why it failed.
func noteExportBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if message := s.NoteExportProblem(); message != vaultstate.ProblemNone {
		if text := noteExportProblemText(m, message); text != "" {
			ui.Text(c, text).TextColor(t.Danger)
		}
		return
	}
	if path := s.NoteExportPath(); path != "" {
		ui.Text(c, m.VaultNoteExportSaved(path)).FontSize(12).TextColor(t.TextMuted).Selectable()
	}
}

func noteExportProblemText(m locales.Messages, problem vaultstate.Problem) string {
	switch problem {
	case vaultstate.ProblemNoteExport:
		return m.VaultNoteExportFailed()
	case vaultstate.ProblemNoteExportEmpty:
		return m.VaultNoteExportEmpty()
	case vaultstate.ProblemNoteExportWriter:
		return m.VaultNoteExportUnavailable()
	default:
		return problemText(m, problem)
	}
}

func noteEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.NoteForm()
	title := m.VaultNoteFormCreateTitle()
	if form.ID != "" {
		title = m.VaultNoteFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()
		ui.Form(c, func() {
			ui.Field(c, m.VaultNoteTitle(), func() {
				ui.TextInput(c, &form.Title)
			})
			ui.Field(c, m.VaultNoteGroup(), func() {
				ui.TextInput(c, &form.Group).Placeholder("ops/runbooks")
			}).Description(m.VaultNoteGroupHint())
			ui.Field(c, m.VaultNoteContent(), func() {
				ui.TextArea(c, &form.Content).Height(240)
			}).Description(m.VaultNoteContentHint())
		})
		problem(c, s, t)
	})

	editorActions(c, func() { _ = s.SaveNote() }, s.CancelNoteEdit, m)
}

func noteGroupEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.NoteGroupForm()
	title := m.VaultNoteGroupFormCreateTitle()
	if form.OriginalPath != "" {
		title = m.VaultNoteGroupFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()
		ui.Form(c, func() {
			ui.Field(c, m.VaultNoteGroupPath(), func() {
				ui.TextInput(c, &form.Path).Placeholder("ops/runbooks")
			}).Description(m.VaultNoteGroupPathHint())
		})
		problem(c, s, t)
	})

	editorActions(c, func() { _ = s.SaveNoteGroup() }, s.CancelNoteGroupEdit, m)
}

// noteViewer renders a Note's Markdown with native widgets. There is no HTML
// engine or webview: the parsed document becomes text, rows, and columns.
func noteViewer(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	note, ok := s.NoteByID(s.ViewingNoteID())
	if !ok {
		s.CloseNoteView()
		return
	}

	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		ui.Text(c, note.Title).FontSize(20).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultNoteBack()).Clicked() {
			s.CloseNoteView()
			c.Invalidate()
		}
		if ui.PrimaryButton(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditNote(note.ID)
			c.Invalidate()
		}
	})
	if note.Group == "" {
		ui.Text(c, m.VaultNoteViewerRoot()).FontSize(12).TextColor(t.TextMuted)
	} else {
		ui.Text(c, m.VaultNoteViewerGroup(note.Group)).FontSize(12).TextColor(t.TextMuted)
	}
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		if ui.Button(c, m.VaultNoteExportMarkdown()).Clicked() {
			s.ExportNote(appvault.NoteExportMarkdown)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultNoteExportHTML()).Clicked() {
			s.ExportNote(appvault.NoteExportHTML)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultNoteExportZIP()).Clicked() {
			s.ExportNote(appvault.NoteExportZIP)
			c.Invalidate()
		}
	})
	noteExportBanner(c, s, m, t)

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Gap(10).Children(func() {
			noteBlocks(c, markdown.Parse(note.Content).Blocks, m, t, 0)
		})
	})
}

// maxNoteRenderDepth bounds native rendering even if a caller supplies a
// hand-built document.
const maxNoteRenderDepth = 32

func noteBlocks(c *ui.Context, blocks []markdown.Block, m locales.Messages, t *ui.Theme, depth int) {
	if depth > maxNoteRenderDepth {
		ui.Text(c, markdown.PlainText(blocks)).Selectable()
		return
	}
	for _, block := range blocks {
		noteBlock(c, block, m, t, depth)
	}
}

func noteBlock(c *ui.Context, block markdown.Block, m locales.Messages, t *ui.Theme, depth int) {
	switch block.Kind {
	case markdown.HeadingBlock:
		size := float32(14)
		switch block.Level {
		case 1:
			size = 24
		case 2:
			size = 20
		case 3:
			size = 18
		case 4:
			size = 16
		case 5:
			size = 15
		}
		spans := noteSpans(block.Inlines, t, true)
		if len(spans) == 0 {
			return
		}
		ui.RichText(c, spans...).FontSize(size).Bold().Selectable()
	case markdown.CodeBlock:
		ui.Row(c).FillWidth().MinWidth(0).Padding(8).Background(t.Surface).
			Border(1, t.Border).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.RichText(c, ui.Span{Text: block.Code, Font: "monospace"}).Selectable()
			})
		})
	case markdown.QuoteBlock:
		ui.Row(c).FillWidth().MinWidth(0).Padding(8).PaddingStart(12).
			Background(t.Surface).BorderStartWidth(3).BorderColor(t.Accent).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
				noteBlocks(c, block.Blocks, m, t, depth+1)
			})
		})
	case markdown.ListBlock:
		for index, item := range block.Items {
			marker := "•"
			if block.Ordered {
				start := block.Start
				if start <= 0 {
					start = 1
				}
				marker = strconv.Itoa(start+index) + "."
			}
			ui.Row(c).FillWidth().Gap(6).Children(func() {
				ui.Text(c, marker).Width(26).TextColor(t.TextMuted)
				ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
					noteBlocks(c, item.Blocks, m, t, depth+1)
				})
			})
		}
	case markdown.RuleBlock:
		ui.Divider(c).FillWidth()
	case markdown.TableBlock:
		noteTable(c, block, m, t)
	default:
		spans := noteSpans(block.Inlines, t, false)
		if len(spans) == 0 {
			return
		}
		ui.RichText(c, spans...).Selectable()
	}
}

func noteTable(c *ui.Context, block markdown.Block, m locales.Messages, t *ui.Theme) {
	table := func(cells [][]markdown.Inline, bold bool) {
		ui.Row(c).FillWidth().Gap(8).Padding(4, 0).Background(t.Surface).Children(func() {
			for _, cell := range cells {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					spans := noteSpans(cell, t, bold)
					if len(spans) == 0 {
						spans = []ui.Span{{Text: ""}}
					}
					ui.RichText(c, spans...).Selectable()
				})
			}
		})
	}
	if len(block.Header) > 0 {
		table(block.Header, true)
		ui.Divider(c).FillWidth()
	}
	for _, row := range block.Rows {
		table(row, false)
	}
}

// noteSpans turns inline runs into styled spans. Links keep their label and
// show the URL as readable text; images do the same with their alt text.
func noteSpans(inlines []markdown.Inline, t *ui.Theme, bold bool) []ui.Span {
	spans := make([]ui.Span, 0, len(inlines)*2)
	var appendInline func([]markdown.Inline, bool, bool, bool, bool)
	appendInline = func(items []markdown.Inline, strong, emphasis, strike, code bool) {
		for _, inline := range items {
			switch inline.Kind {
			case markdown.InlineStrong:
				appendInline(inline.Children, true, emphasis, strike, code)
			case markdown.InlineEmphasis:
				appendInline(inline.Children, strong, true, strike, code)
			case markdown.InlineStrike:
				appendInline(inline.Children, strong, emphasis, true, code)
			case markdown.InlineCode:
				spans = append(spans, ui.Span{
					Text: inline.Text, Font: "monospace", Background: t.Surface, Radius: 3,
				})
			case markdown.InlineLink, markdown.InlineImage:
				label := inlineText(inline.Children)
				if label == "" {
					label = inline.URL
				}
				spans = append(spans, ui.Span{
					Text: label, Color: t.Accent, Underline: true, Italic: emphasis,
				})
				if inline.URL != "" && inline.URL != label {
					spans = append(spans, ui.Span{Text: " (" + inline.URL + ")", Color: t.TextMuted})
				}
			default:
				if inline.Text == "" {
					continue
				}
				span := ui.Span{
					Text:          inline.Text,
					Italic:        emphasis,
					Strikethrough: strike,
					Underline:     false,
				}
				if strong || bold {
					span.Weight = 700
				}
				if code {
					span.Font = "monospace"
				}
				spans = append(spans, span)
			}
		}
	}
	appendInline(inlines, false, false, false, false)
	return spans
}

// inlineText flattens a run's label without Markdown syntax.
func inlineText(inlines []markdown.Inline) string {
	var builder strings.Builder
	for _, inline := range inlines {
		switch inline.Kind {
		case markdown.InlineLink, markdown.InlineImage:
			builder.WriteString(inlineText(inline.Children))
		case markdown.InlineCode:
			builder.WriteString(inline.Text)
		default:
			if len(inline.Children) > 0 {
				builder.WriteString(inlineText(inline.Children))
			} else {
				builder.WriteString(inline.Text)
			}
		}
	}
	return builder.String()
}
