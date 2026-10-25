package syncstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud-sync.json")
	store := OpenConfig(path)
	config, err := store.Load(context.Background())
	if err != nil || config.Connected {
		t.Fatalf("empty Load = %+v, %v", config, err)
	}
	want := appsync.Config{
		Provider: "github", Connected: true, Login: "octocat",
		AutoSync: true, Strategy: domainsync.StrategyLocal,
		DeviceID: "d", DeviceName: "n", AppVersion: "v",
		LastSyncedAt: 5, RemoteID: "gist-1", RemoteETag: "etag", RemoteUpdatedAt: 4,
		LocalSignature: "sig",
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("config mode = %v, want owner-only", info.Mode().Perm())
		}
	}
}

func TestMalformedConfigIsTheUnconfiguredState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud-sync.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := OpenConfig(path).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.Connected {
		t.Fatalf("config = %+v", config)
	}
}

type memorySecrets struct {
	values map[string][]byte
}

func (m *memorySecrets) Get(key string) ([]byte, error) {
	value, ok := m.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return value, nil
}

func (m *memorySecrets) Set(key string, value []byte) error {
	m.values[key] = append([]byte(nil), value...)
	return nil
}

func (m *memorySecrets) Delete(key string) error {
	if _, ok := m.values[key]; !ok {
		return platformsecrets.ErrNotFound
	}
	delete(m.values, key)
	return nil
}

func TestTokenStoreRoundTrip(t *testing.T) {
	store := NewTokenStore(&memorySecrets{values: map[string][]byte{}})
	if _, err := store.Load(context.Background()); !errors.Is(err, appsync.ErrNoToken) {
		t.Fatalf("empty Load error = %v", err)
	}
	token := domainsync.Token{AccessToken: "token", RefreshToken: "refresh", Scope: "gist"}
	if err := store.Save(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil || got != token {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, appsync.ErrNoToken) {
		t.Fatalf("after Clear error = %v", err)
	}
	if err := store.Clear(context.Background()); err != nil {
		t.Fatalf("clearing a missing token = %v", err)
	}
}

func TestCorruptTokenIsTreatedAsMissing(t *testing.T) {
	secrets := &memorySecrets{values: map[string][]byte{tokenKey: []byte("{")}}
	store := NewTokenStore(secrets)
	if _, err := store.Load(context.Background()); !errors.Is(err, appsync.ErrNoToken) {
		t.Fatalf("error = %v", err)
	}
}
