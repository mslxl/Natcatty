package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func newFileStore(t *testing.T) (*FileStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile", "secrets.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	return store, path
}

func TestFileStoreRoundTrip(t *testing.T) {
	store, _ := newFileStore(t)
	if _, err := store.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
	want := []byte("field-key")
	if err := store.Set("field", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get("field")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Get = %q, want %q", got, want)
	}
	if err := store.Delete("field"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get("field"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestFileStoreDeleteMissingIsNotAnError(t *testing.T) {
	store, _ := newFileStore(t)
	if err := store.Delete("missing"); err != nil {
		t.Fatalf("Delete missing = %v, want nil", err)
	}
}

func TestFileStorePersistsAcrossReopen(t *testing.T) {
	store, path := newFileStore(t)
	if err := store.Set("field", []byte("key")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	got, err := reopened.Get("field")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if string(got) != "key" {
		t.Fatalf("Get after reopen = %q, want %q", got, "key")
	}
}

func TestFileStoreReturnsCopies(t *testing.T) {
	store, _ := newFileStore(t)
	value := []byte("secret")
	if err := store.Set("field", value); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value[0] = 'X'
	got, err := store.Get("field")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("Set kept a reference to the caller's slice: %q", got)
	}
	got[0] = 'Y'
	again, err := store.Get("field")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(again) != "secret" {
		t.Fatalf("Get returned a shared slice: %q", again)
	}
}

func TestFileStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not express permissions as mode bits")
	}
	store, path := newFileStore(t)
	if err := store.Set("field", []byte("key")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("secret file mode = %04o, want 0600", perm)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Fatalf("secret directory mode = %04o, want 0700", perm)
	}
}

func TestFileStoreRejectsAnInsecureFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not express permissions as mode bits")
	}
	path := filepath.Join(t.TempDir(), "secrets.json")
	body := []byte(`{"version":1,"entries":{}}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if _, err := OpenFileStore(path); err == nil {
		t.Fatal("OpenFileStore accepted a file other users can read")
	}
}

func TestFileStoreRejectsACorruptFile(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":     `{"version":1,"entries":`,
		"wrong-version": `{"version":2,"entries":{}}`,
		"bad-base64":    `{"version":1,"entries":{"field":"!!"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secrets.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := OpenFileStore(path); err == nil {
				t.Fatal("OpenFileStore accepted a corrupt file")
			}
		})
	}
}

func TestFileStoreLimits(t *testing.T) {
	store, _ := newFileStore(t)
	if err := store.Set("too-big", make([]byte, fileStoreMaxValue+1)); err == nil {
		t.Fatal("Set accepted a value over the per-value cap")
	}
}

func TestFileStoreRejectsTooManyEntries(t *testing.T) {
	store, _ := newFileStore(t)
	for i := 0; i < fileStoreMaxEntries; i++ {
		key := string(rune('a'+i%26)) + string(rune('a'+i/26)) + string(rune('a'+i/676))
		if err := store.Set(key, []byte("v")); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	if err := store.Set("overflow", []byte("v")); err == nil {
		t.Fatal("Set accepted an entry over the entry cap")
	}
}

func TestFileStoreRejectsTooMuchTotalData(t *testing.T) {
	store, _ := newFileStore(t)
	chunk := make([]byte, fileStoreMaxValue)
	for i := 0; i < fileStoreMaxTotal/fileStoreMaxValue; i++ {
		key := "chunk-" + string(rune('a'+i))
		if err := store.Set(key, chunk); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	if err := store.Set("overflow", []byte("v")); err == nil {
		t.Fatal("Set accepted data over the total cap")
	}
}

func TestFileStoreLeavesNoTemporaryFile(t *testing.T) {
	store, path := newFileStore(t)
	if err := store.Set("field", []byte("key")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".secrets-") {
			t.Fatalf("temporary file %q was left behind", entry.Name())
		}
	}
}

func TestFileStoreConcurrentAccess(t *testing.T) {
	store, _ := newFileStore(t)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			key := "key-" + string(rune('a'+i))
			for n := 0; n < 20; n++ {
				if err := store.Set(key, []byte("value")); err != nil {
					t.Errorf("Set: %v", err)
					return
				}
				if _, err := store.Get(key); err != nil {
					t.Errorf("Get: %v", err)
					return
				}
			}
			if err := store.Delete(key); err != nil {
				t.Errorf("Delete: %v", err)
			}
		}(i)
	}
	group.Wait()
}
