package phoneview

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// fakeWebView stands in for a platform web view. The embedded interface lets
// it satisfy ui.NativeWebView without naming MyGo's internal surface types,
// which this module cannot import; the headless runtime never calls the
// promoted method.
type fakeWebView struct{ ui.NativeWebView }

func TestWebAreaShowsTheNativeWebView(t *testing.T) {
	native := &fakeWebView{}
	tt := ui.NewTester(func(c *ui.Context) {
		WebArea(c, native)
	}, 320, 240)
	if got := tt.WebViewAt(160, 120); got != native {
		t.Fatalf("WebViewAt(center) = %v, want the native web view", got)
	}
}
