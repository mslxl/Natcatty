//go:build selftest

package app

// Ticket 60 self-test. Press F11, then 6, then 0.
//
// The check proves the release is ready from the running build and the
// checked-out definition of done, not from a build log:
//
//   - the shipped CHANGELOG.md (a bundled resource in a packaged build) has a
//     non-empty section for the manifest version, and internal/releasenotes
//     generates deterministic release notes from it;
//   - the parity matrix has no implemented or unverified row, every verified
//     row names an acceptance test that exists and that the default CI build
//     runs, every excluded row has a signed limitations entry with Decision
//     and Ticket lines, and every ticket 01-60 appears on a row;
//   - the two contrast observations from ticket 59's acceptance are closed:
//     AccentBodyText clears the body target on every appearance preset, and
//     the shipped placeholder color is TextMuted, whose value clears the
//     target (the reported #6786b4 is an antialiasing fringe).
//
// The parity step audits the source tree, so it needs the repository. A
// packaged self-test build finds it through the working directory or the
// executable's ancestry; NATCATTY_REPO_ROOT overrides both. The release build
// ships without the selftest tag, so this is a developer/release check only.
//
// Every step is synchronous; there is no wait to sleep through. A repeated
// F11,6,0 resets the run counter and re-reads every file, so the check passes
// twice in one process.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-60.json.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	"github.com/mslxl/natcatty/internal/parity"
	"github.com/mslxl/natcatty/internal/release"
	"github.com/mslxl/natcatty/internal/releasenotes"
	"github.com/mslxl/natcatty/ui/selftest"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

const selftest60Ticket = 60

type selftest60 struct {
	// run counts completed entries. resetRunState starts a fresh run; every
	// step re-reads the changelog, the matrix, and the themes.
	run int
}

func (t *selftest60) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest60Ticket,
		Name:   "Release: verified parity, signed limitations, generated notes",
		Steps: []selftest.Step{
			{Name: "reset", Run: t.resetStep},
			{Name: "release-notes-from-changelog", Run: t.releaseNotesStep},
			{Name: "parity-release-gate", Run: t.parityStep},
			{Name: "contrast-observations", Run: t.contrastStep},
		},
	}
}

func (t *selftest60) resetStep(*ui.Context) selftest.Outcome {
	t.run++
	return selftest.Passed(fmt.Sprintf("run %d starts a fresh release inspection", t.run), "")
}

// releaseNotesStep turns the shipped changelog into release notes for the
// manifest version. It is the runtime proof that notes are generated, not
// hand-written.
func (t *selftest60) releaseNotesStep(*ui.Context) selftest.Outcome {
	manifestPath, manifest, err := selftest60Manifest()
	if err != nil {
		return selftest.Failed(err)
	}
	changelogPath, err := selftest60Changelog()
	if err != nil {
		return selftest.Failed(err)
	}
	if mygo.App.IsPackaged() {
		root, err := selftest60ResourceRoot()
		if err != nil {
			return selftest.Failed(err)
		}
		if !selftest60Inside(root, changelogPath) {
			return selftest.Failed(fmt.Errorf(
				"the packaged changelog is %s, not beside the executable in %s", changelogPath, root))
		}
	}
	data, err := os.ReadFile(changelogPath)
	if err != nil {
		return selftest.Failed(fmt.Errorf("read %s: %w", changelogPath, err))
	}
	notes, err := releasenotes.Generate(data, manifest.Version)
	if err != nil {
		return selftest.Failed(err)
	}
	title := "# Natcatty " + manifest.Version + "\n"
	if !bytes.HasPrefix(notes, []byte(title)) {
		return selftest.Failed(fmt.Errorf("release notes do not start with %q:\n%s", title, notes))
	}
	if !bytes.Contains(notes, []byte("### ")) {
		return selftest.Failed(fmt.Errorf("release notes for %s have no change headings:\n%s", manifest.Version, notes))
	}
	if bytes.Contains(notes, []byte("## [")) {
		return selftest.Failed(fmt.Errorf("release notes leaked another changelog section:\n%s", notes))
	}
	again, err := releasenotes.Generate(data, manifest.Version)
	if err != nil || !bytes.Equal(notes, again) {
		return selftest.Failed(fmt.Errorf("release notes for %s are not deterministic (%v)", manifest.Version, err))
	}
	return selftest.Passed(fmt.Sprintf(
		"generated deterministic %d-byte release notes for %s from %s (manifest %s)",
		len(notes), manifest.Version, changelogPath, manifestPath),
		"internal/releasenotes.Generate + CHANGELOG.md")
}

