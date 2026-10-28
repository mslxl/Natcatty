//go:build selftest

package app

// Ticket 59 self-test. Press F11, then 5, then 9.
//
// The check inspects the running application and its packaged layout, not the
// build log:
//
//   - the app reports the version mygo.json declares (a packaged build must
//     report it exactly; a development build must report no conflicting
//     version);
//   - all five locale catalogs are embedded and each resolves in its own
//     language rather than falling back to en;
//   - the bundled resources shipped by MyGo (et, mosh, their license texts,
//     and THIRD-PARTY-NOTICES.md) are present next to the executable in a
//     packaged layout;
//   - no update metadata or signing certificate is visible next to the
//     executable or the resources, MyGo reports updates disabled, and a
//     packaged macOS build is ad hoc signed (codesign reports Signature=adhoc
//     and no Authority), never Developer ID signed.
//
// Every wait observes a real condition with a hard deadline; nothing sleeps.
// A repeated F11,5,9 resets the run state and re-inspects, so the check passes
// twice in one process.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-59.json.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/internal/release"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest59Ticket = 59
	selftest59Wait   = 10 * time.Second
)

var (
	selftest59Catalogs = []string{"en", "es", "ru", "zh-CN", "zh-TW"}

	selftest59ResourceEntries = []string{
		"et/BUILD-PROVENANCE.json",
		"et/LICENSE-EternalTerminal-Apache-2.0.txt",
		"et/README.md",
		"mosh/BUILD-PROVENANCE.json",
		"mosh/LICENSE-MoshCatty-GPL-3.0.txt",
		"mosh/README.md",
		"THIRD-PARTY-NOTICES.md",
	}
)

type selftest59 struct {
	// run counts completed resets. resetRunState starts a fresh run; every
	// step derives its state from the manifest and the files on disk again.
	run int

	// readyDeadline is cleared by resetStep so every run gets its own wait
	// window instead of inheriting a deadline another run already consumed.
	readyDeadline time.Time

	manifestPath string
	manifest     release.Manifest
	resourceRoot string
}

func (t *selftest59) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest59Ticket,
		Name:   "Packaging: version, embedded locales, bundled resources",
		Steps: []selftest.Step{
			{Name: "reset", Run: t.resetStep},
			t.readyStep(),
			t.versionStep(),
			t.localesStep(),
			t.resourcesStep(),
			t.metadataStep(),
		},
	}
}

func (t *selftest59) resetStep(*ui.Context) selftest.Outcome {
	t.run++
	t.readyDeadline = time.Time{}
	t.manifestPath = ""
	t.manifest = release.Manifest{}
	t.resourceRoot = ""
	return selftest.Passed(fmt.Sprintf("run %d starts a fresh inspection", t.run), "")
}

// readyStep waits for the packaged lifecycle with a hard deadline. A test
// harness has no running Application, so only a packaged build can be gated.
func (t *selftest59) readyStep() selftest.Step {
	return selftest.Step{
		Name: "app-ready",
		Run: func(*ui.Context) selftest.Outcome {
			if !mygo.App.IsPackaged() || mygo.App.IsReady() {
				return selftest.Passed("the application is ready", "")
			}
			if t.readyDeadline.IsZero() {
				t.readyDeadline = time.Now().Add(selftest59Wait)
			}
			if time.Now().After(t.readyDeadline) {
				return selftest.Failed(fmt.Errorf("the packaged app was not ready within %s", selftest59Wait))
			}
			return selftest.Pending
		},
	}
}

func (t *selftest59) versionStep() selftest.Step {
	return selftest.Step{
		Name: "version-matches-manifest",
		Run: func(*ui.Context) selftest.Outcome {
			manifestPath, manifest, err := selftest59LoadManifest()
			if err != nil {
				return selftest.Failed(err)
			}
			t.manifestPath = manifestPath
			t.manifest = manifest
			running := mygo.App.Version()
			packaged := mygo.App.IsPackaged()
			if packaged {
				if running == "" {
					return selftest.Failed(fmt.Errorf(
						"the packaged app reports no version, but %s declares %s", manifestPath, manifest.Version))
				}
				if running != manifest.Version {
					return selftest.Failed(fmt.Errorf(
						"the packaged app reports %s, but %s declares %s", running, manifestPath, manifest.Version))
				}
				if got, want := mygo.App.Name(), manifest.Name; want != "" && got != want {
					return selftest.Failed(fmt.Errorf("the packaged app is named %q, but %s says %q", got, manifestPath, want))
				}
				return selftest.Passed(fmt.Sprintf(
					"the packaged app reports version %s and name %q, matching %s",
					running, mygo.App.Name(), manifestPath), "")
			}
			if running != "" && running != manifest.Version {
				return selftest.Failed(fmt.Errorf(
					"the running build reports %s, but %s declares %s", running, manifestPath, manifest.Version))
			}
			if running == "" {
				return selftest.Passed(fmt.Sprintf(
					"the development build reports no linked version; %s declares %s, the only version source, and a packaged build must report it",
					manifestPath, manifest.Version), "")
			}
			return selftest.Passed(fmt.Sprintf(
				"the running build reports %s, matching %s", running, manifestPath), "")
		},
	}
}

