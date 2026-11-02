package vault

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/markdown"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/vault/section"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// SidePanels are the Scripts and Notes tools of a Session's side panel. They
// read the Vault state the Vault pages use, keeping only their own search and
// the Note being read, so the pages and the panel never fight over a filter.
type SidePanels struct {
	state *vaultstate.State
	// open shows a Vault page, for what the panel leaves to it: creating and
	// editing Snippets and Notes.
	open func(section.Section)

	scriptSearch string
	runScroll    ui.ScrollState

	noteSearch string
	noteID     string
	noteScroll ui.ScrollState
}

// NewSidePanels returns the side panel's Vault tools over s. open shows a
// Vault page in the main window.
func NewSidePanels(s *vaultstate.State, open func(section.Section)) *SidePanels {
	return &SidePanels{state: s, open: open}
}

func (p *SidePanels) openPage(page section.Section) {
	if p.open != nil {
		p.open(page)
	}
}

// sideHeader is a tool's search field with its actions at the end.
func sideHeader(c *ui.Context, query *string, placeholder string, actions func()) bool {
	changed := false
	ui.Row(c).FillWidth().Gap(4).Padding(8, 8, 4, 8).AlignItems(ui.Center).Shrink(0).Children(func() {
		changed = ui.SearchField(c, query).Placeholder(placeholder).Label(placeholder).
			Grow(1).MinWidth(0).Changed()
		actions()
	})
	return changed
}

// Scripts is the side panel's Snippet library, as the original's Scripts
// tool: the Vault's Snippets by package, and a click runs one in the focused
// Session. A Snippet with variables asks for them first.
func (p *SidePanels) Scripts() func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()
		s := p.state
		ui.Column(c).Fill().MinWidth(0).Children(func() {
			if form := s.SnippetRunForm(); form != nil && form.Destination == vaultstate.SnippetDestinationActive {
				ui.Column(c).Fill().MinWidth(0).Padding(12).Gap(8).Children(func() {
					snippetRunEditor(c, s, m, t, &p.runScroll)
				})
				return
			}
			if sideHeader(c, &p.scriptSearch, m.SidePanelSearchScripts(), func() {
				if kit.IconButton(c, icons.Plus, m.VaultSnippetNew()).Clicked() {
					s.BeginCreateSnippet()
					p.openPage(section.Snippets)
				}
			}) {
				c.Invalidate()
			}
			if s.SnippetRunProblem() != vaultstate.ProblemNone {
				ui.Box(c).FillWidth().Padding(0, 8, 4, 8).Children(func() {
					kit.Notice(c, icons.TriangleAlert, t.Danger, sideRunProblem(m, s), func() {
						if kit.IconButton(c, icons.X, m.SidePanelDismiss()).Clicked() {
							s.CancelSnippetRun()
						}
					})
				})
			}
			snippets := p.matchingSnippets()
			if len(snippets) == 0 {
				text := m.VaultSnippetEmpty()
				if strings.TrimSpace(p.scriptSearch) != "" {
					text = m.SidePanelNoMatches()
				}
				kit.EmptyState(c, icons.Zap, text, "", nil)
				return
			}
			ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Padding(0, 8, 8, 8).Gap(2).Children(func() {
					pkg := "\x00"
					for _, snippet := range snippets {
						if snippet.Package != pkg {
							pkg = snippet.Package
							if pkg != "" {
								sideGroupLabel(c, icons.Folder, pkg)
							}
						}
						p.scriptRow(c, m, t, snippet)
					}
				})
			})
		})
	}
}

// matchingSnippets are the Snippets whose label, command, package, or tags
// contain the search, packaged ones first by package, as the library lists
// them.
func (p *SidePanels) matchingSnippets() []domainvault.Snippet {
	query := strings.ToLower(strings.TrimSpace(p.scriptSearch))
	var packaged, root []domainvault.Snippet
	for _, snippet := range p.state.Snippets() {
		if query != "" && !strings.Contains(strings.ToLower(snippet.Label+"\n"+snippet.Command+"\n"+
			snippet.Package+"\n"+strings.Join(snippet.Tags, " ")), query) {
			continue
		}
		if snippet.Package == "" {
			root = append(root, snippet)
		} else {
			packaged = append(packaged, snippet)
		}
	}
	// A stable sort by package keeps each package's own order.
	for i := 1; i < len(packaged); i++ {
		for j := i; j > 0 && packaged[j].Package < packaged[j-1].Package; j-- {
			packaged[j], packaged[j-1] = packaged[j-1], packaged[j]
		}
	}
	return append(packaged, root...)
}

func (p *SidePanels) scriptRow(c *ui.Context, m locales.Messages, t *ui.Theme, snippet domainvault.Snippet) {
	c.Key("side-snippet-" + snippet.ID)
	row := ui.ButtonBase(c).Label(snippet.Label).Tooltip(m.SidePanelRunSnippet()).FillWidth().
		Padding(6, 8).Gap(8).Radius(kit.RadiusControl).AlignItems(ui.Center)
	hovered := row.Hovered()
	if hovered {
		row.Background(kit.Subtle(t))
	}
	row.Children(func() {
		ui.Box(c).Size(26, 26).Radius(6).Background(t.Accent.Alpha(0.12)).Center().TextColor(t.Accent).Shrink(0).
			Children(func() { ui.Icon(c, icons.Lucide(icons.Zap)).Size(13, 13) })
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, snippet.Label).FontSize(13).FontWeight(500).SingleLine()
			ui.Text(c, firstLine(snippet.Command)).FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
		if hovered {
			ui.Icon(c, icons.Lucide(icons.Play)).Size(14, 14).TextColor(t.Accent)
		}
	})
	if row.Clicked() {
		p.state.BeginSnippetRun(snippet.ID, vaultstate.SnippetDestinationActive)
		c.Invalidate()
	}
}

