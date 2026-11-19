package phoneshell

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The native tab bar. It is separated from the sections because it is the one
// part the phone build only reaches through MyGo's application object, so the
// section tests drive the routers without it (the tester has no app).

// Show creates one window per section and the native tab bar over them. The
// tab bar's choice is recorded on the shell, so Selected follows the user.
// Show must run on the main thread once MyGo is ready (App.WhenReady).
func (s *Shell) Show() error {
	for i := range s.sections {
		s.windows[i] = mygo.NewWindow(mygo.WindowOptions{
			Title:   s.sections[i].info.Title,
			Hidden:  true,
			Content: ui.View(s.sections[i].view),
		})
	}
	items := make([]mygo.TabItem, len(s.sections))
	for i, sec := range s.sections {
		items[i] = mygo.TabItem{Title: sec.info.Title, Symbol: sec.info.Symbol, Window: s.windows[i]}
	}
	tabs, err := mygo.NewTabs(items...)
	if err != nil {
		return err
	}
	s.tabs = tabs
	tabs.OnSelect(func(i int) { s.Select(Section(i)) })
	return nil
}

// Tabs returns the native tab bar Show created, or nil before Show.
func (s *Shell) Tabs() *mygo.Tabs { return s.tabs }