// parityStep applies the same release gate the release workflow runs, from the
// checked-out matrix, limitations file, and test tree.
func (t *selftest60) parityStep(*ui.Context) selftest.Outcome {
	matrixPath, err := selftest60FindParityFile(parity.MatrixPath)
	if err != nil {
		return selftest.Failed(err)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(matrixPath)))
	matrixData, err := os.ReadFile(matrixPath)
	if err != nil {
		return selftest.Failed(fmt.Errorf("read %s: %w", matrixPath, err))
	}
	matrix, err := parity.ParseMatrix(matrixData)
	if err != nil {
		return selftest.Failed(err)
	}
	limitationsPath := filepath.Join(root, filepath.FromSlash(parity.LimitationsPath))
	limitationData, err := os.ReadFile(limitationsPath)
	if err != nil {
		return selftest.Failed(fmt.Errorf("read %s: %w", limitationsPath, err))
	}
	limitations := parity.ParseLimitations(limitationData)
	if problems := parity.Audit(matrix, limitations); len(problems) > 0 {
		return selftest.Failed(fmt.Errorf("parity release gate: %s", strings.Join(problems, "; ")))
	}

	// The release workflow also requires that every verified row name a test
	// the default `go test ./...` jobs actually run. A selftest-tagged test is
	// real evidence but CI does not execute it, so it cannot stand alone.
	tests, err := parity.BuildTestIndex(root)
	if err != nil {
		return selftest.Failed(err)
	}
	for index := range matrix.Rows {
		row := &matrix.Rows[index]
		if row.Status == "verified" && !parity.HasCIRunnableTest(row, tests) {
			return selftest.Failed(fmt.Errorf(
				"row %q is verified but names no acceptance test the default CI build runs", row.ID))
		}
	}

	covered := map[string]bool{}
	counts := map[string]int{}
	for _, row := range matrix.Rows {
		counts[row.Status]++
		for _, ticket := range row.Tickets {
			covered[ticket] = true
		}
	}
	var missing []string
	for number := 1; number <= 60; number++ {
		ticket := fmt.Sprintf("%02d", number)
		if !covered[ticket] {
			missing = append(missing, ticket)
		}
	}
	if len(missing) > 0 {
		return selftest.Failed(fmt.Errorf("tickets %s appear on no parity row", strings.Join(missing, ", ")))
	}
	return selftest.Passed(fmt.Sprintf(
		"%d rows: %d verified and %d excluded, with no implemented or unverified row; every verified row names a CI-runnable test, every exclusion is signed, and tickets 01-60 are covered",
		len(matrix.Rows), counts["verified"], counts["excluded"]),
		"internal/parity.Audit + HasCIRunnableTest + docs/parity/matrix.yaml + docs/parity/limitations.md")
}

// contrastStep closes ticket 59's two acceptance observations. The accent
// token itself is a fill color; AccentBodyText is the text variant the
// self-test overlay paints. The Vault search placeholder is painted by MyGo
// with TextMuted, so that token is what must clear the target.
func (t *selftest60) contrastStep(*ui.Context) selftest.Outcome {
	snow, ok := domaintheme.LookupPreset(false, "snow")
	if !ok {
		return selftest.Failed(fmt.Errorf("the Snow preset is missing"))
	}
	light := uitheme.UI(false, snow, uitheme.Options{})
	if got, want := light.TextMuted, ui.Hex("#676f7e"); got != want {
		return selftest.Failed(fmt.Errorf("the Snow placeholder color is %+v, want %+v", got, want))
	}
	for _, fill := range []struct {
		name  string
		color ui.Color
	}{
		{"surface", light.Surface},
		{"background", light.Background},
	} {
		if ratio := selftest60Contrast(light.TextMuted, fill.color); ratio < uitheme.MinimumTextContrast {
			return selftest.Failed(fmt.Errorf("the Snow placeholder on the %s = %.2f:1, want at least %.1f:1",
				fill.name, ratio, uitheme.MinimumTextContrast))
		}
	}
	if ratio := selftest60Contrast(light.Accent, light.Surface); ratio >= uitheme.MinimumTextContrast {
		return selftest.Failed(fmt.Errorf(
			"the raw Snow accent now clears the body target by itself (%.2f:1); the observation and helper premise changed", ratio))
	}

	presets := 0
	for _, appearance := range []struct {
		name    string
		dark    bool
		presets []domaintheme.Preset
	}{
		{"light", false, domaintheme.LightPresets()},
		{"dark", true, domaintheme.DarkPresets()},
	} {
		for _, preset := range appearance.presets {
			presets++
			theme := uitheme.UI(appearance.dark, preset, uitheme.Options{})
			text := uitheme.AccentBodyText(theme)
			for _, fill := range []struct {
				name  string
				color ui.Color
			}{
				{"background", theme.Background},
				{"surface", theme.Surface},
			} {
				if ratio := selftest60Contrast(text, fill.color); ratio < uitheme.MinimumTextContrast {
					return selftest.Failed(fmt.Errorf(
						"%s preset %s: accent body text on the %s = %.2f:1, want at least %.1f:1",
						appearance.name, preset.ID, fill.name, ratio, uitheme.MinimumTextContrast))
				}
			}
		}
	}
	return selftest.Passed(fmt.Sprintf(
		"AccentBodyText clears %.1f:1 on all %d appearance presets, and the Snow placeholder is TextMuted #676f7e (%.2f:1 on the input surface); the reported #6786b4 is an antialiasing fringe",
		uitheme.MinimumTextContrast, presets, selftest60Contrast(light.TextMuted, light.Surface)),
		"ui/theme.AccentBodyText + ui/theme.MinimumTextContrast")
}

