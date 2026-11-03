// SPDX-License-Identifier: GPL-3.0-or-later

package legal

import (
	"os"
	"path/filepath"
	"strings"
)

// Documents are the legal texts the About and Licenses surface displays.
// A missing document is empty, and Missing names it, so a development or
// damaged installation still opens the surface and reports the gap instead of
// failing the window.
type Documents struct {
	License           string
	Notice            string
	ThirdPartyNotices string
	Missing           []string
}

// Load reads the legal documents from the first root that contains each one.
// Roots are searched in order; a packaged app puts all three in its resource
// directory, while a development checkout keeps LICENSE and NOTICE at the
// repository root and the notices under resources/.
func Load(roots ...string) Documents {
	var documents Documents
	documents.License = loadOne(roots, LicenseFile, &documents.Missing)
	documents.Notice = loadOne(roots, NoticeFile, &documents.Missing)
	documents.ThirdPartyNotices = loadOne(roots, ThirdPartyNoticesFile, &documents.Missing)
	return documents
}

func loadOne(roots []string, name string, missing *[]string) string {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			return text
		}
	}
	*missing = append(*missing, name)
	return ""
}
