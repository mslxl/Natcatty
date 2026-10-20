package sessionui

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// SaveScreenFile writes text to a timestamped file in the user's Downloads
// directory, or the temporary directory when Downloads is unavailable. It is
// the default ScreenWriter behind Save current screen.
func SaveScreenFile(text string) (string, error) {
	directory, err := screenDirectory()
	if err != nil {
		return "", err
	}
	name := "natcatty-screen-" + time.Now().Format("20060102-150405") + ".txt"
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func screenDirectory() (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		downloads := filepath.Join(home, "Downloads")
		if info, err := os.Stat(downloads); err == nil && info.IsDir() {
			return downloads, nil
		}
	}
	if temp := os.TempDir(); temp != "" {
		return temp, nil
	}
	return "", errors.New("session: no directory for the screen")
}
