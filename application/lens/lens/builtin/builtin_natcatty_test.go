package builtin

import (
	"sort"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func TestRegisteredLensCatalogIncludesTerminalFamilies(t *testing.T) {
	registered := lens.Registered()
	ids := make([]string, 0, len(registered))
	for _, definition := range registered {
		ids = append(ids, definition.ID)
	}
	sort.Strings(ids)
	t.Logf("registered lens ids (%d): %v", len(ids), ids)
	if len(ids) != 75 {
		t.Fatalf("registered lens ids = %d, want 75", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatalf("duplicate lens registration: %s", ids[i])
		}
	}
	for _, id := range []string{
		"files.ls", "git.status", "git.diff", "net.ping", "search.grep",
		"go.build", "go.test", "rust.cargo", "docker.build", "k8s.get",
		"js.pkg", "docs.help", "pkg.outdated", "gh.view",
	} {
		found := false
		for _, definition := range registered {
			if definition.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("lens %q is missing", id)
		}
	}
}

func TestPingCommandTriggersNetPingLens(t *testing.T) {
	for _, program := range []string{"ping", "ping6"} {
		got := lens.Lookup(model.Cmd{Program: program})
		if got == nil || got.ID() != "net.ping" {
			t.Fatalf("Lookup(%q) = %v, want net.ping", program, got)
		}
	}
	cmd := lens.Normalize("ping.exe -n 1 127.0.0.1", "", "")
	if got := lens.Lookup(cmd); got == nil || got.ID() != "net.ping" {
		t.Fatalf("Lookup(%q) = %v, want net.ping", cmd.Program, got)
	}
}

func TestWindowsPingOutputBuildsNativeView(t *testing.T) {
	l := lens.ByID("net.ping")
	if l == nil {
		t.Fatal("net.ping is not registered")
	}
	state, err := l.Open(model.Cmd{Program: "ping", Args: []string{"-n", "1", "example.com"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(
		"Pinging example.com [93.184.216.34] with 32 bytes of data:\n"+
			"Reply from 93.184.216.34: bytes=32 time=12ms TTL=56\n"+
			"\n"+
			"Ping statistics for 93.184.216.34:\n"+
			"    Packets: Sent = 1, Received = 1, Lost = 0 (0% loss),\n"+
			"Approximate round trip times in milli-seconds:\n"+
			"    Minimum = 12ms, Maximum = 12ms, Average = 12ms\n", "\n") {
		if err := l.Line(state, model.Line{Text: line}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := l.View(state)
	if err != nil || node == nil {
		t.Fatalf("Windows ping view = %v, %v; want native view", node, err)
	}
}
