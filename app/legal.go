// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"os"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/adapters/bundle"
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
	roots := bundle.Roots(resourceDir, executable, workingDir)
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
