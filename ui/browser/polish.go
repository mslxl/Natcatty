package browser

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/ui/locales"
)

// polishPane is one of the panel's local management surfaces. A pane replaces
// the page while it is open, so it fits the 440-DIP content column at the
// minimum window without overlays or clipped controls.
type polishPane int

const (
	paneNone polishPane = iota
	paneBookmarks
	paneDownloads
)

// bookmarkForm is the in-pane editor state. A bookmark edit keeps the id so
// the application service can preserve creation time and ownership.
type bookmarkForm struct {
	open      bool
	editingID string
	title     string
	url       string
	scope     domainbrowser.BookmarkScope
	err       error
}

func (p *Panel) togglePane(pane polishPane) {
	if p.pane == pane {
		p.pane = paneNone
	} else {
		p.pane = pane
	}
	p.downloadErr = nil
	p.bookmarkForm.err = nil
}

func (p *Panel) bookmarkPane(c *ui.Context, state appbrowser.State, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().Padding(8).Gap(6).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, m.BrowserBookmarks()).FontSize(13).Bold().Grow(1).
				SingleLine().TextColor(t.Text)
			if ui.PrimaryButton(c, m.BrowserBookmarkAdd()).Height(26).Clicked() {
				active, _ := activeTab(state)
				p.bookmarkForm = bookmarkForm{
					open:      true,
					title:     active.Title,
					url:       active.URL,
					scope:     domainbrowser.ScopeHost,
					editingID: "",
				}
				c.Invalidate()
			}
			if ui.Button(c, "×").Label(m.BrowserClose()).Width(28).Height(26).Clicked() {
				p.pane = paneNone
				c.Invalidate()
			}
		})
		if p.bookmarkForm.open {
			p.bookmarkEditor(c, m, t)
		}
		if len(p.core.VisibleBookmarks()) == 0 {
			ui.Column(c).Grow(1).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.BrowserBookmarkEmpty()).FontSize(11).
					TextAlign(ui.Center).TextColor(t.TextMuted)
			})
			return
		}
		scroll := ui.Scroll(c).Grow(1).FillWidth().MinHeight(0)
		scroll.Children(func() {
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				for _, bookmark := range p.core.VisibleBookmarks() {
					p.bookmarkRow(c, bookmark, m, t)
				}
			})
		})
	})
}

func (p *Panel) bookmarkEditor(c *ui.Context, m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Gap(4).Padding(6).
		Background(t.Surface).Children(func() {
		if p.bookmarkForm.err != nil {
			ui.Text(c, bookmarkErrorText(p.bookmarkForm.err, m)).FontSize(10).
				SingleLine().TextColor(t.Danger)
		}
		ui.TextInput(c, &p.bookmarkForm.title).FillWidth().
			Label(m.BrowserBookmarkTitle())
		ui.TextInput(c, &p.bookmarkForm.url).FillWidth().
			Label(m.BrowserBookmarkAddress())
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.BrowserBookmarkScope()).FontSize(10).TextColor(t.TextMuted)
			if ui.Radio(c, &p.bookmarkForm.scope, domainbrowser.ScopeHost,
				m.BrowserBookmarkThisHost()).Changed() {
				c.Invalidate()
			}
			if ui.Radio(c, &p.bookmarkForm.scope, domainbrowser.ScopeAllHosts,
				m.BrowserBookmarkAllHosts()).Changed() {
				c.Invalidate()
			}
		})
		ui.Row(c).Gap(6).Children(func() {
			if ui.PrimaryButton(c, m.BrowserBookmarkSave()).Height(26).Clicked() {
				p.saveBookmark(c)
			}
			if ui.Button(c, m.BrowserBookmarkCancel()).Height(26).Clicked() {
				p.bookmarkForm = bookmarkForm{}
				c.Invalidate()
			}
			if p.bookmarkForm.editingID != "" {
				if ui.Button(c, m.BrowserBookmarkDelete()).Height(26).Clicked() {
					if _, err := p.core.RemoveBookmark(p.bookmarkForm.editingID); err != nil {
						p.bookmarkForm.err = err
					} else {
						p.bookmarkForm = bookmarkForm{}
					}
					c.Invalidate()
				}
			}
		})
	})
}

func (p *Panel) saveBookmark(c *ui.Context) {
	_, err := p.core.UpsertBookmark(domainbrowser.BookmarkInput{
		ID:    p.bookmarkForm.editingID,
		Title: p.bookmarkForm.title,
		URL:   p.bookmarkForm.url,
		Scope: p.bookmarkForm.scope,
	})
	if err != nil {
		p.bookmarkForm.err = err
		c.Invalidate()
		return
	}
	p.bookmarkForm = bookmarkForm{}
	c.Invalidate()
}

