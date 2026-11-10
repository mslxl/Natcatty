package app

import (
	"context"
	"io"
	"net"
	"testing"
)

// A local Session's browser reaches this computer's own services, as a Host's
// reaches the Host's.
func TestLocalBrowserTunnelDialsThisComputer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_, _ = conn.Write([]byte("hello"))
			_ = conn.Close()
		}
	}()

	tunnel, err := localBrowserConnector{}.Connect(context.Background(), thisComputerID)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tunnel.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(conn)
	_ = conn.Close()
	if string(got) != "hello" {
		t.Fatalf("read %q through the tunnel", got)
	}

	select {
	case <-tunnel.Done():
		t.Fatal("the tunnel ended before it was discarded")
	default:
	}
	tunnel.Discard()
	tunnel.Discard()
	<-tunnel.Done()
}
