// SPDX-License-Identifier: GPL-3.0-or-later

package legal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestSourceOfferNamesTheReleaseTagAndBundledSources(t *testing.T) {
	offer := SourceOfferDocument("v0.1.0")
	for _, want := range []string{
		"# Natcatty 0.1.0 — Corresponding Source",
		"## Corresponding Source",
		SourceTagURL("0.1.0"),
		"https://github.com/mslxl/natcatty/tree/v0.1.0",
		"GPL-3.0-or-later",
		"https://github.com/binaricat/MoshCatty/tree/moshcatty-0.1.8",
		"554b9d305e7ac4b11de740d764bbc3e05f816d7b",
		"protocol reference only",
		"https://github.com/unixshells/mosh-go/tree/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f",
		"Copyright (c) 2026 Unix Shells",
		"LICENSE-MoshGo-MIT.txt",
		"https://github.com/MisterTea/EternalTerminal/tree/et-v6.2.10",
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
		filepath.Join("resources", "mosh", "LICENSE-MoshCatty-GPL-3.0.txt"),
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

func TestSourceOfferTracksTheBundledClientProvenance(t *testing.T) {
	root := filepath.Join("..", "..")
	offer := SourceOffer("0.1.0")
	for _, name := range []string{"mosh", "et"} {
		path := filepath.Join(root, "resources", name, "BUILD-PROVENANCE.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var provenance struct {
			Release struct {
				Repository string `json:"repository"`
				Tag        string `json:"tag"`
			} `json:"release"`
			Source struct {
				Repository string `json:"repository"`
				Tag        string `json:"tag"`
				Commit     string `json:"commit"`
			} `json:"source"`
			Upstream struct {
				URL string `json:"url"`
			} `json:"upstream"`
			ProtocolReference struct {
				URL string `json:"url"`
			} `json:"protocolReference"`
		}
		if err := json.Unmarshal(data, &provenance); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		tag := provenance.Source.Tag
		if tag == "" {
			tag = provenance.Release.Tag
		}
		if tag == "" || !strings.Contains(offer, tag) {
			t.Errorf("source offer does not name the bundled client release %s", tag)
		}
		if provenance.Source.Commit != "" && !strings.Contains(offer, provenance.Source.Commit) {
			t.Errorf("source offer does not name the bundled client source commit %s", provenance.Source.Commit)
		}
		sourceURL := provenance.ProtocolReference.URL
		if sourceURL == "" {
			sourceURL = provenance.Upstream.URL
		}
		if sourceURL != "" && !strings.Contains(offer, sourceURL) {
			t.Errorf("source offer does not name the reference source %s", sourceURL)
		}
	}
}

func TestMoshProvenanceRecordsTheRealSourceAndReproducibility(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "resources", "mosh", "BUILD-PROVENANCE.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var provenance struct {
		Source struct {
			Repository   string `json:"repository"`
			Tag          string `json:"tag"`
			Commit       string `json:"commit"`
			BuildCommand string `json:"buildCommand"`
		} `json:"source"`
		ProtocolReference struct {
			URL  string `json:"url"`
			Role string `json:"role"`
		} `json:"protocolReference"`
		Reproducibility struct {
			Status   string `json:"status"`
			Evidence any    `json:"evidence"`
		} `json:"reproducibility"`
		ThirdPartyNotices []struct {
			Component   string `json:"component"`
			License     string `json:"license"`
			Copyright   string `json:"copyright"`
			LicenseFile string `json:"licenseFile"`
			Commit      string `json:"commit"`
		} `json:"thirdPartyNotices"`
	}
	if err := json.Unmarshal(data, &provenance); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	const commit = "554b9d305e7ac4b11de740d764bbc3e05f816d7b"
	if provenance.Source.Repository != "binaricat/MoshCatty" || provenance.Source.Tag != "moshcatty-0.1.8" {
		t.Errorf("MoshCatty source = %+v, want binaricat/MoshCatty moshcatty-0.1.8", provenance.Source)
	}
	if provenance.Source.Commit != commit {
		t.Errorf("MoshCatty source commit = %q, want %q", provenance.Source.Commit, commit)
	}
	if provenance.Source.BuildCommand != "cargo build --release" {
		t.Errorf("MoshCatty build command = %q, want cargo build --release", provenance.Source.BuildCommand)
	}
	if provenance.ProtocolReference.URL != "https://github.com/mobile-shell/mosh" ||
		!strings.Contains(provenance.ProtocolReference.Role, "not the source") {
		t.Errorf("MoshCatty protocol reference = %+v, want mobile-shell/mosh marked as not the source",
			provenance.ProtocolReference)
	}
	if provenance.Reproducibility.Status != "not-established" || provenance.Reproducibility.Evidence != nil {
		t.Errorf("MoshCatty reproducibility = %+v, want status not-established and no evidence",
			provenance.Reproducibility)
	}
	foundMoshGo := false
	for _, notice := range provenance.ThirdPartyNotices {
		if notice.Component != "mosh-go" {
			continue
		}
		foundMoshGo = true
		if notice.License != "MIT" || notice.Copyright != "Copyright (c) 2026 Unix Shells" {
			t.Errorf("mosh-go notice = %+v, want MIT Copyright (c) 2026 Unix Shells", notice)
		}
		if notice.Commit != "8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f" {
			t.Errorf("mosh-go commit = %q", notice.Commit)
		}
		if info, statErr := os.Stat(filepath.Join(root, "resources", "mosh", notice.LicenseFile)); statErr != nil || info.Size() == 0 {
			t.Errorf("mosh-go license file %q: %v", notice.LicenseFile, statErr)
		}
	}
	if !foundMoshGo {
		t.Fatal("BUILD-PROVENANCE.json has no mosh-go third-party notice")
	}

	readme, err := os.ReadFile(filepath.Join(root, "resources", "mosh", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	normalizedReadme := strings.ReplaceAll(string(readme), "**", "")
	for _, want := range []string{commit, "Reproducible build: not established", "not the source of this"} {
		if !strings.Contains(normalizedReadme, want) {
			t.Errorf("resources/mosh/README.md does not contain %q", want)
		}
	}
	if strings.Contains(string(readme), "builds mosh from upstream") {
		t.Error("resources/mosh/README.md still claims MoshCatty builds upstream mosh")
	}
}

func TestMoshGoMITNoticeIsShipped(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "mosh", "LICENSE-MoshGo-MIT.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"MIT License",
		"Copyright (c) 2026 Unix Shells",
		"Permission is hereby granted",
		"The above copyright notice and this permission notice",
		`THE SOFTWARE IS PROVIDED "AS IS"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("LICENSE-MoshGo-MIT.txt does not contain %q", want)
		}
	}
}

func TestThirdPartyNoticesNameEveryBundledRustCrate(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "resources", "mosh", "RUST-DEPENDENCIES.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Source struct {
			CrateCount      int    `json:"crateCount"`
			CargoLockSHA256 string `json:"cargoLockSha256"`
			Commit          string `json:"commit"`
		} `json:"source"`
		Crates []struct {
			Name               string `json:"name"`
			Version            string `json:"version"`
			License            string `json:"license"`
			Repository         string `json:"repository"`
			Homepage           string `json:"homepage"`
			CopyrightOrAuthors string `json:"copyrightOrAuthors"`
			CopyrightSource    string `json:"copyrightSource"`
		} `json:"crates"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if manifest.Source.CrateCount != 49 || len(manifest.Crates) != manifest.Source.CrateCount {
		t.Fatalf("crate manifest has count %d and %d entries, want 49",
			manifest.Source.CrateCount, len(manifest.Crates))
	}
	if manifest.Source.Commit != "554b9d305e7ac4b11de740d764bbc3e05f816d7b" {
		t.Errorf("crate manifest source commit = %q", manifest.Source.Commit)
	}
	if manifest.Source.CargoLockSHA256 != "423afa7d1c07136ba2f41f6e4d396e8563a5f7c659964d138dbce8352608142b" {
		t.Errorf("crate manifest Cargo.lock SHA-256 = %q", manifest.Source.CargoLockSHA256)
	}
	notices, err := os.ReadFile(filepath.Join(root, "resources", ThirdPartyNoticesFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, crate := range manifest.Crates {
		for _, want := range []string{crate.Name, crate.Version, crate.License, crate.CopyrightOrAuthors} {
			if !strings.Contains(string(notices), want) {
				t.Errorf("%s does not contain crate %s %s field %q",
					ThirdPartyNoticesFile, crate.Name, crate.Version, want)
			}
		}
		if crate.Repository != "" && !strings.Contains(string(notices), crate.Repository) {
			t.Errorf("%s does not contain crate %s repository %s",
				ThirdPartyNoticesFile, crate.Name, crate.Repository)
		}
		if crate.Homepage != "" && crate.Homepage != crate.Repository &&
			!strings.Contains(string(notices), crate.Homepage) {
			t.Errorf("%s does not contain crate %s homepage %s",
				ThirdPartyNoticesFile, crate.Name, crate.Homepage)
		}
		if crate.CopyrightSource == "" {
			t.Errorf("crate %s %s has no copyright source", crate.Name, crate.Version)
		}
	}
	for _, heading := range []string{
		"### 0BSD license",
		"### Zlib license",
		"### LLVM exception to the Apache License 2.0",
		"### Unicode License V3",
	} {
		if !strings.Contains(string(notices), heading) {
			t.Errorf("%s is missing the %q license text", ThirdPartyNoticesFile, heading)
		}
	}
}

func TestThirdPartyNoticesHaveNoUnresolvedTicket15Marker(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, name := range []string{
		filepath.Join("resources", ThirdPartyNoticesFile),
		filepath.Join("resources", "mosh", "README.md"),
		filepath.Join("resources", "mosh", "BUILD-PROVENANCE.json"),
	} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "TODO(ticket 15)") {
			t.Errorf("%s still contains an unresolved TODO(ticket 15) marker", name)
		}
	}
	provenance, err := os.ReadFile(filepath.Join(root, "resources", "mosh", "BUILD-PROVENANCE.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(provenance), "from upstream mosh") {
		t.Error("BUILD-PROVENANCE.json still claims the binary is built from upstream mosh")
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
		"MyGo libghostty-vt MoshCatty EternalTerminal Netcatty mosh-go 554b9d305e7ac4b11de740d764bbc3e05f816d7b\n")
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
		"MyGo libghostty-vt MoshCatty EternalTerminal Netcatty mosh-go 554b9d305e7ac4b11de740d764bbc3e05f816d7b\n")
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
		protoCommit    = "6f53869473a16407b59f1f3382d291ab97b4f64f"
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
