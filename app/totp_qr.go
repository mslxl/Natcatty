package app

import (
	"os"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/adapters/qrimage"
	"github.com/mslxl/natcatty/ui/locales"
)

// totpQRPicker opens the native image dialog, reads the chosen file, and
// decodes its QR payloads. Only this adapter touches the filesystem and the
// decoder; the Vault state receives payload strings and never an image.
type totpQRPicker struct {
	locale func() string
}

func (p totpQRPicker) PickTOTPQRPayloads() ([]string, error) {
	m := locales.In("en")
	if p.locale != nil {
		if tag := p.locale(); tag != "" {
			m = locales.In(tag)
		}
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title: m.VaultHostTOTPImportQR(),
		Filters: []mygo.FileFilter{
			{Name: m.VaultHostTOTPImportQR(), Extensions: []string{"png", "jpg", "jpeg"}},
			{Name: m.VaultImportFileFilterAll(), Extensions: []string{"*"}},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	content, err := os.ReadFile(paths[0])
	if err != nil {
		return nil, err
	}
	return qrimage.Decode(content)
}
