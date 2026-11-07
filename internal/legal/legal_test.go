// SPDX-License-Identifier: GPL-3.0-or-later

package legal

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalGPL3NormalizedSHA256 is the canonical FSF text from
// https://www.gnu.org/licenses/gpl-3.0.txt after collapsing every run of
// whitespace to one space. Hashing the word sequence ignores harmless reflow
// but catches any altered, added, or omitted wording.
const canonicalGPL3NormalizedSHA256 = "972a178adadacfbdddec346b16d45fd4ed9937ec5e4a5bb46d8685ba4e73a0b1"

func TestSourceOfferNamesTheReleaseTagAndInProcessClients(t *testing.T) {
	offer := SourceOfferDocument("v0.1.0")
	for _, want := range []string{
		"# Natcatty 0.1.0 — Corresponding Source",
		"## Corresponding Source",
		SourceTagURL("0.1.0"),
		"https://github.com/mslxl/natcatty/tree/v0.1.0",
		"GPL-3.0-or-later",
		"https://github.com/unixshells/mosh-go",
		"0284bd652aab3dfff5b0b74945c23901d7537b61",
		"Copyright (c) 2026 Unix Shells",
		"internal/mosh",
		"No separate `mosh-client` binary is bundled",
		"https://github.com/tphakala/et-go",
		"bc33dd76b558418bc1079ba27c8c477b8145eea5",
		"Copyright 2026 Tomi P. Hakala",
		"adapters/et",
		"No separate `et` binary is bundled",
		"WITHOUT",
	} {
		if !strings.Contains(offer, want) {
			t.Errorf("source offer does not contain %q:\n%s", want, offer)
		}
	}
}

func TestSourceTagURLFallsBackToTheRepositoryForDevelopment(t *testing.T) {
	for _, version := range []string{"", "dev", "v"} {
		if got := SourceTagURL(version); got != RepositoryURL {
			t.Errorf("SourceTagURL(%q) = %q, want %q", version, got, RepositoryURL)
		}
	}
}

func TestShippedGPL3TextMatchesCanonicalWording(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, name := range []string{
		LicenseFile,
	} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		normalized := strings.Join(strings.Fields(string(data)), " ")
		sum := sha256.Sum256([]byte(normalized))
		if got := hex.EncodeToString(sum[:]); got != canonicalGPL3NormalizedSHA256 {
			t.Errorf("%s does not match the canonical GPLv3 wording: normalized SHA-256 %s, want %s",
				name, got, canonicalGPL3NormalizedSHA256)
		}
	}
}

func TestInProcessMoshNoticeIsShipped(t *testing.T) {
	notice, err := os.ReadFile(filepath.Join("..", "..", "internal", "mosh", "NOTICE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"https://github.com/unixshells/mosh-go",
		"0284bd652aab3dfff5b0b74945c23901d7537b61",
		"MIT License",
		"Copyright (c) 2026 Unix Shells",
	} {
		if !strings.Contains(string(notice), want) {
			t.Errorf("internal/mosh/NOTICE does not contain %q", want)
		}
	}
	packaged, err := os.ReadFile(filepath.Join("..", "..", "resources", "THIRD-PARTY-NOTICES.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packaged), "internal/mosh/NOTICE") {
		t.Error("resources/THIRD-PARTY-NOTICES.md does not point at internal/mosh/NOTICE")
	}
}

func TestThirdPartyNoticesHaveNoUnresolvedTicket15Marker(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", ThirdPartyNoticesFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "TODO(ticket 15)") {
		t.Errorf("%s still contains an unresolved TODO(ticket 15) marker", ThirdPartyNoticesFile)
	}
}

func TestCheckRepositoryAcceptsThisRepository(t *testing.T) {
	if err := CheckRepository(filepath.Join("..", "..")); err != nil {
		t.Fatalf("the repository's legal documents do not pass the release check: %v", err)
	}
}

func TestCheckRepositoryRejectsAMissingOrIncompleteDocument(t *testing.T) {
	root := t.TempDir()
	if err := CheckRepository(root); err == nil {
		t.Fatal("an empty tree passed the repository check")
	}
	writeFile(t, filepath.Join(root, "LICENSE"), "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n")
	writeFile(t, filepath.Join(root, "NOTICE"), "GPL-3.0-or-later\nmodified version\n")
	writeFile(t, filepath.Join(root, "resources", ThirdPartyNoticesFile),
		"MyGo libghostty-vt EternalTerminal Netcatty mosh-go 0284bd652aab3dfff5b0b74945c23901d7537b61\n")
	writeFile(t, filepath.Join(root, "mygo.json"), `{"version":"1.2.3","resources":["LICENSE","NOTICE"]}`)
	if err := CheckRepository(root); err != nil {
		t.Fatalf("the minimal legal tree was rejected: %v", err)
	}
	writeFile(t, filepath.Join(root, "mygo.json"), `{"version":"1.2.3","resources":["NOTICE"]}`)
	if err := CheckRepository(root); err == nil {
		t.Fatal("mygo.json without LICENSE passed the repository check")
	}
	writeFile(t, filepath.Join(root, "mygo.json"), `{"version":"1.2.3","resources":["LICENSE","NOTICE"]}`)
	writeFile(t, filepath.Join(root, "NOTICE"), "GPL-3.0-or-later\n")
	if err := CheckRepository(root); err == nil {
		t.Fatal("a NOTICE without the modification notice passed the repository check")
	}
}

func TestCheckPackagedRequiresEveryDocumentAndTheSourceOffer(t *testing.T) {
	root := t.TempDir()
	writePackagedTree(t, root)
	if err := CheckPackaged(root); err != nil {
		t.Fatalf("the complete packaged tree was rejected: %v", err)
	}

	for _, name := range []string{LicenseFile, NoticeFile, ThirdPartyNoticesFile, SourceOfferFile} {
		t.Run(name, func(t *testing.T) {
			broken := t.TempDir()
			writePackagedTree(t, broken)
			if err := os.Remove(filepath.Join(broken, name)); err != nil {
				t.Fatal(err)
			}
			if err := CheckPackaged(broken); err == nil {
				t.Fatalf("a packaged tree without %s passed the check", name)
			}
		})
	}
}

func TestCheckPackagedRejectsASourceOfferForAnotherVersion(t *testing.T) {
	root := t.TempDir()
	writePackagedTree(t, root)
	writeFile(t, filepath.Join(root, SourceOfferFile), SourceOfferDocument("9.9.9"))
	if err := CheckPackaged(root); err == nil {
		t.Fatal("a source offer naming another version passed the check")
	}
}

func writePackagedTree(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, LicenseFile), "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n")
	writeFile(t, filepath.Join(root, NoticeFile), "GPL-3.0-or-later\nmodified version\n")
	writeFile(t, filepath.Join(root, ThirdPartyNoticesFile),
		"MyGo libghostty-vt EternalTerminal Netcatty mosh-go 0284bd652aab3dfff5b0b74945c23901d7537b61\n")
	writeFile(t, filepath.Join(root, "mygo.json"), `{"version":"1.2.3"}`)
	writeFile(t, filepath.Join(root, SourceOfferFile), SourceOfferDocument("1.2.3"))
}

