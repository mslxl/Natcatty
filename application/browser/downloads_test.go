package browser

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type testTunnel struct {
	dial net.Dialer
}

func (t *testTunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return t.dial.DialContext(ctx, network, address)
}
func (t *testTunnel) Done() <-chan struct{} { return nil }
func (t *testTunnel) Discard()              {}

func newDownloadTestManager(t *testing.T, origin string, opts DownloadOptions) (*downloadManager, string) {
	t.Helper()
	if opts.Dir == nil {
		opts.Dir = func() (string, error) { return t.TempDir(), nil }
	}
	gw := newGateway(&testTunnel{})
	t.Cleanup(gw.Close)
	gatewayBase, err := gw.URLFor(origin + "/")
	if err != nil {
		t.Fatalf("URLFor: %v", err)
	}
	opts.Validate = func(gatewayURL string) error {
		if _, ok := gw.RemoteURL(gatewayURL); !ok {
			return ErrDownloadURL
		}
		return nil
	}
	manager := newDownloadManager(opts, func(ctx context.Context, gatewayURL, cookies string) (*http.Response, error) {
		return gw.openDownload(ctx, gatewayURL, cookies)
	})
	t.Cleanup(manager.Close)
	return manager, strings.TrimSuffix(gatewayBase, "/")
}

func TestDownloadWritesBytesInsideDestination(t *testing.T) {
	payload := bytes.Repeat([]byte("netcatty-download-"), 4096)
	mux := http.NewServeMux()
	mux.HandleFunc("/files/report.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	})
	origin := httptest.NewServer(mux)
	defer origin.Close()
	dir := t.TempDir()
	manager, base := newDownloadTestManager(t, origin.URL, DownloadOptions{
		Dir: func() (string, error) { return dir, nil },
	})
	item, err := manager.Start(base+"/files/report.bin", "../../escape.bin")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.Wait()
	final := manager.Get(item.ID)
	if final.Status != DownloadCompleted {
		t.Fatalf("status = %q (%s), want completed", final.Status, final.Failure)
	}
	if final.Received != int64(len(payload)) || final.Total != int64(len(payload)) {
		t.Fatalf("bytes = %d/%d, want %d", final.Received, final.Total, len(payload))
	}
	if filepath.Dir(final.SavePath) != filepath.Clean(dir) {
		t.Fatalf("SavePath %q escaped %q", final.SavePath, dir)
	}
	if filepath.Base(final.SavePath) != "escape.bin" {
		t.Fatalf("filename = %q, want sanitized escape.bin", filepath.Base(final.SavePath))
	}
	got, err := os.ReadFile(final.SavePath)
	if err != nil {
		t.Fatalf("read completed file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("bytes differ: got %d, want %d", len(got), len(payload))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory entries = %v, want only the completed file", entries)
	}
}

func TestDownloadRefusesOversizeAndLeavesNoFile(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write(make([]byte, 1000))
	}))
	defer origin.Close()
	dir := t.TempDir()
	manager, base := newDownloadTestManager(t, origin.URL, DownloadOptions{
		Dir:      func() (string, error) { return dir, nil },
		MaxBytes: 100,
	})
	item, err := manager.Start(base+"/big.bin", "big.bin")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.Wait()
	final := manager.Get(item.ID)
	if final.Status != DownloadFailed || final.Failure != DownloadFailureTooLarge {
		t.Fatalf("status = %q/%q, want failed/too_large", final.Status, final.Failure)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("partial files left: %v", entries)
	}
}

func TestDownloadStreamingOverflowIsCleaned(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 100))
			flusher.Flush()
		}
	}))
	defer origin.Close()
	dir := t.TempDir()
	manager, base := newDownloadTestManager(t, origin.URL, DownloadOptions{
		Dir:      func() (string, error) { return dir, nil },
		MaxBytes: 250,
	})
	item, err := manager.Start(base+"/stream.bin", "stream.bin")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.Wait()
	final := manager.Get(item.ID)
	if final.Status != DownloadFailed || final.Failure != DownloadFailureTooLarge {
		t.Fatalf("status = %q/%q, want failed/too_large", final.Status, final.Failure)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("partial files left: %v", entries)
	}
}