func selftest60Contrast(a, b ui.Color) float64 {
	la, okA := domaintheme.HexLuminance(fmt.Sprintf("#%02x%02x%02x", a.R, a.G, a.B))
	lb, okB := domaintheme.HexLuminance(fmt.Sprintf("#%02x%02x%02x", b.R, b.G, b.B))
	if !okA || !okB {
		return 0
	}
	return domaintheme.ContrastRatio(la, lb)
}

// selftest60StartDirs lists the places a packaged or development run may have
// been started from. The working directory is what a release self-test run
// sets to the repository; the executable's directory covers a build output
// tree.
func selftest60StartDirs() []string {
	var dirs []string
	if override := strings.TrimSpace(os.Getenv("NATCATTY_REPO_ROOT")); override != "" {
		dirs = append(dirs, override)
	}
	if workingDir, err := os.Getwd(); err == nil {
		dirs = append(dirs, workingDir)
	}
	if executable, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(executable))
	}
	return dirs
}

// selftest60FindUp walks from start to the filesystem root looking for a
// regular file.
func selftest60FindUp(start, relative string) (string, bool) {
	for dir := start; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, filepath.FromSlash(relative))
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			if absolute, err := filepath.Abs(candidate); err == nil {
				return absolute, true
			}
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
	}
}

// selftest60ResourceRoot is the directory MyGo populated with the manifest
// and bundled resources.
func selftest60ResourceRoot() (string, error) {
	root, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return "", fmt.Errorf("MyGo resource directory: %w", err)
	}
	info, err := os.Stat(filepath.Join(root, "mygo.json"))
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("MyGo resource directory %s has no mygo.json", root)
	}
	if absolute, err := filepath.Abs(root); err == nil {
		return absolute, nil
	}
	return root, nil
}

func selftest60Changelog() (string, error) {
	var candidates []string
	if override := strings.TrimSpace(os.Getenv("NATCATTY_CHANGELOG")); override != "" {
		candidates = append(candidates, override)
	}
	if root, err := mygo.App.Path(mygo.PathResources); err == nil && root != "" {
		candidates = append(candidates, filepath.Join(root, "CHANGELOG.md"))
	}
	for _, start := range selftest60StartDirs() {
		if found, ok := selftest60FindUp(start, "CHANGELOG.md"); ok {
			candidates = append(candidates, found)
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		if absolute, err := filepath.Abs(candidate); err == nil {
			return absolute, nil
		}
		return candidate, nil
	}
	return "", fmt.Errorf("no CHANGELOG.md found in %v", candidates)
}

// selftest60Manifest finds the release manifest the running build was made
// from: the one shipped beside a packaged executable, or the repository copy.
func selftest60Manifest() (string, release.Manifest, error) {
	var candidates []string
	if override := strings.TrimSpace(os.Getenv("NATCATTY_MANIFEST")); override != "" {
		candidates = append(candidates, override)
	}
	if root, err := mygo.App.Path(mygo.PathResources); err == nil && root != "" {
		candidates = append(candidates, filepath.Join(root, "mygo.json"))
	}
	for _, start := range selftest60StartDirs() {
		if found, ok := selftest60FindUp(start, "mygo.json"); ok {
			candidates = append(candidates, found)
		}
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		manifest, err := release.ParseManifest(data)
		if err != nil {
			return "", release.Manifest{}, fmt.Errorf("%s: %w", candidate, err)
		}
		if absolute, err := filepath.Abs(candidate); err == nil {
			candidate = absolute
		}
		return candidate, manifest, nil
	}
	return "", release.Manifest{}, fmt.Errorf("no mygo.json found in %v", candidates)
}

// selftest60FindParityFile locates a source-tree file (the matrix or the
// limitations file) from the working directory, the executable, or the
// explicit repository override.
func selftest60FindParityFile(relative string) (string, error) {
	starts := selftest60StartDirs()
	for _, start := range starts {
		if found, ok := selftest60FindUp(start, relative); ok {
			return found, nil
		}
	}
	return "", fmt.Errorf(
		"%s is not reachable from %v; run the self-test build from the repository tree or set NATCATTY_REPO_ROOT",
		relative, starts)
}

// selftest60Inside reports whether path is inside dir.
func selftest60Inside(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
