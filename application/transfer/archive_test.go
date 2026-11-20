package transfer

import (
	"errors"
	"strings"
	"testing"
)

// TestBuildArchiveCommandStagesAndQuotesTheSelection is the command-contract
// test: the archive is built in a private sibling directory, every selected
// path uses the compress path's single-quote rule, and the source folder keeps
// its descendants out of the argument list.
func TestBuildArchiveCommandStagesAndQuotesTheSelection(t *testing.T) {
	command, plan, err := BuildArchiveCommand(ArchiveRequest{
		Directory: "/srv/files",
		Name:      "my 'pack'.tar.gz",
		Format:    ArchiveTarGz,
		Paths: []string{
			"/srv/files/photos",
			"/srv/files/photos/a b.txt",
			"/srv/files/it's.txt",
			"/srv/files/it's.txt",
		},
	})
	if err != nil {
		t.Fatalf("BuildArchiveCommand: %v", err)
	}

	if got, want := strings.Join(plan.Paths, ","), "it's.txt,photos"; got != want {
		t.Fatalf("planned sources = %q, want %q", got, want)
	}
	for _, want := range []string{
		"set -e",
		"umask 077",
		`cd '/srv/files'`,
		`name='my '\''pack'\''.tar.gz'`,
		`out="./$name"`,
		`tar -czf "$stage/$name" -- './it'\''s.txt' './photos' >/dev/null`,
		`stage=$(mktemp -d "./.netcatty-archive.XXXXXXXXXX")`,
		`trap 'rm -rf -- "$stage"' EXIT`,
		`ln -- "$stage/$name" .`,
		`printf '%s\n' 'NETCATTY_ARCHIVE_RESULT:OK'`,
	} {
		if !strings.Contains(command, want) {
			t.Errorf("the command is missing %q:\n%s", want, command)
		}
	}
	if strings.Contains(command, "photos/a b.txt") {
		t.Fatalf("a selected folder's descendant is still an argument:\n%s", command)
	}
	if strings.Contains(command, "mv -f") {
		t.Fatalf("a non-overwriting command still moves over the target:\n%s", command)
	}
}

// TestBuildArchiveCommandChoosesTheRemoteTool holds the format choice: zip
// uses the zip probe and flags, tar.gz uses tar.
func TestBuildArchiveCommandChoosesTheRemoteTool(t *testing.T) {
	zip, _, err := BuildArchiveCommand(ArchiveRequest{
		Directory: "/srv/files",
		Name:      "photos.zip",
		Format:    ArchiveZip,
		Paths:     []string{"/srv/files/photos"},
	})
	if err != nil {
		t.Fatalf("BuildArchiveCommand zip: %v", err)
	}
	for _, want := range []string{`command -v zip`, `zip -q -r -y -nw -MM "$stage/$name" -- './photos'`} {
		if !strings.Contains(zip, want) {
			t.Errorf("the zip command is missing %q:\n%s", want, zip)
		}
	}
	if strings.Contains(zip, "tar -czf") {
		t.Fatalf("the zip command also runs tar:\n%s", zip)
	}

	tarGz, _, err := BuildArchiveCommand(ArchiveRequest{
		Directory: "/srv/files",
		Name:      "photos.tar.gz",
		Format:    ArchiveTarGz,
		Paths:     []string{"/srv/files/photos"},
	})
	if err != nil {
		t.Fatalf("BuildArchiveCommand tar.gz: %v", err)
	}
	if !strings.Contains(tarGz, `command -v tar`) || !strings.Contains(tarGz, "tar -czf") {
		t.Fatalf("the tar.gz command does not use tar:\n%s", tarGz)
	}
}

