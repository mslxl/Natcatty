package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// parseNMSymbols maps each defined symbol to the number of archive members
// that define it. Mach-O prefixes global C symbols with an underscore; ELF
// does not. Undefined entries (nm type U) are ignored.
func parseNMSymbols(out string) map[string]int {
	symbols := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// nm prints "address type name" for defined symbols and
		// "type name" for undefined ones.
		typ := fields[len(fields)-2]
		name := fields[len(fields)-1]
		if strings.Contains(line, "):") {
			// An archive member header, not a symbol.
			continue
		}
		if typ == "U" || typ == "u" || typ == "?" || typ == "-" || len(typ) != 1 {
			continue
		}
		name = strings.TrimPrefix(name, "_")
		symbols[name]++
	}
	return symbols
}

// symbolProblems returns the expected symbols that are absent and those
// defined more than once. Duplicates break the final static link just as
// missing symbols break the load at run time.
func symbolProblems(got map[string]int, want []string) (missing, duplicates []string) {
	for _, name := range want {
		switch n := got[name]; {
		case n == 0:
			missing = append(missing, name)
		case n > 1:
			duplicates = append(duplicates, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(duplicates)
	return missing, duplicates
}

// parseLoadAlignments reads the Align column of every LOAD segment from
// llvm-readelf -l output.
func parseLoadAlignments(out string) ([]uint64, error) {
	var aligns []uint64
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] != "LOAD" {
			continue
		}
		align, err := strconv.ParseUint(fields[len(fields)-1], 0, 64)
		if err != nil {
			return nil, fmt.Errorf("parse LOAD alignment %q: %w", fields[len(fields)-1], err)
		}
		aligns = append(aligns, align)
	}
	if len(aligns) == 0 {
		return nil, fmt.Errorf("no LOAD segments in readelf output")
	}
	return aligns, nil
}

// checkPageAlignment rejects a shared object whose LOAD segments are not
// mapped in 16 KB pages. Android 15+ devices require 16 KB pages.
func checkPageAlignment(aligns []uint64) error {
	if len(aligns) == 0 {
		return fmt.Errorf("no LOAD segments found; cannot verify %d-byte page alignment", androidPageSize)
	}
	for _, align := range aligns {
		if align < androidPageSize || align&(align-1) != 0 {
			return fmt.Errorf("LOAD segment alignment is %#x, want at least %d (0x4000) for 16 KB pages", align, androidPageSize)
		}
	}
	return nil
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fileContains(path string, needle []byte) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// Artifacts are ~10 MB; read them whole so a string split across a
	// buffer boundary is still found.
	data, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	return bytes.Contains(data, needle), nil
}
