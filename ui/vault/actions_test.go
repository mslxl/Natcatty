package vault

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

// chooseVaultAction opens the Hosts toolbar's More actions menu and chooses
// item, as a user reaches New Group, Import, Export, and Backups.
func chooseVaultAction(tt *ui.Tester, m locales.Messages, item string) error {
	if err := tt.Click(m.VaultMoreActions()); err != nil {
		return err
	}
	return tt.ChooseMenuItem(item)
}
