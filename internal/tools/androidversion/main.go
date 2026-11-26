// Command androidversion maps a SemVer release version to the Android
// versionCode MyGo writes into the APK.
//
// Android orders installs by versionCode, not by the version name, so an
// in-place upgrade needs every release to have a strictly greater code than
// the release before it. MyGo's default (major*10000 + minor*100 + patch)
// collapses every pre-release of a version onto the final release's code, so
// this tool owns the mapping instead:
//
//	MAJOR * 100000000 + MINOR * 1000000 + PATCH * 10000 + SUFFIX
//
// SUFFIX is 9999 for a final release and RANK * 1000 + N for a prerelease
// `<stage>.<N>`, where alpha=0, beta=1 and rc=2. SemVer orders those stages
// alpha < beta < rc < final, and the mapping keeps that order numerically:
//
//	0.1.0-beta.2 -> 1001002   0.1.0-rc.1 -> 1002001   0.1.0 -> 1009999
//
// Tags may carry a leading v and build metadata; both are ignored. MAJOR is
// capped at 20 so every code stays within Android's 2100000000 limit.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "androidversion: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("androidversion", flag.ContinueOnError)
	version := fs.String("version", "", "release version, for example 0.1.0 or v0.1.0-beta.2")
	previous := fs.String("previous", "", "previous release's version; fail unless -version has a greater code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return errors.New("-version is required")
	}
	code, err := versionCode(*version)
	if err != nil {
		return err
	}
	if *previous != "" {
		if code, err = checkIncrease(*version, *previous); err != nil {
			return err
		}
	}
	fmt.Println(code)
	return nil
}

// checkIncrease returns current's version code and fails unless it is
// strictly greater than previous's.
func checkIncrease(current, previous string) (int, error) {
	code, err := versionCode(current)
	if err != nil {
		return 0, err
	}
	prev, err := versionCode(previous)
	if err != nil {
		return 0, fmt.Errorf("previous release: %w", err)
	}
	if code <= prev {
		return 0, fmt.Errorf("version code %d for %s is not greater than %d for %s: "+
			"Android refuses to install it over the previous release", code, current, prev, previous)
	}
	return code, nil
}

// versionCode is the APK versionCode for a SemVer release version.
func versionCode(version string) (int, error) {
	v := strings.TrimSpace(version)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return 0, errors.New("empty version")
	}
	// Build metadata does not order releases.
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("%q is not MAJOR.MINOR.PATCH", version)
	}
	var numbers [3]int
	for i, part := range parts {
		n, err := numeric(part)
		if err != nil {
			return 0, fmt.Errorf("%q: %s is not a number", version, part)
		}
		numbers[i] = n
	}
	major, minor, patch := numbers[0], numbers[1], numbers[2]
	if major > 20 {
		return 0, fmt.Errorf("%q: major version %d makes the version code exceed Android's 2100000000 limit", version, major)
	}
	if minor > 99 || patch > 99 {
		return 0, fmt.Errorf("%q: minor and patch versions must be at most 99", version)
	}

	suffix := 9999 // a final release follows every prerelease of the same version
	if hasPre {
		if pre == "" {
			return 0, fmt.Errorf("%q: empty pre-release", version)
		}
		ids := strings.Split(pre, ".")
		if len(ids) > 2 {
			return 0, fmt.Errorf("%q: pre-release must be <alpha|beta|rc>[.<number>]", version)
		}
		ranks := map[string]int{"alpha": 0, "beta": 1, "rc": 2}
		rank, ok := ranks[ids[0]]
		if !ok {
			return 0, fmt.Errorf("%q: pre-release stage %q is not alpha, beta or rc", version, ids[0])
		}
		n := 0
		if len(ids) == 2 {
			var err error
			if n, err = numeric(ids[1]); err != nil {
				return 0, fmt.Errorf("%q: pre-release number %q is not a number", version, ids[1])
			}
			if n > 999 {
				return 0, fmt.Errorf("%q: pre-release number %d is above 999", version, n)
			}
		}
		suffix = rank*1000 + n
	}

	code := major*100000000 + minor*1000000 + patch*10000 + suffix
	if code <= 0 || code > 2100000000 {
		return 0, fmt.Errorf("%q maps to version code %d, outside Android's 1..2100000000 range", version, code)
	}
	return code, nil
}

// numeric parses a non-empty run of ASCII digits. strconv.Atoi also accepts a
// sign, which a version component must not carry.
func numeric(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty number")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
	}
	return strconv.Atoi(s)
}
