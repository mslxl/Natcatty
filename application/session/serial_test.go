package session

import (
	"context"
	"testing"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

func TestOpenSerialCarriesThePortConfigAndLabel(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)

	cfg := domainserial.Config{
		Path:        "/dev/ttyUSB0",
		BaudRate:    57600,
		DataBits:    7,
		StopBits:    domainserial.StopBitsTwo,
		Parity:      domainserial.ParityEven,
		FlowControl: domainserial.FlowRTSCTS,
	}
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSerial, Serial: &cfg, Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	spec := factory.spec
	if spec.Kind != KindSerial || spec.Serial == nil || *spec.Serial != cfg {
		t.Fatalf("factory spec = %+v, want the serial config", spec)
	}
	if spec.Cols != 100 || spec.Rows != 30 {
		t.Fatalf("factory size = %dx%d, want 100x30", spec.Cols, spec.Rows)
	}
	if got, want := session.Label(), "Serial: ttyUSB0"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if snapshot := session.Snapshot(); snapshot.Kind != KindSerial {
		t.Fatalf("snapshot kind = %q, want %q", snapshot.Kind, KindSerial)
	}
}

func TestSerialSessionReadsWritesAndCloses(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	cfg := domainserial.Config{Path: "COM3"}
	session, err := service.Open(context.Background(), StartSpec{Kind: KindSerial, Serial: &cfg})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	conn := session.TerminalConn()
	factory.transport.send("OK\r\n")
	got := make([]byte, 4)
	if _, err := readWithTimeout(t, conn, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "OK\r\n" {
		t.Fatalf("output = %q, want OK\\r\\n", got)
	}

	if _, err := conn.Write([]byte("AT\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	eventually(t, "input to reach the serial transport", func() bool {
		return factory.transport.input() == "AT\r\n"
	})

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	eventually(t, "the serial Session to report closed", func() bool {
		return session.Snapshot().Status == StatusClosed
	})
}
