// Command releasenotes generates RELEASE_NOTES.md for one version from the
// repository's Keep a Changelog file. It is checked release tooling: the
// release workflow runs it after the tag matches mygo.json and uploads the
// result as a workflow artifact. It never creates or publishes a GitHub
// release.
//
//	go run ./internal/tools/releasenotes -version 0.1.0 -changelog CHANGELOG.md -out RELEASE_NOTES.md
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mslxl/natcatty/internal/release"
	"github.com/mslxl/natcatty/internal/releasenotes"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "releasenotes:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("releasenotes", flag.ContinueOnError)
	flags.SetOutput(stderr)
	version := flags.String("version", "", "release version, with or without a leading v")
	changelogPath := flags.String("changelog", "CHANGELOG.md", "Keep a Changelog file")
	outPath := flags.String("out", "", "output path; empty writes to stdout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return fmt.Errorf("-version is required")
	}
	normalized, err := normalizeVersion(*version)
	if err != nil {
		return err
	}
	changelog, err := os.ReadFile(*changelogPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", *changelogPath, err)
	}
	notes, err := releasenotes.Generate(changelog, normalized)
	if err != nil {
		return err
	}
	if *outPath == "" {
		_, err = stdout.Write(notes)
		return err
	}
	if dir := filepath.Dir(*outPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(*outPath, notes, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *outPath, err)
	}
	fmt.Fprintf(stdout, "wrote release notes for %s to %s\n", normalized, *outPath)
	return nil
}

// normalizeVersion accepts the strict manifest version or the release tag and
// returns the manifest spelling the changelog heading uses.
func normalizeVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "v") {
		parsed, err := release.ParseTag(value)
		if err != nil {
			return "", err
		}
		return parsed.String(), nil
	}
	parsed, err := release.ParseVersion(value)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}
