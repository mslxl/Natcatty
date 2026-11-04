// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

// This test is the end-to-end half of ticket 29's parity claim: one scripted
// SSP sequence is delivered to stock mosh-client 1.4.0 in the pinned parity
// image and to a real application/session.Session backed by the in-process
// transport. The Session's view must produce exactly the bytes stock
// mosh-client writes before its exit banner.
//
// It is opt-in because it needs Docker:
//
//	NATCATTY_MOSH_PARITY=1 \
//	NATCATTY_MOSH_PARITY_IMAGE=natcatty/mosh-parity:1.4.0 \
//	  go test ./adapters/mosh -run TestSessionMatchesStockMoshClient -v

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshcore "github.com/mslxl/natcatty/internal/mosh"
	"github.com/mslxl/natcatty/internal/mosh/terminal"
)

// scriptedState is one host state in the differential. The fake server sends
// one state per client acknowledgement, so stock draws one frame per state,
// and the Go renderer is driven the same way.
type scriptedState struct {
	instructions []moshcore.HostInstruction
}

// goldenScriptStates loads the committed terminal corpus and replays the same
// event sequence, prefixed by a resize to each scenario's size. The corpus is
// the authority for which terminal behaviors are covered; this test adds the
// stock client's real terminal I/O, credentials, overlay, and shutdown paths.
func goldenScriptStates(t *testing.T) []scriptedState {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "internal", "mosh", "terminal",
		"testdata", "parity", "goldens", "*.json"))
	if err != nil {
		t.Fatalf("glob goldens: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no parity goldens found")
	}
	sort.Strings(paths)
	var states []scriptedState
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var golden goldenFileJSON
		if err := json.Unmarshal(data, &golden); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		states = append(states, scriptedState{instructions: []moshcore.HostInstruction{{
			Resize: true, Width: int32(golden.Width), Height: int32(golden.Height),
		}}})
		for _, event := range golden.Events {
			if len(event.Resize) == 2 {
				states = append(states, scriptedState{instructions: []moshcore.HostInstruction{{
					Resize: true, Width: int32(event.Resize[0]), Height: int32(event.Resize[1]),
				}}})
				continue
			}
			// mosh-server never sends terminal queries. Stock's
			// Complete::apply_string asserts that parsing a host state produces
			// no terminal-to-host reply, so a corpus event that queries the
			// terminal is not valid traffic for the stock-client differential.
			// It remains covered by the internal terminal-model comparison.
			probe := terminal.NewComplete(80, 24)
			if probe.Act(string(event.Input)) != "" {
				continue
			}
			states = append(states, scriptedState{instructions: []moshcore.HostInstruction{{
				HostString: event.Input,
			}}})
		}
	}
	return states
}

// scriptDriver advances the fake server one state per client ACK. Delivering
// states only after the previous one was acknowledged keeps stock's loop from
// coalescing several states into one Display::new_frame call.
type scriptDriver struct {
	steps    []scriptedState
	sent     int
	shutdown bool
}

func (d *scriptDriver) onInstruction(server *fakeServer, instruction moshcore.TransportInstruction) {
	if d.shutdown {
		return
	}
	ack := instruction.AckNum
	if sent := uint64(d.sent); ack >= sent && d.sent < len(d.steps) {
		server.sendHostInstructions(d.steps[d.sent].instructions, 0)
		d.sent++
		return
	}
	if d.sent == len(d.steps) && ack >= uint64(d.sent) {
		server.sendHostInstructions(nil, moshcore.ShutdownNum)
		d.shutdown = true
	}
}

func startScriptedServer(t *testing.T, states []scriptedState) *fakeServer {
	t.Helper()
	server := newFakeServerAt(t, net.IPv4zero, 2)
	driver := &scriptDriver{steps: states}
	server.scripted = true
	server.onDecoded = func(instruction moshcore.TransportInstruction) {
		driver.onInstruction(server, instruction)
	}
	server.start()
	return server
}

// dockerHostAddress returns the numeric IPv4 address that reaches the Docker
// host from a container. mosh-client requires a numeric peer because its
// getaddrinfo hints include AI_NUMERICHOST.
func dockerHostAddress(t *testing.T, image string) string {
	t.Helper()
	cmd := exec.Command("docker", "run", "--rm",
		"--add-host", "host.docker.internal:host-gateway",
		"--entrypoint", "getent", image, "ahostsv4", "host.docker.internal")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve the Docker host address: %v\n%s", err, stderr.String())
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || net.ParseIP(fields[0]) == nil {
		t.Fatalf("unexpected Docker host address output %q", out)
	}
	return fields[0]
}

