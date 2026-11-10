// Command natcatty-plugin scaffolds, builds, checks, runs, and launches a
// plugin without the app. `run` uses the fake host; `dev` hands the module to
// the app's NATCATTY_PLUGIN_DIR development mode.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mslxl/natcatty/sdk/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], cli.Options{}); err != nil {
		fmt.Fprintf(os.Stderr, "natcatty-plugin: %v\n", err)
		os.Exit(1)
	}
}
