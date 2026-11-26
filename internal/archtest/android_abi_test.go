package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
)

// androidABI is one row of scripts/mobile/android-abis.txt: the ABI an app
// ships, the Go target, the NDK triple, the pinned artifact, and the ELF
// identity the archive must have.
type androidABI struct {
	abi, goarch, triple, artifact, class, machine string
}

// readAndroidABIs reads the one list the build gate, the staging script and
// this test share.
func readAndroidABIs(t *testing.T) []androidABI {
	t.Helper()
	root := testutil.ModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "scripts/mobile/android-abis.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []androidABI
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 6 {
			t.Fatalf("android-abis.txt: %q has %d fields, want abi goarch triple artifact class machine", line, len(fields))
		}
		rows = append(rows, androidABI{
			abi: fields[0], goarch: fields[1], triple: fields[2],
			artifact: fields[3], class: fields[4], machine: fields[5],
		})
	}
	return rows
}

// TestAndroidABIList pins the ABIs the app ships and their build identity.
// armeabi-v7a is a deliberate product decision this ticket keeps: dropping
// it from the list, or breaking its Go target or pinned artifact, is then a
// visible edit to this test instead of a silent gap in a release.
func TestAndroidABIList(t *testing.T) {
	rows := readAndroidABIs(t)
	want := map[string]androidABI{
		"arm64-v8a": {
			abi: "arm64-v8a", goarch: "arm64", triple: "aarch64-linux-android",
			artifact: "android-arm64-v8a", class: "ELF64", machine: "AArch64",
		},
		"armeabi-v7a": {
			abi: "armeabi-v7a", goarch: "arm", triple: "armv7a-linux-androideabi",
			artifact: "android-armeabi-v7a", class: "ELF32", machine: "ARM",
		},
		"x86_64": {
			abi: "x86_64", goarch: "amd64", triple: "x86_64-linux-android",
			artifact: "android-x86_64", class: "ELF64", machine: "X86-64",
		},
	}
	if len(rows) != len(want) {
		t.Fatalf("android-abis.txt lists %d ABIs, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		w, ok := want[row.abi]
		if !ok {
			t.Errorf("android-abis.txt ships unexpected ABI %q", row.abi)
			continue
		}
		if row != w {
			t.Errorf("android-abis.txt %s = %+v, want %+v", row.abi, row, w)
		}
	}
}

// TestAndroidArchivesMatchTheManifest keeps the ABI list and the committed
// artifact manifest from drifting: every row names an artifact the phone
// stages, and the cgo link in mobile.go names the same directory for the
// same Go target.
func TestAndroidArchivesMatchTheManifest(t *testing.T) {
	root := testutil.ModuleRoot(t)
	rows := readAndroidABIs(t)

	data, err := os.ReadFile(filepath.Join(root, "ui/terminal/artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Artifacts map[string]json.RawMessage `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("ui/terminal/artifacts.json: %v", err)
	}

	mobile, err := os.ReadFile(filepath.Join(root, "ui/terminal/mobile/mobile.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, ok := manifest.Artifacts[row.artifact]; !ok {
			t.Errorf("artifacts.json has no %s for %s", row.artifact, row.abi)
		}
		want := "#cgo android," + row.goarch + " LDFLAGS: ${SRCDIR}/lib/android/" + row.abi + "/libghostty-vt.a"
		if !strings.Contains(string(mobile), want) {
			t.Errorf("mobile.go does not link %s through %q", row.abi, want)
		}
	}
}

// TestAndroidGatesUseTheSharedList pins that the compile gate and the
// staging script consume android-abis.txt rather than a second, drifting
// copy, and that both enforce 16 KB page-compatible links.
func TestAndroidGatesUseTheSharedList(t *testing.T) {
	root := testutil.ModuleRoot(t)
	for _, name := range []string{"scripts/mobile/check-compile.sh", "scripts/mobile/android-libs.sh"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(string(data), "android-abis.txt") {
			t.Errorf("%s does not read the shared android-abis.txt", name)
		}
	}
	libs, err := os.ReadFile(filepath.Join(root, "scripts/mobile/android-libs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(libs), "max-page-size=16384") {
		t.Error("android-libs.sh does not link the 16 KB page probe")
	}
}

// TestManifestLeavesAndroidSigningToTheReleaseJob keeps the committed
// manifest free of signing material and of a frozen version code. The release
// job writes both into its disposable checkout from secrets and the computed
// version; a committed value here would either ship a keystore path or stop
// the version code from growing with each release.
func TestManifestLeavesAndroidSigningToTheReleaseJob(t *testing.T) {
	root := testutil.ModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "mygo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Android struct {
			Keystore    string `json:"keystore"`
			KeyAlias    string `json:"keyAlias"`
			VersionCode int    `json:"versionCode"`
		} `json:"android"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("mygo.json: %v", err)
	}
	if manifest.Android.Keystore != "" || manifest.Android.KeyAlias != "" {
		t.Errorf("mygo.json commits Android signing configuration: %+v", manifest.Android)
	}
	if manifest.Android.VersionCode != 0 {
		t.Errorf("mygo.json commits android.versionCode %d; the release job computes it per release", manifest.Android.VersionCode)
	}
}

// TestAndroidReleaseWorkflowCarriesTheSigningContract pins the release steps
// the ticket requires: keystore material from secrets, the computed version
// code, and the release note saying the APK is installed from a file rather
// than a store.
func TestAndroidReleaseWorkflowCarriesTheSigningContract(t *testing.T) {
	root := testutil.ModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	for _, want := range []string{
		"ANDROID_KEYSTORE_BASE64",
		"ANDROID_KEYSTORE_PASSWORD",
		"ANDROID_KEY_ALIAS",
		"manifest version-code",
		"-c -P 16 4",
		"internal/tools/androidversion",
		"natcatty-android",
		"-append docs/releases/android-apk.md",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release.yml does not contain %q", want)
		}
	}

	note, err := os.ReadFile(filepath.Join(root, "docs/releases/android-apk.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"installed from the file", "not from a store", "adb install"} {
		if !strings.Contains(string(note), want) {
			t.Errorf("docs/releases/android-apk.md does not say %q", want)
		}
	}
}
