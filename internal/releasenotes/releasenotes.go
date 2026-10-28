// Package releasenotes turns the repository's Keep a Changelog file into the
// release notes for one version. Generation is deterministic: the same
// changelog and version produce byte-identical notes, so a release run can
// regenerate and compare them.
package releasenotes

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// Section is one version section of a Keep a Changelog file.
type Section struct {
	// Version is the bracketed heading text, for example "0.1.0" or
	// "Unreleased".
	Version string
	// Date is the ISO date after the version, empty when the heading has none.
	Date string
	// Body is the Markdown between this heading and the next level-2 heading.
	Body string
}

var versionHeading = regexp.MustCompile(`^##[ \t]+\[([^\]]+)\](?:[ \t]*-[ \t]*([0-9]{4}-[0-9]{2}-[0-9]{2}))?[ \t]*$`)

// Parse splits a Keep a Changelog document into its version sections. Level-2
// headings that are not version headings terminate the current section, so an
// unexpected "## Notes" cannot leak into a release.
func Parse(data []byte) ([]Section, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var sections []Section
	seen := map[string]int{}
	var current *Section
	var body []string
	flush := func() {
		if current == nil {
			return
		}
		current.Body = strings.TrimSpace(strings.Join(body, "\n"))
		sections = append(sections, *current)
		current = nil
		body = nil
	}
	for lineNo, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			match := versionHeading.FindStringSubmatch(trimmed)
			if match == nil {
				continue
			}
			if previous, ok := seen[match[1]]; ok {
				return nil, fmt.Errorf("the changelog defines version %q twice (lines %d and %d)", match[1], previous, lineNo+1)
			}
			seen[match[1]] = lineNo + 1
			current = &Section{Version: match[1], Date: match[2]}
			continue
		}
		if current != nil {
			body = append(body, line)
		}
	}
	flush()
	if len(sections) == 0 {
		return nil, fmt.Errorf("the changelog has no \"## [version]\" section")
	}
	return sections, nil
}

// Generate renders the release notes for version from a Keep a Changelog
// document. The version must name exactly one section with a non-empty body;
// an Unreleased section is never published as a release.
func Generate(data []byte, version string) ([]byte, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, fmt.Errorf("release version is empty")
	}
	sections, err := Parse(data)
	if err != nil {
		return nil, err
	}
	var found *Section
	for index := range sections {
		if sections[index].Version == version {
			found = &sections[index]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("the changelog has no section for version %q", version)
	}
	if found.Body == "" {
		return nil, fmt.Errorf("the changelog section for version %q has no changes", version)
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "# Natcatty %s\n\n", version)
	if found.Date != "" {
		fmt.Fprintf(&out, "Released %s.\n\n", found.Date)
	}
	out.WriteString(found.Body)
	out.WriteString("\n")
	return out.Bytes(), nil
}
