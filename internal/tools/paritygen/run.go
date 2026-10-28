package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mslxl/natcatty/internal/parity"
)

const (
	defaultCommit = "73d15da7"
	generatedPath = "docs/parity/matrix.md"
)

func loadMatrix(root string) (*parity.Matrix, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(parity.MatrixPath)))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", parity.MatrixPath, err)
	}
	m, err := parity.ParseMatrix(data)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// run is the CLI seam. It returns 0 on success, 1 for lint or freshness
// failures, and 2 for usage errors.
func run(args []string, root string, stdout, stderr io.Writer) int {
	check := false
	release := false
	commit := defaultCommit
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-check":
			check = true
		case "-release":
			release = true
		case "-commit":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "paritygen: -commit needs a value")
				return 2
			}
			commit = args[i]
		case "-h", "--help":
			fmt.Fprintln(stdout, "usage: paritygen [-check] [-release] [-commit <sha>]")
			return 0
		default:
			fmt.Fprintf(stderr, "paritygen: unknown argument %q\n", args[i])
			return 2
		}
	}

	m, err := loadMatrix(root)
	if err != nil {
		fmt.Fprintf(stderr, "paritygen: %v\n", err)
		return 1
	}
	tests, testErr := parity.BuildTestIndex(root)
	if testErr != nil {
		fmt.Fprintf(stderr, "paritygen: %v\n", testErr)
		return 1
	}
	errs := validate(repo{root: root, commit: commit}, m, tests)
	if release {
		errs = append(errs, validateRelease(repo{root: root, commit: commit}, m, tests)...)
	}
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintf(stderr, "paritygen: %v\n", err)
		}
		return 1
	}

	generated := render(m)
	outputPath := filepath.Join(root, filepath.FromSlash(generatedPath))
	if check {
		existing, err := os.ReadFile(outputPath)
		if err != nil || !bytes.Equal(existing, generated) {
			fmt.Fprintf(stderr, "paritygen: %s is stale; run go run ./internal/tools/paritygen\n", generatedPath)
			return 1
		}
		summarize(stdout, m)
		return 0
	}

	if err := os.WriteFile(outputPath, generated, 0o644); err != nil {
		fmt.Fprintf(stderr, "paritygen: write %s: %v\n", generatedPath, err)
		return 1
	}
	summarize(stdout, m)
	fmt.Fprintf(stdout, "wrote %s\n", generatedPath)
	return 0
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above the working directory")
		}
		dir = parent
	}
}