func (p *Panel) bookmarkRow(c *ui.Context, bookmark appbrowser.BookmarkView, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Key("browser-bookmark-"+bookmark.ID).FillWidth().Height(34).
		Padding(4, 2).Gap(6).AlignItems(ui.Center).Children(func() {
		if bitmap := p.iconBitmap(bookmark.IconDataURL); bitmap != nil {
			ui.Image(c, bitmap).Width(16).Height(16).Shrink(0)
		} else {
			glyph := bookmark.IconGlyph
			if glyph == "" {
				glyph = "◇"
			}
			ui.Text(c, glyph).FontSize(13).Width(16).TextAlign(ui.Center).
				TextColor(t.Accent)
		}
		column := ui.Column(c).Grow(1).MinWidth(0).Gap(0)
		column.Children(func() {
			if ui.Button(c, bookmark.Title).FillWidth().Height(18).SingleLine().Clicked() {
				p.openBookmark(c, bookmark.URL)
			}
			address := bookmark.URL
			if bookmark.Scope == domainbrowser.ScopeAllHosts {
				address = m.BrowserBookmarkAllHosts() + " · " + address
			}
			ui.Text(c, address).FontSize(9).SingleLine().TextColor(t.TextMuted)
		})
		if ui.Button(c, m.BrowserBookmarkEdit()).Width(68).Height(24).Clicked() {
			p.bookmarkForm = bookmarkForm{
				open:      true,
				editingID: bookmark.ID,
				title:     bookmark.Title,
				url:       bookmark.URL,
				scope:     bookmark.Scope,
			}
			c.Invalidate()
		}
		if ui.Button(c, "×").Label(m.BrowserBookmarkDelete()).Width(26).Height(24).Clicked() {
			if _, err := p.core.RemoveBookmark(bookmark.ID); err != nil {
				p.bookmarkForm.err = err
			}
			c.Invalidate()
		}
	})
}

func (p *Panel) openBookmark(c *ui.Context, rawURL string) {
	state := p.core.State()
	active, ok := activeTab(state)
	if !ok {
		id, err := p.core.NewTab()
		if err != nil {
			p.setProblem(err)
			c.Invalidate()
			return
		}
		active = appbrowser.Tab{ID: id}
	}
	gatewayURL, err := p.core.Navigate(active.ID, rawURL)
	if err != nil {
		p.setProblem(err)
		c.Invalidate()
		return
	}
	p.pane = paneNone
	p.loadPage(active.ID, gatewayURL)
	c.Invalidate()
}

func bookmarkErrorText(err error, m locales.Messages) string {
	switch {
	case errors.Is(err, domainbrowser.ErrAddressEmpty),
		errors.Is(err, domainbrowser.ErrAddressInvalid):
		return m.BrowserInvalidAddress()
	case errors.Is(err, domainbrowser.ErrAddressScheme):
		return m.BrowserInvalidScheme()
	case errors.Is(err, domainbrowser.ErrAddressCredentials):
		return m.BrowserCredentials()
	case errors.Is(err, domainbrowser.ErrBookmarkLimit):
		return m.BrowserBookmarkLimit()
	case errors.Is(err, domainbrowser.ErrAddressTooLong):
		return m.BrowserInvalidAddress()
	default:
		return m.BrowserBookmarkSaveFailed()
	}
}

func (p *Panel) downloadPane(c *ui.Context, m locales.Messages, t *ui.Theme) {
	items := p.core.Downloads()
	ui.Column(c).Grow(1).FillWidth().Padding(8).Gap(6).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, m.BrowserDownloads()).FontSize(13).Bold().Grow(1).
				SingleLine().TextColor(t.Text)
			if ui.Button(c, m.BrowserDownloadClear()).Height(26).Clicked() {
				p.core.ClearDownloads()
				c.Invalidate()
			}
			if ui.Button(c, "×").Label(m.BrowserClose()).Width(28).Height(26).Clicked() {
				p.pane = paneNone
				c.Invalidate()
			}
		})
		if p.downloadErr != nil {
			ui.Text(c, downloadErrorText(p.downloadErr, m)).FontSize(10).
				SingleLine().TextColor(t.Danger)
		}
		if len(items) == 0 {
			ui.Column(c).Grow(1).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.BrowserDownloadEmpty()).FontSize(11).
					TextAlign(ui.Center).TextColor(t.TextMuted)
			})
			return
		}
		scroll := ui.Scroll(c).Grow(1).FillWidth().MinHeight(0)
		scroll.Children(func() {
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				for _, item := range items {
					p.downloadRow(c, item, m, t)
				}
			})
		})
	})
}

