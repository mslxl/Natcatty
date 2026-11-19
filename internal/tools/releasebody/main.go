// Command releasebody composes the body of a GitHub release. GitHub generates
// the notes from the tag's commits; checked-in release documents are appended
// to them. The identifier-migration note is appended only for the first
// release whose manifest carries the new identifier, so a user who reads only
// the release page learns about the one-time reset. Notes passed with
// -append (the Android sideload note, for example) are always appended once.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// stringList collects a flag that may be repeated.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// manifest is the part of mygo.json this tool reads.
type manifest struct {
	Identifier string `json:"identifier"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "releasebody: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("releasebody", flag.ContinueOnError)
	notes := fs.String("notes", "", "generated release-body file to read")
	out := fs.String("out", "", "path to write the composed body")
	current := fs.String("manifest", "mygo.json", "manifest carrying the release's identifier")
	previous := fs.String("previous-manifest", "", "manifest of the previous release; absent or empty means this is the first release")
	fragment := fs.String("fragment", "", "checked-in migration note to append")
	var appends stringList
	fs.Var(&appends, "append", "checked-in note to always append (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *notes == "" || *out == "" || *fragment == "" {
		return errors.New("-notes, -out and -fragment are required")
	}

	currentIdentifier, err := manifestIdentifier(*current)
	if err != nil {
		return fmt.Errorf("read the current manifest: %w", err)
	}
	previousID := ""
	if *previous != "" {
		previousID = previousIdentifier(*previous)
	}

	generated, err := os.ReadFile(*notes)
	if err != nil {
		return fmt.Errorf("read the generated notes: %w", err)
	}
	note, err := os.ReadFile(*fragment)
	if err != nil {
		return fmt.Errorf("read the migration note: %w", err)
	}

	body, appended, err := compose(generated, note, previousID, currentIdentifier)
	if err != nil {
		return err
	}
	for _, path := range appends {
		extra, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read the appended note %s: %w", path, err)
		}
		var added bool
		body, added, err = appendNote(body, extra)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if added {
			fmt.Printf("release body: appended %s\n", filepath.Base(path))
		}
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		return fmt.Errorf("write the composed body: %w", err)
	}
	switch {
	case appended:
		fmt.Printf("release body: appended the identifier-migration note (previous identifier %q)\n", previousID)
	case previousID != currentIdentifier:
		fmt.Println("release body: the identifier-migration note is already present")
	default:
		fmt.Printf("release body: no migration note (identifier %q already released)\n", currentIdentifier)
	}
	return nil
}

// readManifest decodes the fields this tool uses from a mygo.json manifest.
func readManifest(path string) (manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

// manifestIdentifier reads the identifier from a mygo.json manifest. A missing
// or empty identifier is an error: the release must not silently skip the note
// because the manifest changed shape.
func manifestIdentifier(path string) (string, error) {
	m, err := readManifest(path)
	if err != nil {
		return "", err
	}
	identifier := strings.TrimSpace(m.Identifier)
	if identifier == "" {
		return "", fmt.Errorf("%s declares no identifier", path)
	}
	return identifier, nil
}

// previousIdentifier returns the identifier the previous release shipped. Any
// unreadable or malformed input is treated as unknown rather than failing the
// release: an unknown previous identifier includes the migration note, which
// is the safe direction for a user reading only the release page.
func previousIdentifier(path string) string {
	m, err := readManifest(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(m.Identifier)
}

// compose appends the migration note unless the previous release already
// shipped the current identifier or the generated body already carries the
// note. appended reports whether the returned body gained the note.
func compose(generated, fragment []byte, previousIdentifier, currentIdentifier string) (body []byte, appended bool, err error) {
	if strings.TrimSpace(currentIdentifier) == "" {
		return nil, false, errors.New("the current manifest declares no identifier")
	}
	note := bytes.TrimSpace(fragment)
	if len(note) == 0 {
		return nil, false, errors.New("the migration note is empty")
	}
	if previousIdentifier == currentIdentifier {
		return generated, false, nil
	}
	if bytes.Contains(generated, note) {
		return generated, false, nil
	}

	body = append([]byte(nil), generated...)
	if len(body) > 0 && !bytes.HasSuffix(body, []byte("\n")) {
		body = append(body, '\n')
	}
	if len(body) > 0 {
		body = append(body, '\n')
	}
	body = append(body, note...)
	body = append(body, '\n')
	return body, true, nil
}

// appendNote appends note after body unless body already carries it.
// added reports whether the returned body gained the note.
func appendNote(body, note []byte) ([]byte, bool, error) {
	trimmed := bytes.TrimSpace(note)
	if len(trimmed) == 0 {
		return nil, false, errors.New("an appended note is empty")
	}
	if bytes.Contains(body, trimmed) {
		return body, false, nil
	}
	out := append([]byte(nil), body...)
	if len(out) > 0 && !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	if len(out) > 0 {
		out = append(out, '\n')
	}
	out = append(out, trimmed...)
	out = append(out, '\n')
	return out, true, nil
}
