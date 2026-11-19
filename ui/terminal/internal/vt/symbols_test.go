package vt

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSymbolsFile pins that symbols.txt is the single list the address table
// and the bindings are generated from.
func TestSymbolsFile(t *testing.T) {
	data, err := os.ReadFile("symbols.txt")
	if err != nil {
		t.Fatal(err)
	}
	var file []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			file = append(file, line)
		}
	}
	if len(file) != len(symbols) {
		t.Fatalf("symbols.txt has %d names, the bindings look up %d", len(file), len(symbols))
	}
	for i, s := range symbols {
		if file[i] != s.name {
			t.Fatalf("symbols.txt[%d] = %q, binding is %q", i, file[i], s.name)
		}
	}
}

// TestAddrTable pins that addr.c resolves exactly the symbols the bindings
// look up: a missing entry is an address of 0, which Load reports as a
// library that is not the one this package binds.
func TestAddrTable(t *testing.T) {
	data, err := os.ReadFile("../../mobile/addr.c")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, m := range regexp.MustCompile(`natcatty_same\(name, "(ghostty_[a-z0-9_]+)"\)`).FindAllStringSubmatch(string(data), -1) {
		seen[m[1]]++
	}
	for _, s := range symbols {
		if seen[s.name] != 1 {
			t.Errorf("addr.c resolves %s %d times, want once", s.name, seen[s.name])
		}
	}
	if len(seen) != len(symbols) {
		t.Errorf("addr.c resolves %d symbols, the bindings look up %d", len(seen), len(symbols))
	}
}
