package sysmanager

import appsysmanager "github.com/mslxl/natcatty/application/sysmanager"

// tabLayoutEntry is one tab's row of the tab layout menu: the state its Show,
// Collapse, and Hide choices carry as Checked, and whether the choices are
// Disabled because the tab is locked. It is separate from ui.Menu so the
// checked and disabled state can be tested; the native menu reports only its
// labels.
type tabLayoutEntry struct {
	Tab                                          appsysmanager.Tab
	Locked                                       bool
	ShowChecked, CollapseChecked, HideChecked    bool
	ShowDisabled, CollapseDisabled, HideDisabled bool
}

// tabLayoutEntries reports each System Tab in menu order with the state its
// layout choices show. Visibility and collapse are exclusive: a visible tab
// checks Show, a collapsed tab checks Collapse, and a hidden one checks Hide.
// Overview is locked, so all three choices are disabled for it.
func tabLayoutEntries(s *State) []tabLayoutEntry {
	tabs := s.AllTabs()
	entries := make([]tabLayoutEntry, 0, len(tabs))
	for _, tab := range tabs {
		visible := s.TabVisible(tab)
		collapsed := s.TabCollapsed(tab)
		locked := tab == appsysmanager.TabOverview
		entries = append(entries, tabLayoutEntry{
			Tab:              tab,
			Locked:           locked,
			ShowChecked:      visible,
			CollapseChecked:  collapsed,
			HideChecked:      !visible && !collapsed,
			ShowDisabled:     locked,
			CollapseDisabled: locked,
			HideDisabled:     locked,
		})
	}
	return entries
}
