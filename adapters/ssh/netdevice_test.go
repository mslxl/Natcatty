package ssh

import (
	"context"
	"io"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

// TestNetworkDeviceBannerUsesTheOneSessionChannel proves a device advertising a
// single-channel software token is driven entirely through the interactive
// channel: the banner reaches the caller for single-channel detection, and a
// bare CR-terminated command gets its answer without an exec or subsystem
// channel being requested.
func TestNetworkDeviceBannerUsesTheOneSessionChannel(t *testing.T) {
	const banner = "SSH-2.0-BHostSSH_7.0"
	commands := make(chan string, 1)
	server, dialFunc := startFixture(t, fakessh.Options{
		Password:      "s3cret",
		ServerVersion: banner,
		Shell: func(channel xssh.Channel) {
			buffer := make([]byte, 64)
			n, err := channel.Read(buffer)
			if err != nil {
				return
			}
			commands <- string(buffer[:n])
			_, _ = io.WriteString(channel, "show version\r\nVRP (R) software\r\n[switch]\r\n")
			// Leave the shell open; the client closes the channel.
			_, _ = io.Copy(io.Discard, channel)
		},
	})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "switch.example.com", Port: 22, Username: "admin", Password: "s3cret",
		VerifyHostKey: acceptEveryKey,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	versioned, ok := client.(interface{ RemoteVersion() string })
	if !ok {
		t.Fatalf("client %T does not expose the remote version", client)
	}
	if got := versioned.RemoteVersion(); got != banner {
		t.Fatalf("RemoteVersion = %q, want %q", got, banner)
	}
	if !appconnection.RemoteRequiresSingleChannel(versioned.RemoteVersion()) {
		t.Fatal("the device banner was not recognized as single-channel")
	}

	terminal, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })

	if _, err := terminal.Write([]byte("show version\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case received := <-commands:
		if received != "show version\r" {
			t.Fatalf("device received %q, want the bare command terminated by CR", received)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the command did not reach the device")
	}

	want := "show version\r\nVRP (R) software\r\n[switch]\r\n"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(terminal, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(got) != want {
		t.Fatalf("output = %q, want %q", got, want)
	}

	if channels := server.SessionChannels(); channels != 1 {
		t.Fatalf("session channels = %d, want exactly 1", channels)
	}
	if requests := server.ExecRequests(); len(requests) != 0 {
		t.Fatalf("exec requests = %q; raw mode must not open another channel", requests)
	}
	if requests := server.SubsystemRequests(); len(requests) != 0 {
		t.Fatalf("subsystem requests = %q; raw mode must not open another channel", requests)
	}
}