// TestBuildArchiveCommandRefusesCollidingOrUnsafeRequests is the collision
// bullet: without overwrite the command publishes with ln and reports EEXIST,
// never replacing a target; unsafe requests never reach the host.
func TestBuildArchiveCommandRefusesCollidingOrUnsafeRequests(t *testing.T) {
	overwrite, _, err := BuildArchiveCommand(ArchiveRequest{
		Directory: "/srv/files",
		Name:      "photos.tar.gz",
		Format:    ArchiveTarGz,
		Paths:     []string{"/srv/files/photos"},
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("BuildArchiveCommand overwrite: %v", err)
	}
	if !strings.Contains(overwrite, `mv -f -- "$stage/$name" .`) {
		t.Fatalf("an overwriting command does not publish with mv:\n%s", overwrite)
	}
	if strings.Contains(overwrite, ArchiveResultExists) {
		t.Fatalf("an overwriting command still reports a collision:\n%s", overwrite)
	}

	base := ArchiveRequest{
		Directory: "/srv/files",
		Name:      "photos.tar.gz",
		Format:    ArchiveTarGz,
		Paths:     []string{"/srv/files/photos"},
	}
	cases := []struct {
		name   string
		change func(*ArchiveRequest)
		want   error
	}{
		{"relative directory", func(r *ArchiveRequest) { r.Directory = "files" }, ErrArchiveDirectory},
		{"windows directory", func(r *ArchiveRequest) { r.Directory = `C:\files` }, ErrArchiveDirectory},
		{"name with a path", func(r *ArchiveRequest) { r.Name = "nested/photos.tar.gz" }, ErrArchiveName},
		{"wrong extension", func(r *ArchiveRequest) { r.Name = "photos.zip" }, ErrArchiveName},
		{"bare extension", func(r *ArchiveRequest) { r.Name = ".tar.gz" }, ErrArchiveName},
		{"source outside the directory", func(r *ArchiveRequest) { r.Paths = []string{"/etc/passwd"} }, ErrArchiveSources},
		{"the directory itself", func(r *ArchiveRequest) { r.Paths = []string{"/srv/files"} }, ErrArchiveSources},
		{"the output name", func(r *ArchiveRequest) { r.Paths = []string{"/srv/files/photos.tar.gz"} }, ErrArchiveSources},
		{"no sources", func(r *ArchiveRequest) { r.Paths = nil }, ErrArchiveSources},
		{"unknown format", func(r *ArchiveRequest) { r.Format = "rar" }, ErrArchiveFormat},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := base
			testCase.change(&request)
			if _, _, err := BuildArchiveCommand(request); !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// TestParseArchiveResult classifies the confirmation marker. A missing or
// unknown marker is an error even when the transport exited zero.
func TestParseArchiveResult(t *testing.T) {
	cases := []struct {
		stdout string
		want   error
	}{
		{ArchiveResultOK + "\n", nil},
		{"noise\n" + ArchiveResultOK + "\n", nil},
		{ArchiveResultExists + "\n", ErrArchiveExists},
		{"", ErrArchiveUnconfirmed},
		{"OK\n", ErrArchiveUnconfirmed},
	}
	for _, testCase := range cases {
		if err := ParseArchiveResult(testCase.stdout); !errors.Is(err, testCase.want) {
			t.Errorf("ParseArchiveResult(%q) = %v, want %v", testCase.stdout, err, testCase.want)
		}
	}
}

// TestDefaultArchiveName follows the selection: one entry keeps its name, a
// multi-selection uses the shared parent, and an existing archive extension
// gains -archive instead of producing the same name again.
func TestDefaultArchiveName(t *testing.T) {
	cases := []struct {
		sources []string
		format  ArchiveFormat
		want    string
	}{
		{[]string{"/srv/files/photos"}, ArchiveTarGz, "photos.tar.gz"},
		{[]string{"/srv/files/photos"}, ArchiveZip, "photos.zip"},
		{[]string{"/srv/files/photos.tar.gz"}, ArchiveTarGz, "photos-archive.tar.gz"},
		{[]string{"/srv/files/photos.tgz"}, ArchiveZip, "photos-archive.zip"},
		{[]string{"/srv/files/photos", "/srv/files/report.txt"}, ArchiveTarGz, "files.tar.gz"},
		{nil, ArchiveTarGz, "archive.tar.gz"},
	}
	for _, testCase := range cases {
		if got := DefaultArchiveName("/srv/files", testCase.sources, testCase.format); got != testCase.want {
			t.Errorf("DefaultArchiveName(%v, %s) = %q, want %q", testCase.sources, testCase.format, got, testCase.want)
		}
	}

	if got := WithArchiveExtension("photos.tar.gz", ArchiveZip); got != "photos.zip" {
		t.Errorf("tar.gz to zip = %q", got)
	}
	if got := WithArchiveExtension("photos.tgz", ArchiveTarGz); got != "photos.tar.gz" {
		t.Errorf("tgz to tar.gz = %q", got)
	}
	if got := WithArchiveExtension("photos", ArchiveZip); got != "photos.zip" {
		t.Errorf("bare name = %q", got)
	}
}
