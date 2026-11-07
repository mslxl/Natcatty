// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/internal/legal"
	"github.com/mslxl/natcatty/ui/settings"
)

// loadLegalDocuments reads the license, modification notice, and third-party
// notices the app ships. The packaged app keeps all three in its resource
// directory; a development checkout keeps LICENSE and NOTICE at the repository
// root and the notices under resources/. Searching both keeps the About area
// useful under `go run` without weakening the release check, which requires
// every packaged artifact to carry the files.
func loadLegalDocuments(version string) settings.LegalDocuments {
	resourceDir, _ := mygo.App.Path(mygo.PathResources)
	executable, _ := os.Executable()
	workingDir, _ := os.Getwd()
	roots := legalRoots(resourceDir, executable, workingDir)
	// The repository root is the last candidate: packaged resources win, so a
	// stray file in the process's working directory cannot shadow the shipped
	// legal text.
	roots = append(roots, workingDir)
	documents := legal.Load(roots...)
	return settings.LegalDocuments{
		Version:           version,
		License:           documents.License,
		Notice:            documents.Notice,
		ThirdPartyNotices: documents.ThirdPartyNotices,
		SourceURL:         legal.SourceTagURL(version),
	}
}

// legalRoots assembles the search order for the shipped documents: an explicit
// environment override, the toolkit's resource directory, the executable's
// directory, a sibling Resources directory (packaged macOS), and the working
// directory's resources (go build and go test). Duplicates and empty entries
// are dropped.
func legalRoots(resourceDir, executablePath, workingDir string) []string {
	var roots []string
	add := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			absolute = root
		}
		for _, existing := range roots {
			if strings.EqualFold(existing, absolute) {
				return
			}
		}
		roots = append(roots, absolute)
	}
	if override := os.Getenv("NATCATTY_RESOURCES"); override != "" {
		add(override)
	}
	add(resourceDir)
	if executablePath != "" {
		exeDir := filepath.Dir(executablePath)
		add(exeDir)
		add(filepath.Join(exeDir, "..", "Resources"))
	}
	if workingDir != "" {
		add(filepath.Join(workingDir, "resources"))
	}
	return roots
}