// sideRunProblem is why the last run from the panel did not reach the
// Session.
func sideRunProblem(m locales.Messages, s *vaultstate.State) string {
	if text := problemText(m, s.SnippetRunProblem()); text != "" {
		return text
	}
	return m.VaultSnippetProblemRun()
}

// sideGroupLabel is a package or Note Group heading in a tool's list.
func sideGroupLabel(c *ui.Context, icon, label string) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(6).Padding(8, 8, 2, 8).AlignItems(ui.Center).TextColor(t.TextMuted).Children(func() {
		ui.Icon(c, icons.Lucide(icon)).Size(12, 12)
		ui.Text(c, label).FontSize(11).FontWeight(600).SingleLine().TextColor(t.TextMuted)
	})
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if line, _, found := strings.Cut(text, "\n"); found {
		return line + " …"
	}
	return text
}

// Notes is the side panel's Note reader, as the original's Notes tool: the
// Vault's Notes by group, and a Note opens in place with its Markdown
// rendered. Writing a Note happens on the Vault's Notes page.
func (p *SidePanels) Notes() func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()
		s := p.state
		ui.Column(c).Fill().MinWidth(0).Children(func() {
			if p.noteID != "" {
				if note, ok := s.NoteByID(p.noteID); ok {
					p.noteReader(c, m, t, note)
					return
				}
				p.noteID = ""
			}
			if sideHeader(c, &p.noteSearch, m.VaultNoteSearchPlaceholder(), func() {
				if kit.IconButton(c, icons.Plus, m.VaultNoteNew()).Clicked() {
					s.BeginCreateNote()
					p.openPage(section.Notes)
				}
			}) {
				c.Invalidate()
			}
			notes := p.matchingNotes()
			if len(notes) == 0 {
				text := m.VaultNoteEmpty()
				if strings.TrimSpace(p.noteSearch) != "" {
					text = m.VaultNoteSearchEmpty(strings.TrimSpace(p.noteSearch))
				}
				kit.EmptyState(c, icons.NotebookPen, text, "", nil)
				return
			}
			ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Padding(0, 8, 8, 8).Gap(2).Children(func() {
					group := "\x00"
					for _, note := range notes {
						if note.Group != group {
							group = note.Group
							if group != "" {
								sideGroupLabel(c, icons.Folder, group)
							}
						}
						p.noteRow(c, t, note)
					}
				})
			})
		})
	}
}

// matchingNotes are the Notes whose title or text contain the search,
// grouped Notes first in the Vault's group order.
func (p *SidePanels) matchingNotes() []domainvault.Note {
	query := strings.ToLower(strings.TrimSpace(p.noteSearch))
	matches := func(note domainvault.Note) bool {
		return query == "" || strings.Contains(strings.ToLower(note.Title+"\n"+note.Content+"\n"+note.Group), query)
	}
	var out []domainvault.Note
	for _, group := range p.state.NoteGroups() {
		for _, note := range p.state.NotesInGroup(group.Path) {
			if matches(note) {
				out = append(out, note)
			}
		}
	}
	for _, note := range p.state.NotesInGroup("") {
		if matches(note) {
			out = append(out, note)
		}
	}
	return out
}

func (p *SidePanels) noteRow(c *ui.Context, t *ui.Theme, note domainvault.Note) {
	c.Key("side-note-" + note.ID)
	row := ui.ButtonBase(c).Label(note.Title).FillWidth().Padding(6, 8).Gap(8).
		Radius(kit.RadiusControl).AlignItems(ui.Center)
	if row.Hovered() {
		row.Background(kit.Subtle(t))
	}
	row.Children(func() {
		ui.Box(c).Size(26, 26).Radius(6).Background(kit.Muted(t)).Center().TextColor(t.TextMuted).Shrink(0).
			Children(func() { ui.Icon(c, icons.Lucide(icons.FileText)).Size(13, 13) })
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, note.Title).FontSize(13).FontWeight(500).SingleLine()
			if preview := notePreview(note.Content); preview != "" {
				ui.Text(c, preview).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
	})
	if row.Clicked() {
		p.noteID = note.ID
		p.noteScroll = ui.ScrollState{}
		c.Invalidate()
	}
}

// notePreview is a Note's first line of text, without Markdown markers.
func notePreview(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#>-*+ "))
		if line != "" {
			return line
		}
	}
	return ""
}

// noteReader shows one Note in the panel: back, its title, Edit, which opens
// the Vault's editor, and the rendered Markdown.
func (p *SidePanels) noteReader(c *ui.Context, m locales.Messages, t *ui.Theme, note domainvault.Note) {
	ui.Row(c).FillWidth().Gap(4).Padding(8, 8, 4, 4).AlignItems(ui.Center).Shrink(0).Children(func() {
		if kit.IconButton(c, icons.ArrowLeft, m.VaultNoteBack()).Clicked() {
			p.noteID = ""
			c.Invalidate()
		}
		ui.Text(c, note.Title).FontSize(14).FontWeight(600).SingleLine().Grow(1).MinWidth(0)
		if kit.IconButton(c, icons.Pencil, m.VaultHostEdit()).Clicked() {
			p.state.BeginEditNote(note.ID)
			p.openPage(section.Notes)
		}
	})
	if note.Group != "" {
		ui.Text(c, note.Group).FontSize(11).TextColor(t.TextMuted).Padding(0, 12, 4, 12)
	}
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).TrackScroll(&p.noteScroll).Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(4, 12, 12, 12).Gap(10).Children(func() {
			noteBlocks(c, markdown.Parse(note.Content).Blocks, m, t, 0)
		})
	})
}