// runStockMoshClient runs stock mosh-client in the parity image with a real
// PTY and returns its stdout up to (not including) the exit banner.
func runStockMoshClient(t *testing.T, server *fakeServer, cols, rows int) []byte {
	t.Helper()
	image := os.Getenv("NATCATTY_MOSH_PARITY_IMAGE")
	if image == "" {
		image = "natcatty/mosh-parity:1.4.0"
	}
	name := fmt.Sprintf("natcatty-stock-client-%d", time.Now().UnixNano())
	key := base64.RawStdEncoding.EncodeToString(server.key)
	// mosh-client 1.4.0 resolves its peer with AI_NUMERICHOST, so a hostname is
	// rejected even when getaddrinfo can resolve it. Ask Docker for the numeric
	// host-gateway address and pass that.
	host := dockerHostAddress(t, image)
	script := fmt.Sprintf("stty rows %d cols %d; exec mosh-client %s %d",
		rows, cols, host, server.Port())
	args := []string{
		"run", "--rm", "-t", "--name", name,
		"-e", "LANG=C.UTF-8",
		"-e", "LC_ALL=C.UTF-8",
		"-e", "TERM=xterm-256color",
		"-e", "MOSH_NO_TERM_INIT=1",
		"-e", "MOSH_KEY=" + key,
		"--entrypoint", "/bin/sh",
		image, "-c", script,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	t.Cleanup(func() {
		// A timed-out docker CLI can leave the TTY-enabled container behind.
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	if ctx.Err() != nil {
		t.Fatalf("stock mosh-client timed out after 90s; stderr: %s", stderr.String())
	}
	if err != nil {
		t.Fatalf("stock mosh-client: %v\nstderr: %s\nstdout: %q", err, stderr.String(), stdout.Bytes())
	}
	out := stdout.Bytes()
	for _, suffix := range [][]byte{
		[]byte("[mosh is exiting.]\r\n"),
		[]byte("[mosh is exiting.]\n"),
	} {
		if bytes.HasSuffix(out, suffix) {
			out = out[:len(out)-len(suffix)]
			return out
		}
	}
	t.Fatalf("stock client output has no exit banner; tail: %q", tail(out, 160))
	return nil
}

// referenceRendererOutput drives the terminal renderer directly with the same
// states. It localizes a mismatch before and after the Session is involved.
func referenceRendererOutput(t *testing.T, states []scriptedState) []byte {
	t.Helper()
	queue := newOutputQueue(8 << 20)
	renderer := newTerminalRendererWithOptions(queue, 80, 24, terminalRendererOptions{
		Display:     terminal.DefaultDisplayOptions(),
		TitlePrefix: []rune("[mosh] "),
	})
	renderer.setScreenSizeHandler(func(int, int, uint64) {})
	if err := renderer.emitInitial(); err != nil {
		t.Fatalf("emit initial: %v", err)
	}
	for i, state := range states {
		num := uint64(i + 1)
		if err := renderer.applyUpdate(hostState(num, num-1, true, state.instructions...)); err != nil {
			t.Fatalf("state %d: %v", num, err)
		}
		if err := renderer.emitLatest(); err != nil {
			t.Fatalf("state %d emit: %v", num, err)
		}
	}
	renderer.close()
	return drainQueue(queue)
}

// sessionOutput opens a real Session around the in-process transport, attaches
// the one live view, and returns every byte the view read until EOF. The sink
// records each server-authoritative size exactly where MyGo calls Resize.
func sessionOutput(t *testing.T, states []scriptedState) ([]byte, [][2]int) {
	t.Helper()
	server := startScriptedServer(t, states)
	factory := stockFactory{open: func(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
		return InProcessFactory{
			Bootstrap:    bannerBootstrap{banner: server.announcement()},
			ProbeTimeout: 2 * time.Second,
		}.Open(ctx, spec)
	}}
	service := appsession.NewService(factory)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := service.Open(ctx, inProcessTestSpec())
	if err != nil {
		t.Fatalf("open Session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	view, err := session.AttachView()
	if err != nil {
		t.Fatalf("attach view: %v", err)
	}
	t.Cleanup(func() { _ = view.Close() })

	var sizesMu sync.Mutex
	var sizes [][2]int
	view.SetScreenSizeSink(func(cols, rows int) {
		sizesMu.Lock()
		sizes = append(sizes, [2]int{cols, rows})
		sizesMu.Unlock()
	})

	type readResult struct {
		data []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		var collected bytes.Buffer
		buffer := make([]byte, 32<<10)
		for {
			n, readErr := view.Read(buffer)
			if n > 0 {
				collected.Write(buffer[:n])
			}
			if readErr != nil {
				done <- readResult{data: collected.Bytes(), err: readErr}
				return
			}
		}
	}()
	select {
	case result := <-done:
		if result.err != io.EOF {
			t.Fatalf("Session view ended with %v, want EOF", result.err)
		}
		sizesMu.Lock()
		defer sizesMu.Unlock()
		return result.data, append([][2]int(nil), sizes...)
	case <-ctx.Done():
		t.Fatal("Session did not finish within 90s")
		return nil, nil
	}
}

type stockFactory struct {
	open func(context.Context, appsession.StartSpec) (appsession.Transport, error)
}

func (f stockFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	return f.open(ctx, spec)
}

func tail(data []byte, n int) []byte {
	if len(data) <= n {
		return data
	}
	return data[len(data)-n:]
}

func firstDifference(got, want []byte) (int, []byte, []byte) {
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	for i := 0; i < limit; i++ {
		if got[i] != want[i] {
			return i, tail(got[:i+1], 80), tail(want[:i+1], 80)
		}
	}
	return limit, tail(got, 80), tail(want, 80)
}

// stockStartupNotification is drawn only when the container's first UDP
// exchange is scheduled later than stock's 250 ms startup grace. It is a
// wall-clock status frame, not output of the scripted host states, so a run
// that contains it is retried rather than compared.
const stockStartupNotification = "Nothing received from server on UDP port"

func runStockMoshClientStable(t *testing.T, states []scriptedState) []byte {
	t.Helper()
	const attempts = 4
	for attempt := 1; attempt <= attempts; attempt++ {
		out := runStockMoshClient(t, startScriptedServer(t, states), 80, 24)
		if !bytes.Contains(out, []byte(stockStartupNotification)) {
			return out
		}
		t.Logf("stock client drew its startup notification on attempt %d; retrying", attempt)
	}
	t.Fatalf("stock client drew its timing-dependent startup notification on all %d attempts", attempts)
	return nil
}

func TestSessionMatchesStockMoshClient(t *testing.T) {
	if os.Getenv("NATCATTY_MOSH_PARITY") == "" {
		t.Skip("set NATCATTY_MOSH_PARITY=1 to compare a Session with stock mosh-client")
	}
	// Both sides run with the variable absent, so the stock title prefix is
	// part of the compared stream regardless of the caller's environment.
	unsetTestEnv(t, "MOSH_TITLE_NOPREFIX")
	states := goldenScriptStates(t)

	stock := runStockMoshClientStable(t, states)
	reference := referenceRendererOutput(t, states)
	if !bytes.Equal(reference, stock) {
		at, got, want := firstDifference(reference, stock)
		t.Fatalf("renderer differs from stock at byte %d (len %d vs %d)\n got: %q\nwant: %q",
			at, len(reference), len(stock), got, want)
	}

	sessionBytes, sizes := sessionOutput(t, states)
	if !bytes.Equal(sessionBytes, stock) {
		at, got, want := firstDifference(sessionBytes, stock)
		t.Fatalf("Session output differs from stock at byte %d (len %d vs %d)\n got: %q\nwant: %q",
			at, len(sessionBytes), len(stock), got, want)
	}
	// Every state that changes the remote size must reach the view's sink, in
	// the same order, and nothing else may. That is the server-authoritative
	// resize path MyGo uses through ResizeByConn.
	wantSizes := expectedScreenSizes(states)
	if fmt.Sprint(sizes) != fmt.Sprint(wantSizes) {
		t.Fatalf("view resize sequence = %v, want %v", sizes, wantSizes)
	}
	if !strings.Contains(string(stock), "\x1b[?1h") {
		t.Fatal("stock output never forced application-cursor-key mode")
	}
}

// TestInProcessTransportOnlyQueuesEncodedOutput is the non-opt-in form of the
// raw-HostBytes rule at the transport seam: the in-process client is fed a
// host string whose escape sequences Display rewrites, and its queue must
// contain only the encoder's canonical output.
func TestInProcessTransportOnlyQueuesEncodedOutput(t *testing.T) {
	raw := "\x1b[999;999H\x1b]52;c;ZGF0YQ==\x1b\\X"
	states := []scriptedState{
		{instructions: []moshcore.HostInstruction{{HostString: []byte(raw)}}},
		{instructions: []moshcore.HostInstruction{{HostString: []byte("END")}}},
	}
	server := startScriptedServer(t, states)
	factory := InProcessFactory{
		Bootstrap:    bannerBootstrap{banner: server.announcement()},
		ProbeTimeout: 2 * time.Second,
	}
	transport, err := factory.Open(context.Background(), inProcessTestSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	type readResult struct {
		data []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		data, readErr := io.ReadAll(transport)
		done <- readResult{data: data, err: readErr}
	}()
	var result readResult
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("transport did not end after the scripted shutdown")
	}
	if result.err != nil {
		t.Fatalf("read transport: %v", result.err)
	}
	out := string(result.data)
	for _, rawOnly := range []string{"\x1b[999;999H", "\x1b]52;c;ZGF0YQ==\x1b\\"} {
		if strings.Contains(out, rawOnly) {
			t.Fatalf("transport queued raw host sequence %q in %q", rawOnly, out)
		}
	}
	if !strings.Contains(out, "\x1b]52;c;ZGF0YQ==\x07") {
		t.Fatalf("transport lost the canonical clipboard sequence: %q", out)
	}
	if !strings.Contains(out, "X") || !strings.Contains(out, "END") {
		t.Fatalf("transport lost visible text: %q", out)
	}
}

func expectedScreenSizes(states []scriptedState) [][2]int {
	width, height := 80, 24
	var sizes [][2]int
	for _, state := range states {
		for _, instruction := range state.instructions {
			if !instruction.Resize {
				continue
			}
			nextWidth, nextHeight := int(instruction.Width), int(instruction.Height)
			if nextWidth == width && nextHeight == height {
				continue
			}
			width, height = nextWidth, nextHeight
			sizes = append(sizes, [2]int{width, height})
		}
	}
	return sizes
}
