package icons

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestEveryBundledIconParses(t *testing.T) {
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := ui.ParseSVG(data); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDistroLookup(t *testing.T) {
	if Distro("Ubuntu") == nil {
		t.Error("ubuntu logo missing")
	}
	for _, id := range []string{"", "../icons", "nope"} {
		if Distro(id) != nil {
			t.Errorf("Distro(%q) should be nil", id)
		}
	}
	if _, ok := DistroColor("debian"); !ok {
		t.Error("debian color missing")
	}
	if !strings.HasPrefix(Server, "server") {
		t.Error("constant drift")
	}
}

// Every name constant must name a bundled Lucide file, as Lucide panics on
// an unknown one.
func TestNameConstantsAreBundled(t *testing.T) {
	src, err := os.ReadFile("icons.go")
	if err != nil {
		t.Fatal(err)
	}
	block := string(src)[strings.Index(string(src), "const ("):]
	for _, m := range regexp.MustCompile(`(?m)^\t\w+\s+= "([a-z0-9-]+)"$`).FindAllStringSubmatch(block, -1) {
		if _, err := files.ReadFile("lucide/" + m[1] + ".svg"); err != nil {
			t.Errorf("constant names missing icon %q", m[1])
		}
	}
}
