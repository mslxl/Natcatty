package webdav_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/webdav"
	"github.com/mslxl/natcatty/adapters/webdav/fake"
	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type integrationConfigs struct {
	mu     sync.Mutex
	config appsync.Config
}

func (c *integrationConfigs) Load(context.Context) (appsync.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config, nil
}

func (c *integrationConfigs) Save(_ context.Context, config appsync.Config) error {
	c.mu.Lock()
	c.config = config
	c.mu.Unlock()
	return nil
}

type integrationTokens struct {
	mu    sync.Mutex
	token domainsync.Token
	set   bool
}

func (t *integrationTokens) Load(context.Context) (domainsync.Token, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.set {
		return domainsync.Token{}, appsync.ErrNoToken
	}
	return t.token, nil
}

func (t *integrationTokens) Save(_ context.Context, token domainsync.Token) error {
	t.mu.Lock()
	t.token = token
	t.set = true
	t.mu.Unlock()
	return nil
}

func (t *integrationTokens) Clear(context.Context) error {
	t.mu.Lock()
	t.token = domainsync.Token{}
	t.set = false
	t.mu.Unlock()
	return nil
}

type integrationVault struct {
	mu      sync.Mutex
	payload appsync.Payload
	applied int
}

func (v *integrationVault) Snapshot(context.Context) (appsync.Payload, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return cloneIntegrationPayload(v.payload), nil
}

func (v *integrationVault) Apply(_ context.Context, payload appsync.Payload) error {
	v.mu.Lock()
	v.payload = cloneIntegrationPayload(payload)
	v.applied++
	v.mu.Unlock()
	return nil
}

func cloneIntegrationPayload(payload appsync.Payload) appsync.Payload {
	clone := payload
	clone.Hosts = append([]domainvault.Host(nil), payload.Hosts...)
	return clone
}

func integrationPayload(label string, syncedAt int64) appsync.Payload {
	return appsync.Payload{
		Hosts: []domainvault.Host{{
			ID: "host-1", Label: label, Hostname: "example.test", Port: 22, Protocol: domainvault.ProtocolSSH,
		}},
		SyncedAt: syncedAt,
	}
}

func newIntegrationService(t *testing.T, client *webdav.Client, vault appsync.Vault, configs appsync.ConfigStore, tokens appsync.TokenStore) *appsync.Service {
	t.Helper()
	service := appsync.NewService(client, vault, configs, tokens,
		appsync.WithRemote(appsync.ProviderWebDAV, client),
		appsync.WithClock(func() time.Time { return time.UnixMilli(5000) }),
	)
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return service
}

