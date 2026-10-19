// Command paritygen validates docs/parity/matrix.yaml and regenerates
// docs/parity/matrix.md. Run it from anywhere inside the module:
//
//	go run ./internal/tools/paritygen          # validate and regenerate
//	go run ./internal/tools/paritygen -check   # validate and fail if stale
package main

import (
	"fmt"
	"os"
)

func main() {
	root, err := findRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "paritygen: %v\n", err)
		os.Exit(2)
	}
	os.Exit(run(os.Args[1:], root, os.Stdout, os.Stderr))
}
