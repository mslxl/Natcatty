package pluginwebview_test

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/pluginwebview"
)

func TestUnavailableAndCrashedPagesShowAReadableSurface(t *testing.T) {
	surface := pluginwebview.New(func() ui.NativeWebView { return nil })
	tt := ui.NewTester(func(c *ui.Context) { surface.View(c) }, 320, 200)
	tt.Frame()
	if !tt.HasText("Web view unavailable") {
		t.Fatalf("placeholder texts = %v", tt.Texts())
	}

	surface.SetProblem(pluginwebview.ProblemCrashed)
	tt.Frame()
	if !tt.HasText("The plugin page crashed. Close and reopen the view to reload it.") {
		t.Fatalf("problem texts = %v", tt.Texts())
	}
	if surface.Problem() != pluginwebview.ProblemCrashed {
		t.Fatal("Problem did not report the error surface")
	}
	surface.SetProblem(pluginwebview.ProblemNone)
	if surface.Problem() != pluginwebview.ProblemNone {
		t.Fatal("Problem did not clear")
	}
}