func TestDownloadCancelRemovesPartFile(t *testing.T) {
	started := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 32))
		flusher.Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer origin.Close()
	dir := t.TempDir()
	manager, base := newDownloadTestManager(t, origin.URL, DownloadOptions{
		Dir: func() (string, error) { return dir, nil },
	})
	item, err := manager.Start(base+"/slow.bin", "slow.bin")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("origin never received the download")
	}
	if !manager.Cancel(item.ID) {
		t.Fatal("Cancel reported false")
	}
	manager.Wait()
	final := manager.Get(item.ID)
	if final.Status != DownloadCancelled {
		t.Fatalf("status = %q (%s), want cancelled", final.Status, final.Failure)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("partial files left after cancel: %v", entries)
	}
}

func TestDownloadFailureIsClean(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer origin.Close()
	dir := t.TempDir()
	manager, base := newDownloadTestManager(t, origin.URL, DownloadOptions{
		Dir: func() (string, error) { return dir, nil },
	})
	item, err := manager.Start(base+"/broken.bin", "broken.bin")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.Wait()
	final := manager.Get(item.ID)
	if final.Status != DownloadFailed {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("partial files left after failure: %v", entries)
	}
}

func TestDownloadRedirectsStayOnTheRouteOrigin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "payload")
	})
	mux.HandleFunc("/leave", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.2:9/steal", http.StatusFound)
	})
	origin := httptest.NewServer(mux)
	defer origin.Close()
	tunnel := newRoutedTunnel(strings.TrimPrefix(origin.URL, "http://"))
	gw := newGateway(tunnel)
	defer gw.Close()

	sameOrigin, err := gw.URLFor(origin.URL + "/hop")
	if err != nil {
		t.Fatal(err)
	}
	response, err := gw.openDownload(context.Background(), sameOrigin, "")
	if err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "payload" {
		t.Fatalf("same-origin redirect body = %q, want payload", body)
	}

	offRoute, err := gw.URLFor(origin.URL + "/leave")
	if err != nil {
		t.Fatal(err)
	}
	response, err = gw.openDownload(context.Background(), offRoute, "")
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, ErrDownloadURL) {
		t.Fatalf("off-route redirect error = %v, want ErrDownloadURL", err)
	}
}

func TestDownloadRefusesNonGatewayURL(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer origin.Close()
	manager, _ := newDownloadTestManager(t, origin.URL, DownloadOptions{})
	if _, err := manager.Start(origin.URL+"/not-a-gateway.bin", "x.bin"); !errors.Is(err, ErrDownloadURL) {
		t.Fatalf("error = %v, want ErrDownloadURL", err)
	}
}

func TestDownloadUniqueNamesAndMarker(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, strings.NewReader("same"))
	}))
	defer origin.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, base := newDownloadTestManager(t, origin.URL, DownloadOptions{
		Dir: func() (string, error) { return dir, nil },
	})
	item, err := manager.Start(base+"/same.txt", "same.txt")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.Wait()
	final := manager.Get(item.ID)
	if filepath.Base(final.SavePath) != "same (1).txt" {
		t.Fatalf("SavePath = %q, want unique numbered name", final.SavePath)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "same.txt"))
	if string(old) != "old" {
		t.Fatalf("existing file overwritten: %q", old)
	}
	if _, err := os.Stat(final.SavePath + ".part"); !os.IsNotExist(err) {
		t.Fatalf("part marker still present: %v", err)
	}
}

func TestDownloadManagerLimitsAndClear(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()
	manager, base := newDownloadTestManager(t, origin.URL, DownloadOptions{})
	item, err := manager.Start(base+"/one.txt", "one.txt")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	manager.Wait()
	if !manager.Remove(item.ID) {
		t.Fatal("Remove completed download = false")
	}
	if got := manager.Get(item.ID); got.ID != "" {
		t.Fatalf("removed download still present: %+v", got)
	}
}
