package terminal

import "testing"

// The click fills a command, so every suffix must map to the extractor that
// understands it and no ordinary file may look like an archive.
func TestArchiveExtractCommandMapsSuffixes(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"bundle.tar", "tar -xf 'bundle.tar'"},
		{"./bundle.tar.gz", "tar -xzf './bundle.tar.gz'"},
		{"/tmp/bundle.tgz", "tar -xzf '/tmp/bundle.tgz'"},
		{"bundle.tar.bz2", "tar -xjf 'bundle.tar.bz2'"},
		{"bundle.tbz2", "tar -xjf 'bundle.tbz2'"},
		{"bundle.tar.xz", "tar -xJf 'bundle.tar.xz'"},
		{"bundle.zip", "unzip 'bundle.zip'"},
		{"BUNDLE.ZIP", "unzip 'BUNDLE.ZIP'"},
		{"it's.tar", `tar -xf 'it'\''s.tar'`},
	}
	for _, testCase := range cases {
		got, ok := ArchiveExtractCommand(testCase.path)
		if !ok || got != testCase.want {
			t.Errorf("ArchiveExtractCommand(%q) = %q, %v; want %q, true",
				testCase.path, got, ok, testCase.want)
		}
	}
}

func TestArchiveExtractCommandRefusesUnsafeNames(t *testing.T) {
	paths := []string{
		"",
		".tar",
		".zip",
		"README.md",
		"bundle.tar.gz.exe",
		"-bundle.tar",
		"~/bundle.tar",
		"host:bundle.tar",
		`dir\bundle.zip`,
		"bundle!.tar",
		"bundle[1].zip",
		"bundle.tar\nrm -rf /",
		"bundle tar.gz",
	}
	for _, path := range paths {
		if command, ok := ArchiveExtractCommand(path); ok {
			t.Errorf("ArchiveExtractCommand(%q) = %q, true; want refused", path, command)
		}
	}
}
