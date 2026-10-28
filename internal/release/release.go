// Package release holds the release-version rules shared by the packaging
// pipeline and the ticket 59 self-test.
//
// mygo.json is the only version source. A release tag is the strict semantic
// version "vMAJOR.MINOR.PATCH[-pre]": no leading zeros, a non-empty
// pre-release of dot-separated identifiers, and no build metadata, because
// mygo.json has no field to compare build metadata with. A tag is valid only
// when its version is exactly the manifest's.
package release

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a strict semantic version without the tag's leading "v".
type Version struct {
	Major, Minor, Patch int
	Pre                 []string
}

var (
	corePattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	identPattern   = regexp.MustCompile(`^[0-9A-Za-z-]+$`)
	numericPattern = regexp.MustCompile(`^[0-9]+$`)
)

// ParseVersion parses the manifest's MAJOR.MINOR.PATCH[-pre] version.
func ParseVersion(version string) (Version, error) {
	if version == "" {
		return Version{}, errors.New("version is empty")
	}
	if strings.HasPrefix(version, "v") {
		return Version{}, fmt.Errorf("version %q must not start with \"v\"; the tag carries the prefix", version)
	}
	core := version
	var pre []string
	if index := strings.IndexByte(version, '-'); index >= 0 {
		core = version[:index]
		rest := version[index+1:]
		if rest == "" {
			return Version{}, fmt.Errorf("version %q has an empty pre-release", version)
		}
		pre = strings.Split(rest, ".")
		for _, identifier := range pre {
			if err := validateIdentifier(identifier, version); err != nil {
				return Version{}, err
			}
		}
	}
	match := corePattern.FindStringSubmatch(core)
	if match == nil {
		return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH without leading zeros", version)
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return Version{}, fmt.Errorf("version %q: %w", version, err)
	}
	minor, err := strconv.Atoi(match[2])
	if err != nil {
		return Version{}, fmt.Errorf("version %q: %w", version, err)
	}
	patch, err := strconv.Atoi(match[3])
	if err != nil {
		return Version{}, fmt.Errorf("version %q: %w", version, err)
	}
	return Version{Major: major, Minor: minor, Patch: patch, Pre: pre}, nil
}

func validateIdentifier(identifier, version string) error {
	if identifier == "" {
		return fmt.Errorf("version %q has an empty pre-release identifier", version)
	}
	if !identPattern.MatchString(identifier) {
		return fmt.Errorf("version %q: pre-release identifier %q is not alphanumeric or a hyphen", version, identifier)
	}
	if numericPattern.MatchString(identifier) && len(identifier) > 1 && identifier[0] == '0' {
		return fmt.Errorf("version %q: numeric pre-release identifier %q has a leading zero", version, identifier)
	}
	return nil
}

// ParseTag parses a release tag, which is "v" followed by a strict version.
func ParseTag(tag string) (Version, error) {
	if !strings.HasPrefix(tag, "v") {
		return Version{}, fmt.Errorf("tag %q must start with \"v\"", tag)
	}
	if len(tag) == 1 {
		return Version{}, errors.New("tag is just \"v\"")
	}
	return ParseVersion(tag[1:])
}

// ValidateTag reports whether tag names exactly manifestVersion. Both must be
// strict versions, so a tag cannot smuggle another spelling of the same
// version past the manifest.
func ValidateTag(tag, manifestVersion string) error {
	if _, err := ParseVersion(manifestVersion); err != nil {
		return fmt.Errorf("mygo.json: %w", err)
	}
	version, err := ParseTag(tag)
	if err != nil {
		return err
	}
	if tag[1:] != manifestVersion {
		return fmt.Errorf("tag %q names version %s, but mygo.json says %s", tag, version, manifestVersion)
	}
	return nil
}

// String returns the version without the tag's leading "v".
func (v Version) String() string {
	out := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		out += "-" + strings.Join(v.Pre, ".")
	}
	return out
}