func (p *Panel) downloadRow(c *ui.Context, item appbrowser.Download, m locales.Messages, t *ui.Theme) {
	active := item.Status == appbrowser.DownloadWaiting || item.Status == appbrowser.DownloadProgressing
	ui.Row(c).Key("browser-download-"+item.ID).FillWidth().MinHeight(42).
		Padding(4, 2).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, item.Filename).FontSize(11).Bold().SingleLine().TextColor(t.Text)
			ui.Text(c, downloadStatusText(item, m)).FontSize(9).SingleLine().
				TextColor(t.TextMuted)
			if active {
				value := float64(0)
				if item.Total > 0 {
					value = float64(item.Received) / float64(item.Total)
				}
				ui.Progress(c, value).FillWidth().Height(4)
			}
		})
		if active {
			if ui.Button(c, m.BrowserDownloadCancel()).Height(24).Clicked() {
				p.core.CancelDownload(item.ID)
				c.Invalidate()
			}
		} else if ui.Button(c, "×").Label(m.BrowserDownloadRemove()).Width(26).Height(24).Clicked() {
			p.core.RemoveDownload(item.ID)
			c.Invalidate()
		}
	})
}

func downloadStatusText(item appbrowser.Download, m locales.Messages) string {
	switch item.Status {
	case appbrowser.DownloadWaiting:
		return m.BrowserDownloadWaiting()
	case appbrowser.DownloadProgressing:
		if item.Total > 0 {
			percent := int(float64(item.Received) * 100 / float64(item.Total))
			return fmt.Sprintf("%s · %d%% · %s / %s", m.BrowserDownloadProgressing(),
				percent, humanBytes(item.Received), humanBytes(item.Total))
		}
		return fmt.Sprintf("%s · %s", m.BrowserDownloadProgressing(), humanBytes(item.Received))
	case appbrowser.DownloadCompleted:
		return fmt.Sprintf("%s · %s", m.BrowserDownloadCompleted(), humanBytes(item.Received))
	case appbrowser.DownloadCancelled:
		return m.BrowserDownloadCancelled()
	case appbrowser.DownloadFailed:
		switch item.Failure {
		case appbrowser.DownloadFailureTooLarge:
			return m.BrowserDownloadTooLarge()
		case appbrowser.DownloadFailureNetwork:
			return m.BrowserDownloadNetwork()
		case appbrowser.DownloadFailureStatus:
			return m.BrowserDownloadStatus()
		case appbrowser.DownloadFailureWrite:
			return m.BrowserDownloadWrite()
		default:
			return m.BrowserDownloadFailed()
		}
	default:
		return string(item.Status)
	}
}

func downloadErrorText(err error, m locales.Messages) string {
	if errors.Is(err, appbrowser.ErrDownloadURL) {
		return m.BrowserDownloadRefused()
	}
	if errors.Is(err, appbrowser.ErrDownloadDir) {
		return m.BrowserDownloadNoDirectory()
	}
	return m.BrowserDownloadStartFailed()
}

func humanBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KB", "MB", "GB"}
	size := float64(value)
	for _, unit := range units {
		size /= 1024
		if size < 1024 {
			return fmt.Sprintf("%.1f %s", size, unit)
		}
	}
	return fmt.Sprintf("%.1f TB", size/1024)
}

// iconBitmap decodes a favicon data URL once per distinct value. A failed
// decode is remembered as a miss, so an undecodable icon keeps the caller's
// glyph fallback instead of failing every frame.
func (p *Panel) iconBitmap(dataURL string) *ui.Bitmap {
	if dataURL == "" {
		return nil
	}
	if p.iconCache == nil {
		p.iconCache = make(map[string]*ui.Bitmap)
		p.iconMiss = make(map[string]bool)
	}
	if bitmap, ok := p.iconCache[dataURL]; ok {
		return bitmap
	}
	if p.iconMiss[dataURL] {
		return nil
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 || !strings.HasSuffix(dataURL[:comma], ";base64") {
		p.iconMiss[dataURL] = true
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		p.iconMiss[dataURL] = true
		return nil
	}
	bitmap, err := ui.DecodeBitmap(raw)
	if err != nil {
		p.iconMiss[dataURL] = true
		return nil
	}
	if len(p.iconCache) > 128 {
		p.iconCache = make(map[string]*ui.Bitmap)
		p.iconMiss = make(map[string]bool)
	}
	p.iconCache[dataURL] = bitmap
	return bitmap
}

// startDownloadFromPage is called by the page event bridge. A failed start is
// kept for the downloads pane; a successful one opens the pane so progress is
// visible without another click.
func (p *Panel) startDownloadFromPage(gatewayURL, suggested string) {
	_, err := p.core.StartDownload(gatewayURL, suggested)
	p.downloadErr = err
	p.pane = paneDownloads
}
