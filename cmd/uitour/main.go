//go:build uitour

// Command uitour runs the app on a throwaway profile and captures every page.
package main

import (
	"log"

	"github.com/mslxl/natcatty/app"
)

func main() {
	if err := app.RunUITour(); err != nil {
		log.Fatal(err)
	}
}
