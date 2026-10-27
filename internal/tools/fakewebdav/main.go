// Command fakewebdav runs the in-process WebDAV fake as a standalone local
// server for GUI acceptance. It listens on loopback only, prints one JSON line
// with the URLs to use, and exits on Ctrl+C after writing the final state.
//
// The server is a test double, not a real WebDAV service. It never contacts a
// network and its credentials and token are local placeholders.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	webdavfake "github.com/mslxl/natcatty/adapters/webdav/fake"
)

const (
	toolUsername = "dav-user"
	toolPassword = "dav-secret"
	toolToken    = "fakewebdav-tool-token"
)

type readyInfo struct {
	URL           string `json:"url"`
	CollectionURL string `json:"collectionUrl"`
	StateURL      string `json:"stateUrl"`
	ControlURL    string `json:"controlUrl"`
	AuthMode      string `json:"authMode"`
	Username      string `json:"username"`
	TLS           bool   `json:"tls"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakewebdav:", err)
		os.Exit(1)
	}
}

func run() error {
	address := flag.String("addr", "127.0.0.1:0", "loopback listen address")
	authMode := flag.String("auth", "basic", "authentication mode: basic, digest, or bearer")
	username := flag.String("username", toolUsername, "Basic or Digest username")
	password := flag.String("password", toolPassword, "Basic or Digest password (local test value)")
	token := flag.String("token", toolToken, "bearer token (local test value)")
	collection := flag.String("collection", "/dav", "collection path")
	tlsMode := flag.Bool("tls", false, "serve a self-signed TLS certificate (the app must reject it)")
	digestAlgorithm := flag.String("digest-algorithm", "MD5", "Digest algorithm: MD5, MD5-sess, SHA-256, or SHA-256-sess")
	stale := flag.Bool("stale", false, "answer the first valid Digest request with stale=true once")
	noETag := flag.Bool("no-etag", false, "omit ETags from responses")
	disableHead := flag.Bool("disable-head", false, "answer HEAD with 405")
	readOnly := flag.Bool("readonly", false, "refuse PUT with 403")
	forbidden := flag.Bool("forbidden", false, "refuse every authenticated request with 403")
	redirect := flag.String("redirect", "", "redirect every request: other-host, downgrade, loop, or same-host")
	delayMS := flag.Int("delay-ms", 0, "delay each request, for exercising the syncing state")
	stateFile := flag.String("state-file", "", "write the final acceptance state JSON here on exit")
	flag.Parse()

	if *authMode != "basic" && *authMode != "digest" && *authMode != "bearer" {
		return fmt.Errorf("-auth must be basic, digest, or bearer")
	}

	var (
		server *webdavfake.Server
		err    error
	)
	if *tlsMode {
		server, err = webdavfake.StartTLSAt(*address)
	} else {
		server, err = webdavfake.StartAt(*address)
	}
	if err != nil {
		return err
	}
	defer server.Close()
	server.Reset()
	if err := server.SetCollection(*collection); err != nil {
		return err
	}
	server.Configure(func(control *webdavfake.Control) {
		control.AuthMode = *authMode
		control.Username = *username
		control.Password = *password
		control.Token = *token
		control.DigestAlgorithm = *digestAlgorithm
		control.DigestStale = *stale
		control.NoETag = *noETag
		control.DisableHead = *disableHead
		control.ReadOnly = *readOnly
		control.Forbidden = *forbidden
		control.Redirect = *redirect
		control.DelayMS = *delayMS
	})

	info := readyInfo{
		URL:           server.URL(),
		CollectionURL: server.CollectionURL(),
		StateURL:      server.URL() + "/__state",
		ControlURL:    server.URL() + "/__control",
		AuthMode:      *authMode,
		Username:      *username,
		TLS:           *tlsMode,
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	if *stateFile != "" {
		state, err := json.MarshalIndent(server.State(), "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*stateFile, append(state, '\n'), 0o600); err != nil {
			return fmt.Errorf("write the state file: %w", err)
		}
	}
	return nil
}
