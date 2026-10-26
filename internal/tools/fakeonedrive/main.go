// Command fakeonedrive runs the in-process fake Microsoft identity and OneDrive
// approot server on a fixed loopback port for GUI acceptance runs. The app is
// pointed at it with NATCATTY_ONEDRIVE_AUTH_BASE, NATCATTY_ONEDRIVE_TOKEN_BASE,
// NATCATTY_ONEDRIVE_GRAPH_BASE, and NATCATTY_ONEDRIVE_CLIENT_ID. Control and
// state are HTTP endpoints (/__control, /__state); no real Microsoft credential
// or network is used.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mslxl/natcatty/adapters/onedrive/fake"
)

func main() {
	address := flag.String("addr", "127.0.0.1:0", "loopback address to listen on")
	printClientID := flag.Bool("print-client-id", false, "print only the client id")
	flag.Parse()

	server, err := fake.StartAt(*address)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeonedrive:", err)
		os.Exit(1)
	}
	defer server.Close()
	state := server.State()
	if *printClientID {
		fmt.Println(state.ClientID)
		return
	}
	encoded, _ := json.Marshal(map[string]string{
		"url":      state.URL,
		"clientId": state.ClientID,
	})
	fmt.Println(string(encoded))
	fmt.Fprintln(os.Stderr, "fakeonedrive listening; control at "+state.URL+"/__control, state at "+state.URL+"/__state")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}
