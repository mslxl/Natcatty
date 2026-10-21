package vault

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

// fakeImportStore applies one batch atomically to the in-memory repositories.
type fakeImportStore struct {
	hosts   *fakeHosts
	groups  *fakeGroups
	err     error
	batches int
}

func (f *fakeImportStore) SaveImportBatch(_ context.Context, batch ImportBatch) error {
	if f.err != nil {
		return f.err
	}
	f.batches++
	for _, group := range batch.Groups {
		if err := f.groups.SaveGroup(context.Background(), group); err != nil {
			return err
		}
	}
	for _, host := range batch.Hosts {
		if err := f.hosts.Save(context.Background(), host); err != nil {
			return err
		}
	}
	return nil
}

func newImportService(t *testing.T) (*Service, *fakeHosts, *fakeGroups, *fakeSecrets, *fakeImportStore) {
	t.Helper()
	hosts := &fakeHosts{}
	groups := &fakeGroups{}
	secrets := newFakeSecrets()
	store := &fakeImportStore{hosts: hosts, groups: groups}
	var hostIDs int
	service := NewService(hosts,
		WithGroups(groups),
		WithSecrets(secrets),
		WithImportStore(store),
		WithIDGenerator(func() model.HostID {
			hostIDs++
			return model.HostID("imported-" + string(rune('0'+hostIDs)))
		}),
	)
	return service, hosts, groups, secrets, store
}

func importCSV() string {
	longLabel := strings.Repeat("x", 300)
	return "\xef\xbb\xbf" +
		"Groups,Label,Hostname,Protocol,Port,Username,Password\r\n" +
		"Prod/Web,web-1,web1.example.test,ssh,2222,deploy,new-secret\r\n" +
		"Prod/Web,web-2,web2.example.test,ssh,22,root,\r\n" +
		"Prod/Web,web-1 copy,web1.example.test,ssh,2222,deploy,\r\n" +
		"Lab," + longLabel + ",bad.example.test,ssh,22,root,\r\n"
}

