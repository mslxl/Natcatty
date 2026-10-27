// Command fakes3 runs the in-process S3 fake as a standalone local server for
// GUI acceptance. It listens on loopback only, prints one JSON line with the
// endpoint and control URLs, and exits on Ctrl+C after writing the final state.
//
// The server is a test double, not AWS. It never contacts a network and its
// credentials are local placeholders. The ready line deliberately omits the
// access key id, secret access key, and session token: read those from the
// fake package's documented local defaults, never from a log.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	s3fake "github.com/mslxl/natcatty/adapters/s3/fake"
)

const toolSessionToken = "fakes3-session-token"

type readyInfo struct {
	URL                  string `json:"url"`
	StateURL             string `json:"stateUrl"`
	ControlURL           string `json:"controlUrl"`
	Bucket               string `json:"bucket"`
	Region               string `json:"region"`
	BasePath             string `json:"basePath,omitempty"`
	SessionTokenRequired bool   `json:"sessionTokenRequired"`
	CredentialSource     string `json:"credentialSource"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakes3:", err)
		os.Exit(1)
	}
}

func run() error {
	address := flag.String("addr", "127.0.0.1:0", "loopback listen address")
	accessKey := flag.String("access-key", s3fake.DefaultAccessKeyID, "local test access key id")
	secretKey := flag.String("secret-key", s3fake.DefaultSecretAccessKey, "local test secret access key")
	sessionToken := flag.String("session-token", toolSessionToken, "required session token; pass an empty string to disable")
	region := flag.String("region", s3fake.DefaultRegion, "bucket region")
	bucket := flag.String("bucket", s3fake.DefaultBucket, "bucket name")
	basePath := flag.String("base-path", "", "optional endpoint base path, e.g. /s3/gateway")
	forbidden := flag.Bool("forbidden", false, "refuse every authenticated request with 403")
	serverError := flag.Bool("server-error", false, "answer every request with 500")
	readOnly := flag.Bool("readonly", false, "refuse PUT with 403")
	conflictWrites := flag.Bool("conflict-writes", false, "answer every conditional PUT with 412")
	delayMS := flag.Int("delay-ms", 0, "delay each request, for exercising the syncing state")
	stateFile := flag.String("state-file", "", "write the final acceptance state JSON here on exit")
	flag.Parse()

	server, err := s3fake.StartAt(*address)
	if err != nil {
		return err
	}
	defer server.Close()
	server.Reset()
	server.Configure(func(control *s3fake.Control) {
		control.AccessKeyID = *accessKey
		control.SecretAccessKey = *secretKey
		control.SessionToken = *sessionToken
		control.Region = *region
		control.Bucket = *bucket
		control.BasePath = *basePath
		control.Forbidden = *forbidden
		control.ServerError = *serverError
		control.ReadOnly = *readOnly
		control.ConflictWrites = *conflictWrites
		control.DelayMS = *delayMS
	})

	state := server.State()
	info := readyInfo{
		URL:                  server.URL() + state.Control.BasePath,
		StateURL:             server.URL() + "/__state",
		ControlURL:           server.URL() + "/__control",
		Bucket:               state.Bucket,
		Region:               state.Region,
		BasePath:             state.Control.BasePath,
		SessionTokenRequired: state.Control.SessionTokenNeeded,
		CredentialSource:     "adapters/s3/fake documented local defaults",
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
		final, err := json.MarshalIndent(server.State(), "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*stateFile, append(final, '\n'), 0o600); err != nil {
			return fmt.Errorf("write the state file: %w", err)
		}
	}
	return nil
}
