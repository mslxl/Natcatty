// Command ghosttyvt builds libghostty-vt for natcatty's phone targets from
// the pinned Ghostty commit, verifies every artifact against the ABI the Go
// bindings mirror, and writes the artifact manifest.
//
// This is normally run through scripts/mobile/build-ghostty-vt.sh; see that
// script for prerequisites and examples.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("ghosttyvt: ")
	if err := run(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("no command")
	}
	switch args[0] {
	case "build":
		return cmdBuild(args[1:])
	case "fetch":
		return cmdFetch(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "manifest":
		return cmdManifest(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ghosttyvt builds the pinned libghostty-vt for phones.

Usage:
  scripts/mobile/build-ghostty-vt.sh build [flags]
  scripts/mobile/build-ghostty-vt.sh fetch [flags]
  scripts/mobile/build-ghostty-vt.sh verify [flags]
  scripts/mobile/build-ghostty-vt.sh manifest [flags]

build flags:
  --out DIR         artifact directory (default build/ghostty-vt)
  --manifest PATH   manifest to write (default ui/terminal/artifacts.json)
  --platforms P     all (default), ios or android
  --zig PATH        zig binary; must be 0.16.0
  --ndk PATH        Android NDK root; default $ANDROID_NDK_HOME
  --cache DIR       download and build cache
  --release URL     release URL recorded in the manifest
  --no-manifest     build and verify without recording artifacts

fetch flags:
  --out DIR         artifact directory (default build/ghostty-vt)
  --manifest PATH   manifest naming each artifact (default ui/terminal/artifacts.json)
  --platform P      all (default), ios or android
  --build-if-missing
                    build the pinned commit from source when the release
                    artifact cannot be downloaded (default true)
  --build-if-missing=false
                    require the release; fail instead of building

verify flags:
  --out DIR         artifact directory (default build/ghostty-vt)
  --manifest PATH   manifest to verify (default ui/terminal/artifacts.json)
  --ndk PATH        Android NDK root; default $ANDROID_NDK_HOME

Prerequisites:
  iOS      macOS with Xcode; run under: nix develop .#ios
  Android  Android NDK; run under: nix develop .#android
  Zig      pinned 0.16.0 is downloaded and sha256-checked when --zig/$ZIG
           and PATH do not already provide it.

The build downloads the Ghostty tarball for the pinned commit, verifies its
sha256, builds the iOS device and simulator static libraries and the three
Android ABI archives, and fails if an artifact is missing a binding symbol,
carries the wrong library version, or cannot be linked with 16 KB pages.
`)
}