func TestThirdPartyNoticesNameEveryGoModule(t *testing.T) {
	root := filepath.Join("..", "..")
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	notices, err := os.ReadFile(filepath.Join(root, "resources", ThirdPartyNoticesFile))
	if err != nil {
		t.Fatal(err)
	}
	modules := requiredModules(string(goMod))
	if len(modules) == 0 {
		t.Fatal("go.mod lists no required modules")
	}
	for _, module := range modules {
		if !strings.Contains(string(notices), module) {
			t.Errorf("%s does not name the required Go module %s", ThirdPartyNoticesFile, module)
		}
	}
}

// TestThirdPartyNoticesAttributeVendoredETGoAndProtobuf ties the app-level
// notices to the vendored tree's own provenance: a future provenance update
// cannot leave the distributed notice naming a different commit, copyright
// holder, or set of local license documents.
func TestThirdPartyNoticesAttributeVendoredETGoAndProtobuf(t *testing.T) {
	root := filepath.Join("..", "..")
	notices, err := os.ReadFile(filepath.Join(root, "resources", ThirdPartyNoticesFile))
	if err != nil {
		t.Fatal(err)
	}
	vendored := filepath.Join(root, "adapters", "et", "internal", "etgo")
	provenance, err := os.ReadFile(filepath.Join(vendored, "PROVENANCE.md"))
	if err != nil {
		t.Fatal(err)
	}

	const (
		etGoCommit     = "bc33dd76b558418bc1079ba27c8c477b8145eea5"
		etGoCopyright  = "Copyright 2026 Tomi P. Hakala"
		protoCommit    = "a8318ca4598756ca3c5f4711cce12a65faeff57d"
		protobufNotice = "Copyright (c) 2018 The Go Authors. All rights reserved."
	)
	for _, want := range []string{etGoCommit, etGoCopyright, protoCommit} {
		if !strings.Contains(string(provenance), want) {
			t.Errorf("adapters/et/internal/etgo/PROVENANCE.md does not contain %q", want)
		}
	}
	for _, want := range []string{
		"tphakala/et-go",
		etGoCommit,
		etGoCopyright,
		protoCommit,
		"Jason Gauci and contributors",
		"adapters/et/internal/etgo",
		"google.golang.org/protobuf",
		protobufNotice,
	} {
		if !strings.Contains(string(notices), want) {
			t.Errorf("%s does not contain %q", ThirdPartyNoticesFile, want)
		}
	}
	for _, name := range []string{"LICENSE", "NOTICE", "PROVENANCE.md"} {
		if _, err := os.Stat(filepath.Join(vendored, name)); err != nil {
			t.Errorf("vendored et-go document %s: %v", name, err)
		}
	}
}

func requiredModules(goMod string) []string {
	var modules []string
	inRequire := false
	for _, line := range strings.Split(goMod, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inRequire = true
		case inRequire && line == ")":
			inRequire = false
		case inRequire && line != "" && !strings.HasPrefix(line, "//"):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				modules = append(modules, fields[0])
			}
		}
	}
	return modules
}

func TestLoadFindsDocumentsAcrossRootsAndReportsMissing(t *testing.T) {
	root := t.TempDir()
	notices := filepath.Join(root, "resources")
	writeFile(t, filepath.Join(root, LicenseFile), "license text")
	writeFile(t, filepath.Join(root, NoticeFile), "notice text")
	writeFile(t, filepath.Join(notices, ThirdPartyNoticesFile), "notices text")

	documents := Load(root, notices)
	if documents.License != "license text" || documents.Notice != "notice text" ||
		documents.ThirdPartyNotices != "notices text" {
		t.Fatalf("loaded documents = %+v", documents)
	}
	if len(documents.Missing) != 0 {
		t.Fatalf("complete roots reported missing documents: %v", documents.Missing)
	}

	empty := Load(t.TempDir())
	if len(empty.Missing) != 3 {
		t.Fatalf("missing documents = %v, want all three", empty.Missing)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
