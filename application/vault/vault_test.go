package vault

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeHosts is the in-memory HostRepository the Service is tested against.
type fakeHosts struct {
	hosts   []domainvault.Host
	saves   int
	saveErr error
}

func (f *fakeHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), f.hosts...), nil
}

func (f *fakeHosts) Get(_ context.Context, id model.HostID) (domainvault.Host, error) {
	for _, host := range f.hosts {
		if host.ID == id {
			return host, nil
		}
	}
	return domainvault.Host{}, ErrNotFound
}

func (f *fakeHosts) Save(_ context.Context, host domainvault.Host) error {
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	for i, existing := range f.hosts {
		if existing.ID == host.ID {
			f.hosts[i] = host
			return nil
		}
	}
	f.hosts = append(f.hosts, host)
	return nil
}

func (f *fakeHosts) Delete(_ context.Context, id model.HostID) error {
	for i, host := range f.hosts {
		if host.ID == id {
			f.hosts = append(f.hosts[:i], f.hosts[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func newTestService(t *testing.T, hosts *fakeHosts) *Service {
	t.Helper()
	var ids int
	return NewService(hosts,
		WithIDGenerator(func() model.HostID {
			ids++
			return model.HostID("host-" + strconv.Itoa(ids))
		}),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
	)
}

func TestCreateHostNormalizesAndPersists(t *testing.T) {
	repo := &fakeHosts{}
	service := newTestService(t, repo)

	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "  example.com ",
		Username: " root ",
		Group:    "/prod/web/",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if host.ID != "host-1" {
		t.Errorf("ID = %q, want host-1", host.ID)
	}
	if host.Label != "example.com" || host.Port != 22 || host.Protocol != domainvault.ProtocolSSH {
		t.Errorf("Host = %+v, want a normalized SSH Host", host)
	}
	if host.OrderKey != 1000 {
		t.Errorf("OrderKey = %d, want 1000", host.OrderKey)
	}
	if !host.CreatedAt.Equal(time.Unix(1700000000, 0)) || !host.UpdatedAt.Equal(host.CreatedAt) {
		t.Errorf("timestamps = %v / %v, want the injected clock", host.CreatedAt, host.UpdatedAt)
	}
	if repo.saves != 1 || len(repo.hosts) != 1 {
		t.Fatalf("saves = %d, hosts = %d, want 1 and 1", repo.saves, len(repo.hosts))
	}
}

func TestCreateHostRejectsInvalidDraftsWithATypedError(t *testing.T) {
	repo := &fakeHosts{}
	service := newTestService(t, repo)

	_, err := service.Create(context.Background(), domainvault.Draft{Label: "web"})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("Create error = %v, want a validation Error", err)
	}
	var invalid *domainvault.ValidationError
	if !errors.As(err, &invalid) || invalid.Field != domainvault.FieldHostname {
		t.Fatalf("validation detail = %+v, want FieldHostname", invalid)
	}
	if repo.saves != 0 {
		t.Fatalf("saves = %d after an invalid draft, want 0", repo.saves)
	}
}

func TestCreateHostRejectsADuplicateEndpoint(t *testing.T) {
	repo := &fakeHosts{}
	service := newTestService(t, repo)

	first := domainvault.Draft{Label: "web", Hostname: "example.com", Username: "root", Group: "prod"}
	if _, err := service.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	duplicate := first
	duplicate.Label = "web again"
	duplicate.Hostname = "EXAMPLE.com"
	duplicate.Protocol = domainvault.ProtocolSSH

	_, err := service.Create(context.Background(), duplicate)
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindConflict {
		t.Fatalf("Create duplicate error = %v, want a conflict Error", err)
	}
	if len(repo.hosts) != 1 {
		t.Fatalf("hosts = %d, want 1", len(repo.hosts))
	}
}

func TestUpdateHostPreservesIdentityAndOrder(t *testing.T) {
	repo := &fakeHosts{}
	service := newTestService(t, repo)
	created, err := service.Create(context.Background(), domainvault.Draft{
		Label: "web", Hostname: "example.com", Username: "root", Group: "prod",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.Update(context.Background(), created.ID, domainvault.Draft{
		Label: "web-2", Hostname: "example.org", Port: 2222, Username: "admin", Group: "prod/web", Pinned: true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ID != created.ID {
		t.Errorf("ID = %q, want %q", updated.ID, created.ID)
	}
	if updated.OrderKey != created.OrderKey {
		t.Errorf("OrderKey = %d, want %d", updated.OrderKey, created.OrderKey)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", updated.CreatedAt, created.CreatedAt)
	}
	if updated.Label != "web-2" || updated.Hostname != "example.org" || updated.Port != 2222 || !updated.Pinned {
		t.Errorf("updated Host = %+v, want the edited values", updated)
	}
}

func TestUpdateHostRejectsAnotherHostsEndpoint(t *testing.T) {
	repo := &fakeHosts{}
	service := newTestService(t, repo)
	first, err := service.Create(context.Background(), domainvault.Draft{Hostname: "a.example", Username: "root"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), domainvault.Draft{Hostname: "b.example", Username: "root"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Update(context.Background(), second.ID, domainvault.Draft{
		Hostname: first.Hostname, Username: first.Username,
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindConflict {
		t.Fatalf("Update error = %v, want a conflict Error", err)
	}
}

func TestUpdateHostReportsANotFoundHost(t *testing.T) {
	service := newTestService(t, &fakeHosts{})

	_, err := service.Update(context.Background(), "missing", domainvault.Draft{Hostname: "example.com"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update error = %v, want ErrNotFound", err)
	}
}

func TestCreateHostOrdersAfterTheCurrentMaximum(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{{ID: "old", OrderKey: 4000}}}
	service := newTestService(t, repo)

	host, err := service.Create(context.Background(), domainvault.Draft{Hostname: "new.example"})
	if err != nil {
		t.Fatal(err)
	}
	if host.OrderKey != 5000 {
		t.Fatalf("OrderKey = %d, want 5000", host.OrderKey)
	}
}

func TestDeleteHostLeavesOtherHosts(t *testing.T) {
	repo := &fakeHosts{}
	service := newTestService(t, repo)
	first, err := service.Create(context.Background(), domainvault.Draft{Hostname: "a.example"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), domainvault.Draft{Hostname: "b.example"})
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Delete(context.Background(), first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	hosts, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID != second.ID {
		t.Fatalf("hosts = %+v, want only %q", hosts, second.ID)
	}
	if err := service.Delete(context.Background(), first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete error = %v, want ErrNotFound", err)
	}
}
