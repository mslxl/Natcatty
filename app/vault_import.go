package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/ui/locales"
)

// importFilePicker opens the native file dialog and reads the selected
// inventory. Only this adapter touches the user's filesystem. The locale seam
// keeps the dialog labels in the same catalogs as the rest of the UI.
type importFilePicker struct {
	locale func() string
}

func (p importFilePicker) PickImportFile() (string, []byte, error) {
	m := locales.In("en")
	if p.locale != nil {
		if tag := p.locale(); tag != "" {
			m = locales.In(tag)
		}
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title: m.VaultImportTitle(),
		Filters: []mygo.FileFilter{
			{Name: m.VaultImportFileFilterHosts(), Extensions: []string{
				"csv", "tsv", "txt", "reg", "ini", "json", "conf", "config", "mxtsessions",
			}},
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
	content, err := os.ReadFile(path)
	if err != nil {
		return path, nil, err
	}
	return path, content, nil
}

// importFileReader reads an IdentityFile referenced by an imported inventory.
// A leading "~" is expanded so ssh_config's common paths work.
type importFileReader struct{}

func (importFileReader) ReadImportFile(path string) ([]byte, error) {
	return os.ReadFile(expandImportPath(path))
}

func expandImportPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