func (t *selftest59) localesStep() selftest.Step {
	return selftest.Step{
		Name: "embedded-locales",
		Run: func(*ui.Context) selftest.Outcome {
			tags := locales.Tags()
			if !slices.Equal(tags, selftest59Catalogs) {
				return selftest.Failed(fmt.Errorf("embedded catalogs = %v, want %v", tags, selftest59Catalogs))
			}
			for _, tag := range selftest59Catalogs {
				message, language, ok := locales.Catalog.Message(tag, "workTabsLabel")
				if !ok || message == nil || strings.TrimSpace(message.String()) == "" {
					return selftest.Failed(fmt.Errorf("the %s catalog has no workTabsLabel message", tag))
				}
				if language != tag {
					return selftest.Failed(fmt.Errorf(
						"the %s catalog falls back to %s for workTabsLabel", tag, language))
				}
			}
			return selftest.Passed(
				"all five catalogs are embedded and each resolves workTabsLabel in its own language",
				"ui/locales.Catalog")
		},
	}
}

func (t *selftest59) resourcesStep() selftest.Step {
	return selftest.Step{
		Name: "bundled-resources",
		Run: func(*ui.Context) selftest.Outcome {
			root, err := selftest59ResourceRoot()
			if err != nil {
				return selftest.Failed(err)
			}
			for _, entry := range selftest59ResourceEntries {
				info, err := os.Stat(filepath.Join(root, filepath.FromSlash(entry)))
				if err != nil {
					return selftest.Failed(fmt.Errorf("bundled resource %s: %w", entry, err))
				}
				if !info.Mode().IsRegular() || info.Size() == 0 {
					return selftest.Failed(fmt.Errorf("bundled resource %s is not a non-empty file", entry))
				}
			}
			detail := fmt.Sprintf("%d bundled resources are present in %s", len(selftest59ResourceEntries), root)
			if mygo.App.IsPackaged() {
				expected, err := selftest59ExecutableResources()
				if err != nil {
					return selftest.Failed(err)
				}
				if !selftest59SamePath(root, expected) {
					return selftest.Failed(fmt.Errorf(
						"packaged resources are in %s, not next to the executable at %s", root, expected))
				}
				detail = fmt.Sprintf(
					"%d bundled resources are present next to the executable in %s", len(selftest59ResourceEntries), root)
			}
			t.resourceRoot = root
			return selftest.Passed(detail, "")
		},
	}
}

func (t *selftest59) metadataStep() selftest.Step {
	return selftest.Step{
		Name: "no-update-signing-metadata",
		Run: func(*ui.Context) selftest.Outcome {
			if t.manifestPath == "" {
				path, manifest, err := selftest59LoadManifest()
				if err != nil {
					return selftest.Failed(err)
				}
				t.manifestPath, t.manifest = path, manifest
			}
			if err := t.manifest.CheckPackaging(); err != nil {
				return selftest.Failed(err)
			}
			if mygo.Updater.Enabled() {
				return selftest.Failed(errors.New("MyGo reports self-updates enabled"))
			}
			root := t.resourceRoot
			if root == "" {
				var err error
				if root, err = selftest59ResourceRoot(); err != nil {
					return selftest.Failed(err)
				}
			}
			found, err := selftest59ForbiddenMetadata(root)
			if err != nil {
				return selftest.Failed(err)
			}
			executable, err := os.Executable()
			if err != nil {
				return selftest.Failed(err)
			}
			executableDir := filepath.Dir(executable)
			if !selftest59SamePath(executableDir, root) {
				more, err := selftest59ForbiddenMetadata(executableDir)
				if err != nil {
					return selftest.Failed(err)
				}
				found = append(found, more...)
			}
			if len(found) > 0 {
				return selftest.Failed(fmt.Errorf("update or signing metadata is visible: %s", strings.Join(found, ", ")))
			}
			if runtime.GOOS == "darwin" && mygo.App.IsPackaged() {
				if err := selftest59AdHocOnly(executable); err != nil {
					return selftest.Failed(err)
				}
			}
			return selftest.Passed(
				"mygo.json and the packaged files carry no update feed, delta archive, certificate, or distribution signature; updates are disabled",
				"mygo.json + mygo.Updater + codesign inspection")
		},
	}
}

