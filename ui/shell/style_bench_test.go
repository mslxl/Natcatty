package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/internal/styletest"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// BenchmarkTerminalFrame is a terminal Tab's frame, split in two with the
// side panel open, on the CPU renderer in each Interface Style, so the cost
// of the macOS style's glass shows beside Classic's.
func BenchmarkTerminalFrame(b *testing.B) {
	theme := uitheme.UI(true, domaintheme.ResolvePreset(true, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme), uitheme.Options{})
	b.Cleanup(func() { kit.SetStyle(nil) })
	for _, style := range styletest.Styles() {
		b.Run(style.ID(), func(b *testing.B) {
			service := appsession.NewService(multiFactory{})
			b.Cleanup(service.CloseAll)
			graph := &workspaceViewGraph{state: state.NewState(
				state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
					return fakeSurface{}, nil
				}),
			)}
			if err := graph.state.NewTab(); err != nil {
				b.Fatal(err)
			}
			if err := graph.state.SplitRight(); err != nil {
				b.Fatal(err)
			}
			graph.waitForSurfaces(b, 2)
			tt := ui.NewTester(styletest.Styled(style, theme, View(graph.state)), 1280, 800)
			tt.SetVibrancy(true)
			tt.Frame()
			b.ResetTimer()
			// Painting is damage-based; a one-pixel resize repaints the whole
			// window every frame, the worst case of heavy output.
			for i := range b.N {
				tt.SetSize(1280+i%2, 800)
				tt.Frame()
			}
		})
	}
}
