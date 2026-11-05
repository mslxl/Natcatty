// Command sourceoffer writes the per-release Corresponding Source offer that
// every packaged artifact carries. The legal text lives in internal/legal so
// the app and the release tooling share one source.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mslxl/natcatty/internal/legal"
)

func main() {
	version := flag.String("version", "", "release version, with or without a leading v")
	out := flag.String("out", "", "output path for SOURCE-OFFER.md")
	flag.Parse()
	if *version == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "sourceoffer: -version and -out are required")
		os.Exit(2)
	}
	if dir := filepath.Dir(*out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "sourceoffer: %v\n", err)
			os.Exit(1)
		}
	}
	if err := os.WriteFile(*out, []byte(legal.SourceOfferDocument(*version)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "sourceoffer: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote the corresponding-source offer for %s to %s\n", *version, *out)
}