// selftest59LoadManifest finds the release manifest. A packaged app reads the
// manifest MyGo ships with it, so the check compares against the artifact's
// own copy; a development build walks up to the repository.
func selftest59LoadManifest() (string, release.Manifest, error) {
	var candidates []string
	if override := strings.TrimSpace(os.Getenv("NATCATTY_MANIFEST")); override != "" {
		candidates = append(candidates, override)
	}
	if mygo.App.IsPackaged() {
		root, err := selftest59ResourceRoot()
		if err != nil {
			return "", release.Manifest{}, err
		}
		candidates = append(candidates, filepath.Join(root, "mygo.json"))
	} else {
		if root, err := selftest59ResourceRoot(); err == nil {
			candidates = append(candidates, filepath.Join(root, "mygo.json"))
		}
		workingDir, _ := os.Getwd()
		executable, _ := os.Executable()
		for _, start := range []string{workingDir, filepath.Dir(executable)} {
			for dir := start; ; dir = filepath.Dir(dir) {
				candidates = append(candidates, filepath.Join(dir, "mygo.json"))
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
			}
		}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		manifest, err := release.ParseManifest(data)
		if err != nil {
			return "", release.Manifest{}, fmt.Errorf("%s: %w", candidate, err)
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			absolute = candidate
		}
		return absolute, manifest, nil
	}
	return "", release.Manifest{}, fmt.Errorf("no mygo.json found in %v", candidates)
}

// selftest59ResourceRoot finds the directory MyGo populated with resources.
// A packaged build must use its own resource directory; a development build
// may fall back to the repository's resources directory.
func selftest59ResourceRoot() (string, error) {
	var candidates []string
	if override := strings.TrimSpace(os.Getenv("NATCATTY_RESOURCES")); override != "" {
		candidates = append(candidates, override)
	}
	if root, err := mygo.App.Path(mygo.PathResources); err == nil {
		candidates = append(candidates, root)
	}
	if !mygo.App.IsPackaged() {
		workingDir, _ := os.Getwd()
		executable, _ := os.Executable()
		for _, start := range []string{workingDir, filepath.Dir(executable)} {
			for dir := start; ; dir = filepath.Dir(dir) {
				candidates = append(candidates, filepath.Join(dir, "resources"))
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
			}
		}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		complete := true
		for _, entry := range selftest59ResourceEntries {
			info, err := os.Stat(filepath.Join(candidate, filepath.FromSlash(entry)))
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				complete = false
				break
			}
		}
		if complete {
			absolute, err := filepath.Abs(candidate)
			if err != nil {
				absolute = candidate
			}
			return absolute, nil
		}
	}
	return "", fmt.Errorf("none of %v holds the bundled resources %v", candidates, selftest59ResourceEntries)
}

// selftest59ExecutableResources is the resource directory MyGo documents for
// a packaged executable: the executable's directory, or Contents/Resources
// inside a macOS bundle.
func selftest59ExecutableResources() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(executable)
	if runtime.GOOS == "darwin" && filepath.Base(dir) == "MacOS" &&
		filepath.Base(filepath.Dir(dir)) == "Contents" {
		return filepath.Join(filepath.Dir(dir), "Resources"), nil
	}
	return dir, nil
}

func selftest59SamePath(a, b string) bool {
	resolve := func(value string) string {
		if absolute, err := filepath.Abs(value); err == nil {
			value = absolute
		}
		if real, err := filepath.EvalSymlinks(value); err == nil {
			value = real
		}
		return filepath.Clean(value)
	}
	a, b = resolve(a), resolve(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// selftest59ForbiddenMetadata finds update feeds and signing material by
// name. Developer ID signatures are embedded in the binary instead and are
// checked separately.
func selftest59ForbiddenMetadata(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		base := strings.ToLower(entry.Name())
		forbidden := false
		switch {
		case strings.HasPrefix(base, "update-") && strings.HasSuffix(base, ".json"):
			forbidden = true
		case strings.HasPrefix(base, "latest") &&
			(strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")):
			forbidden = true
		case strings.HasPrefix(base, "appcast") && strings.HasSuffix(base, ".xml"):
			forbidden = true
		case strings.HasSuffix(base, ".delta"),
			strings.HasSuffix(base, ".p12"),
			strings.HasSuffix(base, ".pfx"),
			strings.HasSuffix(base, ".mobileprovision"):
			forbidden = true
		}
		if forbidden {
			relative, relErr := filepath.Rel(root, filePath)
			if relErr != nil {
				relative = filePath
			}
			found = append(found, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// selftest59AdHocOnly proves a macOS build is ad hoc signed, not Developer ID
// signed. It runs only in a packaged app on macOS.
func selftest59AdHocOnly(executable string) error {
	output, err := exec.Command("codesign", "-dv", "--verbose=4", executable).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign -dv: %v: %s", err, strings.TrimSpace(string(output)))
	}
	text := string(output)
	if !strings.Contains(text, "Signature=adhoc") {
		return fmt.Errorf("the macOS signature is not ad hoc: %s", strings.TrimSpace(text))
	}
	if strings.Contains(text, "Authority=") {
		return fmt.Errorf("the macOS build carries a distribution signature: %s", strings.TrimSpace(text))
	}
	return nil
}