func TestWebDAVEndToEndThroughTheRealAdapter(t *testing.T) {
	server, err := fake.Start()
	if err != nil {
		t.Fatalf("start fake: %v", err)
	}
	defer server.Close()
	client := webdav.New(webdav.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	configs := &integrationConfigs{}
	tokens := &integrationTokens{}
	vault := &integrationVault{payload: integrationPayload("laptop-1", 1000)}
	service := newIntegrationService(t, client, vault, configs, tokens)
	settings := appsync.WebDAVSettings{
		Endpoint: server.CollectionURL(), AuthType: domainsync.WebDAVAuthBasic,
		Username: "dav-user", Password: "dav-secret",
	}

	if err := service.ConnectWebDAV(ctx, "master-password", settings, false); err != nil {
		if !errors.Is(err, appsync.ErrInsecureEndpoint) {
			t.Fatalf("unconfirmed connect = %v", err)
		}
	} else {
		t.Fatal("plain HTTP connect succeeded without confirmation")
	}
	if tokens.set {
		t.Fatal("credential stored before the insecure confirmation")
	}
	if _, exists := server.FileContent("/dav", domainsync.SyncFileName); exists {
		t.Fatal("a file was written before the insecure confirmation")
	}

	if err := service.ConnectWebDAV(ctx, "master-password", settings, true); err != nil {
		t.Fatalf("confirmed connect: %v", err)
	}
	tokens.mu.Lock()
	stored := tokens.token
	tokens.mu.Unlock()
	if stored.Provider != appsync.ProviderWebDAV || stored.WebDAV == nil ||
		stored.WebDAV.Password != "dav-secret" {
		t.Fatalf("stored credential = %+v", stored)
	}
	configs.mu.Lock()
	encodedConfig, _ := json.Marshal(configs.config)
	configs.mu.Unlock()
	if strings.Contains(string(encodedConfig), "dav-secret") {
		t.Fatalf("plaintext config leaked the password: %s", encodedConfig)
	}

	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		t.Fatalf("first manual sync: %v", err)
	}
	content, exists := server.FileContent("/dav", domainsync.SyncFileName)
	if !exists {
		t.Fatal("the first sync did not create netcatty-vault.json")
	}
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		t.Fatalf("uploaded file is not an envelope: %v", err)
	}
	plaintext, err := domainsync.DecryptPayload(envelope, "master-password")
	if err != nil {
		t.Fatalf("decrypt uploaded file: %v", err)
	}
	var uploaded appsync.Payload
	if err := json.Unmarshal(plaintext, &uploaded); err != nil {
		t.Fatalf("decode uploaded payload: %v", err)
	}
	if len(uploaded.Hosts) != 1 || uploaded.Hosts[0].Label != "laptop-1" {
		t.Fatalf("uploaded payload = %+v", uploaded)
	}

	// A second process-like service restores the credential and profile. Its
	// older local copy must download the newer cloud copy.
	restored := configs.Config()
	restored.LocalSignature = ""
	restored.RemoteETag = ""
	restored.RemoteID = ""
	restored.RemoteUpdatedAt = 0
	configs2 := &integrationConfigs{config: restored}
	vault2 := &integrationVault{payload: integrationPayload("laptop-2", 500)}
	service2 := newIntegrationService(t, client, vault2, configs2, tokens)
	if err := service2.SetPassword("master-password"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	requestsBefore := server.State().Counters.Requests
	if _, err := service2.Sync(ctx, appsync.TriggerManual); !errors.Is(err, appsync.ErrInsecureEndpoint) {
		t.Fatalf("restored manual sync without confirmation = %v", err)
	}
	if requests := server.State().Counters.Requests; requests != requestsBefore {
		t.Fatalf("restored sync sent %d requests before confirmation", requests-requestsBefore)
	}
	if !service2.ConfirmInsecureEndpoint(ctx) {
		t.Fatal("restored insecure endpoint was not confirmable")
	}
	if result, err := service2.Sync(ctx, appsync.TriggerManual); err != nil {
		t.Fatalf("restored sync: %v", err)
	} else if !result.Downloaded {
		t.Fatalf("restored sync did not download: %+v", result)
	}
	vault2.mu.Lock()
	downloaded := cloneIntegrationPayload(vault2.payload)
	vault2.mu.Unlock()
	if len(downloaded.Hosts) != 1 || downloaded.Hosts[0].Label != "laptop-1" {
		t.Fatalf("downloaded payload = %+v", downloaded)
	}

	// A newer local edit uploads through the same restored profile.
	vault2.mu.Lock()
	vault2.payload = integrationPayload("laptop-2-edited", 9000)
	vault2.mu.Unlock()
	if _, err := service2.Sync(ctx, appsync.TriggerManual); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	content, exists = server.FileContent("/dav", domainsync.SyncFileName)
	if !exists {
		t.Fatal("cloud file disappeared")
	}
	envelope, _ = domainsync.UnmarshalFile(content)
	plaintext, err = domainsync.DecryptPayload(envelope, "master-password")
	if err != nil {
		t.Fatalf("decrypt second upload: %v", err)
	}
	if err := json.Unmarshal(plaintext, &uploaded); err != nil {
		t.Fatalf("decode second upload: %v", err)
	}
	if len(uploaded.Hosts) != 1 || uploaded.Hosts[0].Label != "laptop-2-edited" {
		t.Fatalf("second uploaded payload = %+v", uploaded)
	}
	state := server.State()
	if state.Counters.BasicSuccesses == 0 || state.Counters.Puts < 2 {
		t.Fatalf("counters = %+v", state.Counters)
	}
	if state.Counters.SecretViolations != 0 {
		t.Fatalf("secret violations = %d", state.Counters.SecretViolations)
	}
}

// Config returns the stored profile, for test setup only.
func (c *integrationConfigs) Config() appsync.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config
}
