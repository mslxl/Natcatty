package ssh

import (
	"io"
	"net"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

func TestVersionRecorderCapturesTheServerBanner(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	banner := "SSH-2.0-OpenSSH_9.6\r\n"
	payload := banner + "handshake bytes"
	go func() { _, _ = server.Write([]byte(payload)) }()

	recorder := &versionRecorder{Conn: client}
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(recorder, buffer); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buffer) != payload {
		t.Fatalf("read %q, want the untouched payload", buffer)
	}
	if got := recorder.Version(); got != "SSH-2.0-OpenSSH_9.6" {
		t.Fatalf("version = %q, want the trimmed identification line", got)
	}
}

func TestVersionRecorderIgnoresNonSSHLines(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() { _, _ = server.Write([]byte("garbage\r\nSSH-2.0-dropbear_2022\r\n")) }()
	recorder := &versionRecorder{Conn: client}
	buffer := make([]byte, 64)
	if _, err := recorder.Read(buffer); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := recorder.Version(); got != "SSH-2.0-dropbear_2022" {
		t.Fatalf("version = %q, want the later SSH line", got)
	}
}

func TestKeepaliveOnAClosedTransportFails(t *testing.T) {
	client := &sshClient{closed: true}
	if err := client.Keepalive(); err != appconnection.ErrClosed {
		t.Fatalf("Keepalive on a closed transport = %v, want ErrClosed", err)
	}
}

func TestRemoteVersionReportsTheCapturedBanner(t *testing.T) {
	client := &sshClient{remoteVersion: "SSH-2.0-OpenSSH_9.6"}
	if got := client.RemoteVersion(); got != "SSH-2.0-OpenSSH_9.6" {
		t.Fatalf("RemoteVersion = %q", got)
	}
}
