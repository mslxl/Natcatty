//go:build launchsmoke

// Command launchsmoke is the launch smoke test's binary. It runs the real app
// and quits after the main window is ready.
package main

import (
	"log"

	"github.com/mslxl/natcatty/app"
)

func main() {
	if err := app.RunSmoke(); err != nil {
		log.Fatal(err)
	}
}
