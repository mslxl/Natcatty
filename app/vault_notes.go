package app

import (
	"os"

	"github.com/egoist/mygo"
)

// noteFileWriter saves a Vault Note export through the native save dialog.
// The application layer generates the bytes; only this adapter touches the
// user's filesystem.
type noteFileWriter struct{}

func (noteFileWriter) Save(name string, content []byte) (string, error) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{DefaultPath: name})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
