package app

import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/ui/locales"
)

// themeFilePicker opens the native file dialog for an iTerm2 color scheme.
// Only this adapter touches the user's filesystem; the returned name is the
// base name the service derives the scheme name from.
type themeFilePicker struct {
	locale func() string
}

func (p themeFilePicker) Pick() (name string, data []byte, err error) {
	m := locales.In("en")
	if p.locale != nil {
		if tag := p.locale(); tag != "" {
			m = locales.In(tag)
		}
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title: m.SettingsTerminalThemeImport(),
		Filters: []mygo.FileFilter{
			{Name: m.SettingsTerminalThemeImportFilter(), Extensions: []string{"itermcolors", "xml", "plist"}},
			{Name: m.VaultImportFileFilterAll(), Extensions: []string{"*"}},
		},
	})
	if err != nil {
		return "", nil, err
	}
	if len(paths) == 0 {
		return "", nil, nil
	}
	path := paths[0]
	data, err = os.ReadFile(path)
	if err != nil {
		return filepath.Base(path), nil, err
	}
	return filepath.Base(path), data, nil
}
