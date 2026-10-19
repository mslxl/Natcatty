package main

import (
	"log"

	"github.com/mslxl/natcatty/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
