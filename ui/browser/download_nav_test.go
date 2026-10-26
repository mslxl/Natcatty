package browser

import (
	"testing"

	appbrowser "github.com/mslxl/natcatty/application/browser"
)

// A response that becomes a download aborts the navigation the panel asked
// for. The page still on screen must keep its committed address and must not
// be left in a loading state; the download pane owns the new outcome.
func TestDownloadNavigationKeepsTheShownPage(t *testing.T) {
	h := newHarness(t, "en", 868, 688)
	h.waitRoute(t, appbrowser.RouteConnected)
	h.frame()
	h.navigate(t, "http://localhost:3000/first")
	h.frame()
	waitFor(t, "the first page", func() bool { return h.pages.count() == 1 })
	page := h.pages.at(0)
	page.emit(PageEvent{Kind: PageTitle, Title: "Ticket 48"})
	page.emit(PageEvent{Kind: PageLoaded})
	h.frame()

	first := h.activeTab(t)
	if first.URL != "http://localhost:3000/first" || first.Loading {
		t.Fatalf("committed tab = %+v, want the settled first page", first)
	}
	gatewayURL, err := h.core.Navigate(first.ID, "http://localhost:3000/file.bin")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if got := h.activeTab(t); got.URL != "http://localhost:3000/file.bin" || !got.Loading {
		t.Fatalf("provisional tab = %+v, want the download address while loading", got)
	}

	h.panel.onPageEvent(first.ID, page, PageEvent{
		Kind: PageDownload, URL: gatewayURL, SuggestedName: "file.bin",
	})
	h.frame()

	after := h.activeTab(t)
	if after.URL != first.URL {
		t.Fatalf("tab address after a download = %q, want the shown page %q", after.URL, first.URL)
	}
	if after.Loading {
		t.Fatal("tab stayed loading after its navigation became a download")
	}
	if before := h.panel.pane; before != paneDownloads {
		t.Fatalf("pane after a page download = %v, want the downloads pane", before)
	}
}