func seedConflict(t *testing.T, service *Service) domainvault.Host {
	t.Helper()
	host, err := service.Create(context.Background(), domainvault.Draft{
		Label: "existing", Hostname: "web1.example.test", Port: 2222,
		Username: "deploy", Group: "Prod/Web", Password: "old-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func TestPreviewImportReportsConflictsDuplicatesAndInvalidRows(t *testing.T) {
	service, hosts, _, _, _ := newImportService(t)
	seedConflict(t, service)

	preview, err := service.PreviewImport(context.Background(), importCSV(), PreviewImportOptions{FileName: "hosts.csv"})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if preview.Format != vaultimport.FormatCSV {
		t.Fatalf("format = %q", preview.Format)
	}
	if len(preview.Rows) != 4 {
		t.Fatalf("rows = %d, want 4: %+v", len(preview.Rows), preview.Rows)
	}
	wantStatus := []ImportStatus{
		ImportStatusConflict, ImportStatusNew, ImportStatusDuplicate, ImportStatusInvalid,
	}
	for index, want := range wantStatus {
		if preview.Rows[index].Status != want {
			t.Errorf("row %d status = %q, want %q", index, preview.Rows[index].Status, want)
		}
	}
	if preview.Counts != (ImportCounts{Total: 4, New: 1, Conflicts: 1, Duplicates: 1, Invalid: 1, Selected: 2}) {
		t.Fatalf("counts = %+v", preview.Counts)
	}
	if preview.Rows[0].Existing == "" || preview.Rows[0].ExistingLabel != "existing" {
		t.Fatalf("conflict row = %+v", preview.Rows[0])
	}
	if preview.Rows[2].Selected || preview.Rows[3].Selected {
		t.Fatal("duplicate and invalid rows must start unselected")
	}
	if len(preview.Rows[3].Issues) == 0 {
		t.Fatal("invalid row must carry a localized-renderable issue")
	}
	if !containsString(preview.GroupsToCreate, "Prod") ||
		!containsString(preview.GroupsToCreate, "Prod/Web") {
		t.Fatalf("groups to create = %v", preview.GroupsToCreate)
	}
	if len(hosts.hosts) != 1 {
		t.Fatalf("preview wrote Hosts: %d", len(hosts.hosts))
	}
}

func TestCommitImportIsChunkedAndPersistsThroughSecrets(t *testing.T) {
	service, hosts, groups, secrets, store := newImportService(t)
	seedConflict(t, service)
	preview, err := service.PreviewImport(context.Background(), importCSV(), PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{
		Merge: ImportMergeOverwrite, Limit: 2,
	})
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if first.Done || first.Next != 2 {
		t.Fatalf("first result = %+v, want Next 2 and Done false", first)
	}
	if len(first.Imported) != 1 || len(first.Overwritten) != 1 {
		t.Fatalf("first result = %+v, want one import and one overwrite", first)
	}
	if !containsString(first.GroupsCreated, "Prod") || !containsString(first.GroupsCreated, "Prod/Web") {
		t.Fatalf("groups created = %v", first.GroupsCreated)
	}
	if store.batches != 1 {
		t.Fatalf("batches = %d, want 1", store.batches)
	}

	second, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{
		Merge: ImportMergeOverwrite, Offset: first.Next, Limit: 2,
	})
	if err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	if !second.Done || second.Next != 4 || second.Skipped != 2 {
		t.Fatalf("second result = %+v", second)
	}
	if len(hosts.hosts) != 2 || len(groups.items) != 2 {
		t.Fatalf("hosts = %d, groups = %d", len(hosts.hosts), len(groups.items))
	}

	var imported *domainvault.Host
	for index := range hosts.hosts {
		if hosts.hosts[index].Hostname == "web2.example.test" {
			imported = &hosts.hosts[index]
		}
	}
	if imported == nil {
		t.Fatal("web2 was not imported")
	}
	if imported.Group != "Prod/Web" || imported.Port != 22 || imported.Username != "root" {
		t.Fatalf("imported = %+v", imported)
	}
	if imported.HasPassword {
		t.Fatal("web2 has no password")
	}

	overwritten := hosts.hosts[0]
	if overwritten.Label != "web-1" || overwritten.MergeKey() != imported.MergeKey() && overwritten.Hostname != "web1.example.test" {
		t.Fatalf("overwritten = %+v", overwritten)
	}
	if !overwritten.HasPassword {
		t.Fatal("overwritten Host must advertise its password")
	}
	secret, err := secrets.Get(context.Background(), HostSecretRef(overwritten.ID, FieldPassword))
	if err != nil || secret != "new-secret" {
		t.Fatalf("stored password = %q, %v", secret, err)
	}
	if _, err := service.Get(context.Background(), imported.ID); err != nil {
		t.Fatalf("Get imported Host: %v", err)
	}
}

// fakeImportFiles serves the key material an IdentityFile reference points at.
type fakeImportFiles map[string]string

func (f fakeImportFiles) ReadImportFile(path string) ([]byte, error) {
	value, ok := f[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(value), nil
}

// keyImportService wires the key inspector, key store, and file reader the
// external-tool import path uses.
func keyImportService(t *testing.T, hosts *fakeHosts, keys *fakeKeys, secrets *fakeSecrets,
	files ImportFileReader) (*Service, *fakeImportStore) {

	t.Helper()
	groups := &fakeGroups{}
	store := &fakeImportStore{hosts: hosts, groups: groups}
	var hostID int
	var keyID int
	service := NewService(hosts,
		WithGroups(groups),
		WithKeys(keys),
		WithSecrets(secrets),
		WithKeyInspector(&fakeInspector{}),
		WithImportStore(store),
		WithImportFiles(files),
		WithIDGenerator(func() model.HostID {
			hostID++
			return model.HostID("import-host-" + string(rune('0'+hostID)))
		}),
		WithKeyIDGenerator(func() model.KeyID {
			keyID++
			return model.KeyID("import-key-" + string(rune('0'+keyID)))
		}),
	)
	return service, store
}

func TestCommitImportCreatesOneKeyPerIdentityFileAcrossChunks(t *testing.T) {
	hosts := &fakeHosts{}
	keys := &fakeKeys{}
	secrets := newFakeSecrets()
	service, _ := keyImportService(t, hosts, keys, secrets,
		fakeImportFiles{"/keys/id_test": "PRIVATE-KEY-MATERIAL"})
	text := "Hostname,Label,Username,KeyPath\n" +
		"one.example.test,one,root,/keys/id_test\n" +
		"two.example.test,two,root,/keys/id_test\n"
	preview, err := service.PreviewImport(context.Background(), text, PreviewImportOptions{})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if len(preview.keyFiles) != 1 {
		t.Fatalf("key files = %d, want 1", len(preview.keyFiles))
	}

	first, err := service.CommitImport(context.Background(), preview,
		ImportCommitOptions{Limit: 1})
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if len(keys.keys) != 1 {
		t.Fatalf("first chunk created %d Keys, want 1", len(keys.keys))
	}
	second, err := service.CommitImport(context.Background(), preview,
		ImportCommitOptions{Offset: first.Next, Limit: 1})
	if err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	if !second.Done {
		t.Fatalf("second chunk = %+v, want Done", second)
	}
	if len(keys.keys) != 1 {
		t.Fatalf("chunked import created %d Keys for one file, want 1", len(keys.keys))
	}
	if len(hosts.hosts) != 2 {
		t.Fatalf("hosts = %d, want 2", len(hosts.hosts))
	}
	for _, host := range hosts.hosts {
		if host.KeyID != keys.keys[0].ID {
			t.Fatalf("Host %s KeyID = %q, want %q", host.Hostname, host.KeyID, keys.keys[0].ID)
		}
	}
}

func TestCommitImportSkipConflictDoesNotCreateKey(t *testing.T) {
	hosts := &fakeHosts{}
	keys := &fakeKeys{}
	secrets := newFakeSecrets()
	service, _ := keyImportService(t, hosts, keys, secrets,
		fakeImportFiles{"/keys/id_test": "PRIVATE-KEY-MATERIAL"})
	existing, err := service.Create(context.Background(), domainvault.Draft{
		Label: "existing", Hostname: "web1.example.test", Port: 22, Username: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := "Hostname,Port,Username,KeyPath\nweb1.example.test,22,root,/keys/id_test\n"
	preview, err := service.PreviewImport(context.Background(), text, PreviewImportOptions{})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if preview.Counts.Conflicts != 1 {
		t.Fatalf("counts = %+v, want one conflict", preview.Counts)
	}
	result, err := service.CommitImport(context.Background(), preview,
		ImportCommitOptions{Merge: ImportMergeSkip})
	if err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	if result.Skipped != 1 || len(result.Imported) != 0 {
		t.Fatalf("result = %+v, want one skipped row", result)
	}
	if len(keys.keys) != 0 {
		t.Fatalf("skipped conflict created %d Keys", len(keys.keys))
	}
	unchanged, err := service.Get(context.Background(), existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.KeyID != "" {
		t.Fatalf("skipped conflict changed KeyID to %q", unchanged.KeyID)
	}
}

func TestCommitImportNeverWritesPlaintextPassword(t *testing.T) {
	service, hosts, _, secrets, _ := newImportService(t)
	text := "Hostname,Label,Password\nplain.example.test,plain,top-secret\n"
	preview, err := service.PreviewImport(context.Background(), text, PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 1 {
		t.Fatalf("result = %+v", result)
	}
	host := hosts.hosts[0]
	data, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "top-secret") {
		t.Fatalf("Host JSON leaked a password: %s", data)
	}
	if !host.HasPassword {
		t.Fatal("Host must advertise the encrypted password")
	}
	if secrets.values[HostSecretRef(host.ID, FieldPassword)] != "top-secret" {
		t.Fatalf("secret store = %v", secrets.values)
	}
}

func TestCommitImportRollsBackSecretsWhenTheBatchFails(t *testing.T) {
	service, hosts, _, secrets, store := newImportService(t)
	store.err = errors.New("disk full")
	preview, err := service.PreviewImport(context.Background(), importCSV(), PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CommitImport(context.Background(), preview, ImportCommitOptions{
		Merge: ImportMergeOverwrite,
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindInternal {
		t.Fatalf("commit error = %v, want internal", err)
	}
	if len(secrets.values) != 0 {
		t.Fatalf("secrets written despite a failed batch: %v", secrets.values)
	}
	if len(hosts.hosts) != 0 {
		t.Fatalf("hosts written despite a failed batch: %+v", hosts.hosts)
	}

	// The same offset resumes cleanly once the store recovers.
	store.err = nil
	result, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{
		Merge: ImportMergeOverwrite, Limit: 1,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(result.Imported) != 1 || len(hosts.hosts) != 1 {
		t.Fatalf("resume result = %+v hosts=%+v", result, hosts.hosts)
	}
}

func TestCommitImportSkipsConflictsByDefault(t *testing.T) {
	service, hosts, _, _, _ := newImportService(t)
	existing := seedConflict(t, service)
	preview, err := service.PreviewImport(context.Background(), importCSV(), PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 3 {
		t.Fatalf("skipped = %d, want conflict + duplicate + invalid", result.Skipped)
	}
	for _, host := range hosts.hosts {
		if host.ID == existing.ID && host.Label != "existing" {
			t.Fatalf("conflict overwritten without permission: %+v", host)
		}
	}
}

func TestPreviewImportRejectsUnsupportedFormat(t *testing.T) {
	service, _, _, _, _ := newImportService(t)
	_, err := service.PreviewImport(context.Background(), "<html>", PreviewImportOptions{})
	if err == nil {
		t.Fatal("expected an error for an undetectable document")
	}
}

func TestCommitImportRequiresTheBatchStore(t *testing.T) {
	hosts := &fakeHosts{}
	secrets := newFakeSecrets()
	service := NewService(hosts, WithSecrets(secrets))
	preview, err := service.PreviewImport(context.Background(), "Hostname\nweb.example.test\n", PreviewImportOptions{Format: vaultimport.FormatCSV})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CommitImport(context.Background(), preview, ImportCommitOptions{})
	if !errors.Is(err, ErrImportStoreUnavailable) {
		t.Fatalf("commit error = %v, want ErrImportStoreUnavailable", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestCommitImportKeepsTagsNotesAndProxy(t *testing.T) {
	service, hosts, _, secrets, _ := newImportService(t)
	text := "Groups,Label,Tags,Notes,Hostname/IP,Protocol,Port,Username,Password,KeyPath,Passphrase,Proxy,LocalShell\n" +
		"Prod,web,alpha;beta,note,web.example.test,ssh,22,deploy,,,,socks5://u:p%40ss@127.0.0.1:1080,\n"
	preview, err := service.PreviewImport(context.Background(), text, PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 1 || len(hosts.hosts) != 1 {
		t.Fatalf("result = %+v, hosts = %+v", result, hosts.hosts)
	}
	host := hosts.hosts[0]
	if strings.Join(host.Tags, ",") != "alpha,beta" || host.Notes != "note" {
		t.Fatalf("imported Host lost tags/notes: %+v", host)
	}
	if host.Proxy == nil || host.Proxy.Type != domainvault.ProxySOCKS5 ||
		host.Proxy.Host != "127.0.0.1" || host.Proxy.Port != 1080 ||
		host.Proxy.Username != "u" || !host.Proxy.HasPassword {
		t.Fatalf("imported Host proxy = %+v", host.Proxy)
	}
	if got := secrets.values[HostSecretRef(host.ID, FieldProxyPassword)]; got != "p@ss" {
		t.Fatalf("proxy secret = %q, want p@ss", got)
	}
	data, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "p@ss") {
		t.Fatalf("Host JSON leaked the proxy password: %s", data)
	}
}

func TestCommitImportResolvesForwardProxyJump(t *testing.T) {
	service, hosts, _, _, _ := newImportService(t)
	text := "Host a\n    HostName a.example.test\n    ProxyJump b\n\n" +
		"Host b\n    HostName b.example.test\n"
	preview, err := service.PreviewImport(context.Background(), text,
		PreviewImportOptions{Format: vaultimport.FormatSSHConfig})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 2 {
		t.Fatalf("result = %+v", result)
	}
	var a, b *domainvault.Host
	for index := range hosts.hosts {
		switch hosts.hosts[index].Hostname {
		case "a.example.test":
			a = &hosts.hosts[index]
		case "b.example.test":
			b = &hosts.hosts[index]
		}
	}
	if a == nil || b == nil {
		t.Fatalf("imported Hosts = %+v", hosts.hosts)
	}
	if a.HostChain == nil || len(a.HostChain.HostIDs) != 1 ||
		a.HostChain.HostIDs[0] != b.ID {
		t.Fatalf("chain = %+v, want b %q", a.HostChain, b.ID)
	}
}
