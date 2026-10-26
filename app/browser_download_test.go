package app

import "testing"

// WebView2 reports a canceled navigation with platform status codes, while the
// Linux engine uses ERR_ABORTED. None of them means the page that is still
// displayed failed: Stop and a navigation the engine turned into a download
// both abort this way.
func TestAbortedBrowserNavigationsAreNotPageFailures(t *testing.T) {
	for _, code := range []int{-3, 9, 14} {
		if !browserLoadIsAborted(code) {
			t.Errorf("load code %d was treated as a page failure", code)
		}
	}
}

func TestRealBrowserLoadFailuresStillReachThePanel(t *testing.T) {
	for _, code := range []int{0, 1, 6, 7, 8, 10, 11, 12, 15, 16, 17, 18} {
		if browserLoadIsAborted(code) {
			t.Errorf("load code %d was hidden as an aborted navigation", code)
		}
	}
}
