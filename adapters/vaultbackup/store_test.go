package vaultbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

type memSecrets struct {
	mu          sync.Mutex
	values      map[string][]byte
	unsupported bool
}

func newMemSecrets() *memSecrets {
	return &memSecrets{values: map[string][]byte{}}
}

func (m *memSecrets) Get(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unsupported {
		return nil, platformsecrets.ErrUnsupported
	}
	value, ok := m.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (m *memSecrets) Set(key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unsupported {
		return platformsecrets.ErrUnsupported
	}
	m.values[key] = append([]byte(nil), value...)
	return nil
}

func (m *memSecrets) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
	return nil
}

func fingerprint(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func testPayload(label string) []byte {
	return []byte(`{"schemaVersion":10,"tables":[{"name":"hosts","rows":[{"id":{"kind":"text","text":"` + label + `"}}]}]}`)
}

func saveInput(id string, createdAt int64, label string) appbackup.SaveInput {
	payload := testPayload(label)
	return appbackup.SaveInput{
		ID:          id,
		CreatedAt:   createdAt,
		Reason:      domainbackup.ReasonVaultChange,
		Fingerprint: fingerprint(payload),
		Payload:     payload,
		Counts:      domainbackup.Counts{Hosts: 1},
	}
}

func openTestStore(t *testing.T, opts ...Option) (*Store, string, *memSecrets) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault-backups")
	keys := newMemSecrets()
	base := []Option{WithRandom(strings.NewReader(strings.Repeat("\x00", 4096)))}
	base = append(base, opts...)
	return Open(dir, keys, base...), dir, keys
}

func TestSaveEncryptsAndRoundTrips(t *testing.T) {
	store, dir, _ := openTestStore(t)
	ctx := context.Background()
	payload := testPayload("h1")
	input := saveInput("backup-1", 1_700_000_000_000, "h1")
	input.SourceAppVersion = "1.2.3"
	input.TargetAppVersion = "1.2.4"

	summary, err := store.Save(ctx, input)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if summary.Fingerprint != input.Fingerprint || summary.SizeBytes != int64(len(payload)) {
		t.Fatalf("summary = %+v", summary)
	}

	readSummary, readPayload, err := store.Read(ctx, "backup-1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(readPayload) != string(payload) {
		t.Fatalf("payload = %q, want %q", readPayload, payload)
	}
	if readSummary.SourceAppVersion != "1.2.3" || readSummary.TargetAppVersion != "1.2.4" {
		t.Fatalf("versions did not round trip: %+v", readSummary)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries, want 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "schemaVersion") ||
		strings.Contains(string(raw), `"text":"h1"`) {
		t.Fatal("the plaintext snapshot leaked into the backup file")
	}
	if !strings.Contains(string(raw), payloadCipher) {
		t.Fatal("the record does not name its payload cipher")
	}
}

func TestAtomicWriteLeavesNoPartialFile(t *testing.T) {
	for _, stage := range []string{"sync", "rename"} {
		t.Run(stage, func(t *testing.T) {
			store, dir, _ := openTestStore(t, WithFault(func(got string) error {
				if got == stage {
					return errors.New("injected " + stage + " failure")
				}
				return nil
			}))
			if _, err := store.Save(context.Background(), saveInput("backup-1", 1_700_000_000_000, "h1")); err == nil {
				t.Fatal("Save hid the injected failure")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), fileSuffix) {
					t.Fatalf("partial final file left behind: %s", entry.Name())
				}
			}
			list, err := store.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 0 {
				t.Fatalf("List after a failed write = %+v", list)
			}
		})
	}
}

