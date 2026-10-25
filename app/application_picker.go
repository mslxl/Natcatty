package app

import (
	"path/filepath"

	"github.com/egoist/mygo"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// chooseOpenApplication asks for one executable through the native open
// dialog. It is the application-picker seam the Settings associations form
// calls; the self-test scripts the same seam instead of opening a dialog.
func chooseOpenApplication() (domainpreferences.ExternalApplication, bool) {
	language := mygo.App.Locale()
	if language == "" {
		language = "en"
	}
	m := locales.In(language)
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title: m.SettingsChooseApplication(),
		Filters: []mygo.FileFilter{
			{Name: m.SettingsOpenerExternal(), Extensions: []string{"*"}},
		},
	})
	if err != nil || len(paths) == 0 {
		return domainpreferences.ExternalApplication{}, false
	}
	path := paths[0]
	if path == "" {
		return domainpreferences.ExternalApplication{}, false
	}
	return domainpreferences.ExternalApplication{
		Path: path,
		Name: filepath.Base(path),
	}, true
}
