package ssh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// TestOpenSubsystemServesSFTP opens "sftp" through the real SSH handshake and
// hands the stream to the real SFTP adapter. It proves the new subsystem
// capability, the channel request path, and the adapter agree end to end.
func TestOpenSubsystemServesSFTP(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, dialFunc := startFixture(t, fakessh.Options{
		Password: "s3cret",
		Subsystems: map[string]func(ssh.Channel){
			"sftp": func(channel ssh.Channel) {
				server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(root))
				if err != nil {
					_ = channel.Close()
					return
				}
				_ = server.Serve()
			},
		},
	})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "tester", Password: "s3cret",
		VerifyHostKey: func(context.Context, domainconn.Key) error { return nil },
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	subsystems, ok := client.(appconnection.SubsystemClient)
	if !ok {
		t.Fatal("the SSH client does not expose the subsystem capability")
	}
	stream, err := subsystems.OpenSubsystem(context.Background(), "sftp")
	if err != nil {
		t.Fatalf("OpenSubsystem: %v", err)
	}
	remote, err := adaptersftp.NewRemote(stream, func(error) error { return nil })
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })

	ctx := context.Background()
	dir, err := remote.Root(ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	entries, err := remote.List(ctx, dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "hello.txt" && entry.Size == 5 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the SFTP listing is %+v", entries)
	}
	if got := server.SubsystemRequests(); len(got) != 1 || got[0] != "sftp" {
		t.Fatalf("subsystem requests = %v", got)
	}

	if _, err := subsystems.OpenSubsystem(ctx, "unknown"); err == nil {
		t.Fatal("an unserved subsystem name was accepted")
	} else if !errors.Is(err, appconnection.ErrSubsystemUnavailable) {
		t.Fatalf("a refused subsystem is not classified as unavailable: %v", err)
	} else {
		var typed *appconnection.Error
		if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
			t.Fatalf("a refused subsystem is %#v, want unsupported", err)
		}
	}
}