func TestReadDetectsTamperedCiphertext(t *testing.T) {
	store, dir, _ := openTestStore(t)
	ctx := context.Background()
	if _, err := store.Save(ctx, saveInput("backup-1", 1_700_000_000_000, "h1")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vault-backup-1700000000000-backup-1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed record
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	parsed.Payload[0] ^= 0xff
	raw, _ = json.Marshal(parsed)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(ctx, "backup-1"); !errors.Is(err, appbackup.ErrCorrupt) {
		t.Fatalf("Read = %v, want ErrCorrupt", err)
	}
}

func TestListSkipsStructurallyInvalidRecords(t *testing.T) {
	store, dir, _ := openTestStore(t)
	if _, err := store.Save(context.Background(), saveInput("backup-1", 1_700_000_000_000, "h1")); err != nil {
		t.Fatal(err)
	}
	bad := record{
		FormatVersion: domainbackup.FormatVersion,
		ID:            "bad id with spaces",
		CreatedAt:     1,
		Reason:        domainbackup.ReasonVaultChange,
		Fingerprint:   strings.Repeat("0", 64),
	}
	raw, _ := json.Marshal(bad)
	if err := os.WriteFile(filepath.Join(dir, "vault-backup-1-bad.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "backup-1" {
		t.Fatalf("List = %+v, want only the valid backup", entries)
	}
}

func TestListSortsNewestFirst(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	for index, id := range []string{"older", "newest", "middle"} {
		createdAt := int64(1_700_000_000_000 + index)
		if _, err := store.Save(ctx, saveInput(id, createdAt, id)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{entries[0].ID, entries[1].ID, entries[2].ID}; got[0] != "middle" ||
		got[1] != "newest" || got[2] != "older" {
		t.Fatalf("order = %v", got)
	}
}

func TestPruneAndClear(t *testing.T) {
	store, dir, _ := openTestStore(t)
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		id := "backup-" + string(rune('a'+index))
		if _, err := store.Save(ctx, saveInput(id, int64(1_700_000_000_000+index), id)); err != nil {
			t.Fatal(err)
		}
	}
	// A limit above the history length must be a no-op, not a slice panic.
	if removed, err := store.Prune(ctx, 5); err != nil || removed != 0 {
		t.Fatalf("Prune above the history length = %d, %v; want 0, nil", removed, err)
	}
	if entries, _ := store.List(ctx); len(entries) != 3 {
		t.Fatalf("Prune above the history length removed entries: %+v", entries)
	}
	removed, err := store.Prune(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("Prune removed %d, want 2", removed)
	}
	entries, _ := store.List(ctx)
	if len(entries) != 1 || entries[0].ID != "backup-c" {
		t.Fatalf("after prune = %+v", entries)
	}

	// A temp file from an interrupted write is removed by Clear; unrelated
	// files are preserved.
	temp := filepath.Join(dir, "vault-backup-99-x.json.tmp-partial")
	if err := os.WriteFile(temp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Clear(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("Clear deleted %d, want the backup and its temp file", deleted)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 || files[0].Name() != "notes.txt" {
		t.Fatalf("directory after clear = %v", files)
	}
}

func TestRetentionSettings(t *testing.T) {
	store, dir, _ := openTestStore(t)
	if got := store.Load(); got != domainbackup.DefaultRetention {
		t.Fatalf("Load without settings = %d", got)
	}
	if err := store.SaveRetention(0); err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got != domainbackup.MinRetention {
		t.Fatalf("Load after clamping 0 = %d", got)
	}
	if err := store.SaveRetention(1000); err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got != domainbackup.MaxRetention {
		t.Fatalf("Load after clamping 1000 = %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, settingsName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got != domainbackup.DefaultRetention {
		t.Fatalf("Load of corrupt settings = %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, settingsName),
		[]byte(`{"formatVersion":99,"retention":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := store.Load(); got != domainbackup.DefaultRetention {
		t.Fatalf("Load of a future settings version = %d", got)
	}
}

func TestUnavailableKeychainRefusesToWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault-backups")
	keys := newMemSecrets()
	keys.unsupported = true
	store := Open(dir, keys, WithRandom(strings.NewReader(strings.Repeat("\x00", 64))))
	if store.EncryptionAvailable() {
		t.Fatal("EncryptionAvailable reported true without a keychain")
	}
	_, err := store.Save(context.Background(), saveInput("backup-1", 1_700_000_000_000, "h1"))
	if !errors.Is(err, appbackup.ErrEncryptionUnavailable) {
		t.Fatalf("Save = %v, want ErrEncryptionUnavailable", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("a plaintext backup was written: %v", entries)
	}
}

func TestSaveValidatesItsInput(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	valid := saveInput("backup-1", 1_700_000_000_000, "h1")

	cases := map[string]func(*appbackup.SaveInput){
		"id":          func(input *appbackup.SaveInput) { input.ID = "../escape" },
		"fingerprint": func(input *appbackup.SaveInput) { input.Fingerprint = "abc" },
		"created at":  func(input *appbackup.SaveInput) { input.CreatedAt = 0 },
		"reason":      func(input *appbackup.SaveInput) { input.Reason = "manual" },
		"size":        func(input *appbackup.SaveInput) { input.Payload = make([]byte, domainbackup.MaxPayloadBytes+1) },
	}
	for name, mutate := range cases {
		input := valid
		mutate(&input)
		if _, err := store.Save(ctx, input); err == nil {
			t.Errorf("%s: Save accepted invalid input", name)
		}
	}
}

func TestListToleratesAMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	store := Open(dir, newMemSecrets())
	entries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v", entries)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("List did not create the backup directory")
	}
}

func TestPruneUsesCreatedAtNotFileName(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	// IDs sort differently from timestamps: lexical id order would keep the
	// wrong record.
	if _, err := store.Save(ctx, saveInput("z-old", 1, "old")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, saveInput("a-new", 2, "new")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prune(ctx, 1); err != nil {
		t.Fatal(err)
	}
	entries, _ := store.List(ctx)
	if len(entries) != 1 || entries[0].ID != "a-new" {
		t.Fatalf("kept %+v", entries)
	}
}
