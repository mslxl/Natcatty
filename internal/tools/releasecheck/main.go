// Command releasecheck validates release tags against mygo.json and handles
// SHA256SUMS and artifact assertions for the release workflow. It is checked
// Go tooling, not part of the shipped app.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mslxl/natcatty/internal/release"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "releasecheck:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	command, rest := args[0], args[1:]
	switch command {
	case "validate-tag":
		return validateTagCommand(rest)
	case "version":
		return versionCommand(rest)
	case "check-manifest":
		return checkManifestCommand(rest)
	case "generate-sums":
		return generateSumsCommand(rest)
	case "verify-sums":
		return verifySumsCommand(rest)
	case "collect":
		return collectCommand(rest)
	case "classify":
		return classifyCommand(rest)
	case "assert-no-updates":
		return assertNoUpdatesCommand(rest)
	case "check-binary":
		return checkBinaryCommand(rest)
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("unknown command %q\n\n%s", command, usageText)
	}
}

func usage() error {
	fmt.Fprint(os.Stderr, usageText)
	return flag.ErrHelp
}

const usageText = `usage: releasecheck <command> [flags]

commands:
  validate-tag      -tag vX.Y.Z [-manifest mygo.json]
  version           [-manifest mygo.json]
  check-manifest    [-manifest mygo.json]
  generate-sums     -dir DIR [-out FILE]
  verify-sums       -dir DIR [-sums FILE]
  collect           -from DIR -to DIR
  classify          -dir DIR
  assert-no-updates [-dir DIR] [-manifest mygo.json]
  check-binary      -binary FILE [-catalog-dir ui/locales]
`

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	return flags
}

func validateTagCommand(args []string) error {
	flags := newFlagSet("validate-tag")
	tag := flags.String("tag", "", "release tag, e.g. v0.1.0")
	manifestPath := flags.String("manifest", "mygo.json", "path to mygo.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *tag == "" {
		return fmt.Errorf("validate-tag needs -tag")
	}
	version, err := manifestVersion(*manifestPath)
	if err != nil {
		return err
	}
	if err := release.ValidateTag(*tag, version); err != nil {
		return err
	}
	fmt.Printf("%s matches mygo.json version %s\n", *tag, version)
	return nil
}

func versionCommand(args []string) error {
	flags := newFlagSet("version")
	manifestPath := flags.String("manifest", "mygo.json", "path to mygo.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	version, err := manifestVersion(*manifestPath)
	if err != nil {
		return err
	}
	fmt.Println(version)
	return nil
}

func checkManifestCommand(args []string) error {
	flags := newFlagSet("check-manifest")
	manifestPath := flags.String("manifest", "mygo.json", "path to mygo.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := checkManifest(*manifestPath); err != nil {
		return err
	}
	fmt.Printf("%s has no signing, notarization, or update configuration\n", *manifestPath)
	return nil
}

func generateSumsCommand(args []string) error {
	flags := newFlagSet("generate-sums")
	dir := flags.String("dir", "", "directory of release artifacts")
	out := flags.String("out", "", "SHA256SUMS path (default DIR/SHA256SUMS)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("generate-sums needs -dir")
	}
	sumsPath := *out
	if sumsPath == "" {
		sumsPath = *dir + string(os.PathSeparator) + "SHA256SUMS"
	}
	entries, err := writeSums(*dir, sumsPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fmt.Printf("%s  %s\n", entry.Sum, entry.Name)
	}
	fmt.Printf("wrote %s\n", sumsPath)
	return nil
}

func verifySumsCommand(args []string) error {
	flags := newFlagSet("verify-sums")
	dir := flags.String("dir", "", "directory of release artifacts")
	sumsPath := flags.String("sums", "", "SHA256SUMS path (default DIR/SHA256SUMS)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("verify-sums needs -dir")
	}
	if *sumsPath == "" {
		*sumsPath = *dir + string(os.PathSeparator) + "SHA256SUMS"
	}
	entries, err := verifySums(*dir, *sumsPath)
	if err != nil {
		return err
	}
	fmt.Printf("verified %d artifacts from %s\n", len(entries), *sumsPath)
	return nil
}

func collectCommand(args []string) error {
	flags := newFlagSet("collect")
	from := flags.String("from", "", "directory tree with downloaded workflow artifacts")
	to := flags.String("to", "", "flat directory for the aggregate checksums")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return fmt.Errorf("collect needs -from and -to")
	}
	names, err := collect(*from, *to)
	if err != nil {
		return err
	}
	for _, name := range names {
		fmt.Println(name)
	}
	fmt.Printf("collected %d artifacts into %s\n", len(names), *to)
	return nil
}

// classifyCommand names each downloaded workflow-artifact directory as a
// platform artifact set when it carries SHA256SUMS. The release workflow's
// checksums job reads this output instead of guessing from file names, so the
// two cannot drift. The output is one "kind<TAB>path" line per directory, and
// anything that is not a platform artifact set is an error.
func classifyCommand(args []string) error {
	flags := newFlagSet("classify")
	dir := flags.String("dir", "", "download root holding one directory per workflow artifact")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("classify needs -dir")
	}
	dirs, err := release.ClassifyArtifactDirs(*dir)
	if err != nil {
		return err
	}
	for _, classified := range dirs {
		fmt.Printf("%s\t%s\n", classified.Kind, classified.Path)
	}
	return nil
}

func assertNoUpdatesCommand(args []string) error {
	flags := newFlagSet("assert-no-updates")
	dir := flags.String("dir", "", "artifact directory to inspect")
	manifestPath := flags.String("manifest", "", "path to mygo.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" && *manifestPath == "" {
		return fmt.Errorf("assert-no-updates needs -dir and/or -manifest")
	}
	if err := checkNoUpdateMetadata(*dir, *manifestPath); err != nil {
		return err
	}
	fmt.Println("no update or signing metadata found")
	return nil
}

func checkBinaryCommand(args []string) error {
	flags := newFlagSet("check-binary")
	binaryPath := flags.String("binary", "", "packaged executable to inspect")
	catalogDir := flags.String("catalog-dir", "ui/locales", "directory of the JSON catalogs")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *binaryPath == "" {
		return fmt.Errorf("check-binary needs -binary")
	}
	if err := checkEmbeddedCatalogs(*binaryPath, *catalogDir); err != nil {
		return err
	}
	fmt.Printf("%s embeds all %d catalogs\n", *binaryPath, len(CatalogTags))
	return nil
}
