package vault

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeSnippets struct {
	snippets []domainvault.Snippet
	packages []domainvault.SnippetPackage
	saves    int
}

func (f *fakeSnippets) ListSnippets(context.Context) ([]domainvault.Snippet, error) {
	return append([]domainvault.Snippet(nil), f.snippets...), nil
}

func (f *fakeSnippets) GetSnippet(_ context.Context, id string) (domainvault.Snippet, error) {
	for _, snippet := range f.snippets {
		if snippet.ID == id {
			return snippet, nil
		}
	}
	return domainvault.Snippet{}, ErrNotFound
}

func (f *fakeSnippets) SaveSnippet(_ context.Context, snippet domainvault.Snippet) error {
	f.saves++
	for i, existing := range f.snippets {
		if existing.ID == snippet.ID {
			f.snippets[i] = snippet
			return nil
		}
	}
	f.snippets = append(f.snippets, snippet)
	return nil
}

func (f *fakeSnippets) DeleteSnippet(_ context.Context, id string) error {
	for i, snippet := range f.snippets {
		if snippet.ID == id {
			f.snippets = append(f.snippets[:i], f.snippets[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeSnippets) ListSnippetPackages(context.Context) ([]domainvault.SnippetPackage, error) {
	packages := append([]domainvault.SnippetPackage(nil), f.packages...)
	sort.SliceStable(packages, func(i, j int) bool {
		if packages[i].OrderKey != packages[j].OrderKey {
			return packages[i].OrderKey < packages[j].OrderKey
		}
		return packages[i].Path < packages[j].Path
	})
	return packages, nil
}

func (f *fakeSnippets) GetSnippetPackage(_ context.Context, path string) (domainvault.SnippetPackage, error) {
	for _, pkg := range f.packages {
		if pkg.Path == path {
			return pkg, nil
		}
	}
	return domainvault.SnippetPackage{}, ErrNotFound
}

func (f *fakeSnippets) SaveSnippetPackage(_ context.Context, pkg domainvault.SnippetPackage) error {
	for i, existing := range f.packages {
		if existing.Path == pkg.Path {
			f.packages[i] = pkg
			return nil
		}
	}
	f.packages = append(f.packages, pkg)
	return nil
}

func (f *fakeSnippets) DeleteSnippetPackage(_ context.Context, path string) error {
	for i, pkg := range f.packages {
		if pkg.Path == path {
			f.packages = append(f.packages[:i], f.packages[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func newSnippetService(t *testing.T, hosts *fakeHosts, snippets *fakeSnippets) *Service {
	t.Helper()
	var ids int
	return NewService(hosts,
		WithSnippets(snippets),
		WithSnippetIDGenerator(func() string {
			ids++
			return "snippet-" + strconv.Itoa(ids)
		}),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
	)
}

func TestSnippetCRUDPersistsTargetsAndRememberedValues(t *testing.T) {
	snippets := &fakeSnippets{}
	service := newSnippetService(t, &fakeHosts{}, snippets)
	ctx := context.Background()

	created, err := service.CreateSnippet(ctx, domainvault.SnippetDraft{
		Label: " Restart nginx ", Command: "systemctl restart nginx",
		Package: "ops", Targets: []model.HostID{"host-1"}, TargetGroups: []string{"prod"},
	})
	if err != nil {
		t.Fatalf("CreateSnippet: %v", err)
	}
	if created.ID != "snippet-1" || created.Label != "Restart nginx" ||
		created.OrderKey != 1000 || created.MultiLineRunMode != domainvault.SnippetRunPaste {
		t.Fatalf("created = %+v", created)
	}

	updated, err := service.UpdateSnippet(ctx, created.ID, domainvault.SnippetDraft{
		Label: "Restart", Command: "systemctl restart nginx", Package: "ops/linux",
	})
	if err != nil {
		t.Fatalf("UpdateSnippet: %v", err)
	}
	if updated.ID != created.ID || updated.OrderKey != created.OrderKey ||
		updated.Package != "ops/linux" || len(updated.Targets) != 0 {
		t.Fatalf("updated = %+v", updated)
	}

	if err := service.SaveSnippetVariableValues(ctx, created.ID,
		map[string]string{"name": "web", "": "ignored"}); err != nil {
		t.Fatalf("SaveSnippetVariableValues: %v", err)
	}
	stored, err := snippets.GetSnippet(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.VariableValues, map[string]string{"name": "web"}) {
		t.Fatalf("values = %v", stored.VariableValues)
	}

	if err := service.DeleteSnippet(ctx, created.ID); err != nil {
		t.Fatalf("DeleteSnippet: %v", err)
	}
	if err := service.DeleteSnippet(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestPrepareSnippetSubstitutesVariablesAndResolvesTargets(t *testing.T) {
	hosts := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH, Group: "prod/web"},
		{ID: "host-2", Label: "db", Hostname: "10.0.0.2", Protocol: domainvault.ProtocolSSH, Group: "db"},
	}}
	snippets := &fakeSnippets{snippets: []domainvault.Snippet{{
		ID: "snippet-1", Label: "deploy", Package: "ops",
		Command:      "deploy {{app:api}} --to {{target}}",
		TargetGroups: []string{"prod"},
	}}}
	service := newSnippetService(t, hosts, snippets)

	run, err := service.PrepareSnippet(context.Background(), "snippet-1", map[string]string{"target": "web-1"})
	if err != nil {
		t.Fatalf("PrepareSnippet: %v", err)
	}
	if run.Command != "deploy api --to web-1" {
		t.Fatalf("command = %q", run.Command)
	}
	if len(run.Hosts) != 1 || run.Hosts[0].ID != "host-1" {
		t.Fatalf("hosts = %+v, want the group target", run.Hosts)
	}
	// The effective Host carries the protocol port, not the stored zero.
	if run.Hosts[0].Port != 22 {
		t.Fatalf("effective port = %d, want 22", run.Hosts[0].Port)
	}
}

func TestPrepareSnippetReportsMissingVariables(t *testing.T) {
	snippets := &fakeSnippets{snippets: []domainvault.Snippet{{
		ID: "snippet-1", Label: "deploy", Command: "deploy {{app}} --to {{target}}",
	}}}
	service := newSnippetService(t, &fakeHosts{}, snippets)

	_, err := service.PrepareSnippet(context.Background(), "snippet-1", map[string]string{"app": "api"})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("PrepareSnippet error = %v, want KindValidation", err)
	}
	var variables *SnippetVariableError
	if !errors.As(err, &variables) || !reflect.DeepEqual(variables.Missing, []string{"target"}) {
		t.Fatalf("missing = %+v", variables)
	}
}

func TestSnippetPackageRenameRewritesSnippetsAndDeleteKeepsThem(t *testing.T) {
	snippets := &fakeSnippets{
		packages: []domainvault.SnippetPackage{{Path: "ops", OrderKey: 1000}, {Path: "ops/linux", OrderKey: 2000}, {Path: "db", OrderKey: 3000}},
		snippets: []domainvault.Snippet{
			{ID: "snippet-1", Label: "a", Command: "a", Package: "ops"},
			{ID: "snippet-2", Label: "b", Command: "b", Package: "ops/linux"},
			{ID: "snippet-3", Label: "c", Command: "c", Package: "db"},
		},
	}
	service := newSnippetService(t, &fakeHosts{}, snippets)
	ctx := context.Background()

	renamed, err := service.RenameSnippetPackage(ctx, "ops", "platform")
	if err != nil {
		t.Fatalf("RenameSnippetPackage: %v", err)
	}
	if renamed.Path != "platform" {
		t.Fatalf("renamed = %+v", renamed)
	}
	listed, err := service.ListSnippetPackages(ctx)
	if err != nil {
		t.Fatalf("ListSnippetPackages: %v", err)
	}
	var paths []string
	for _, pkg := range listed {
		paths = append(paths, pkg.Path)
	}
	if !reflect.DeepEqual(paths, []string{"platform", "platform/linux", "db"}) {
		t.Fatalf("packages = %v", paths)
	}
	if _, err := snippets.GetSnippetPackage(ctx, "ops"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old package path survived: %v", err)
	}
	if snippets.snippets[0].Package != "platform" || snippets.snippets[1].Package != "platform/linux" {
		t.Fatalf("snippet packages = %q %q", snippets.snippets[0].Package, snippets.snippets[1].Package)
	}

	if _, err := service.RenameSnippetPackage(ctx, "platform", "platform/inner"); err == nil {
		t.Fatal("a package was moved inside itself")
	}
	if _, err := service.RenameSnippetPackage(ctx, "db", "platform"); err == nil {
		t.Fatal("a package overwrote an existing path")
	}

	if err := service.DeleteSnippetPackage(ctx, "platform"); err != nil {
		t.Fatalf("DeleteSnippetPackage: %v", err)
	}
	if len(snippets.packages) != 1 || snippets.packages[0].Path != "db" {
		t.Fatalf("packages after delete = %+v", snippets.packages)
	}
	if len(snippets.snippets) != 3 {
		t.Fatalf("snippets after delete = %d, want 3", len(snippets.snippets))
	}
	if snippets.snippets[0].Package != "" || snippets.snippets[1].Package != "" {
		t.Fatalf("deleted package references = %q %q", snippets.snippets[0].Package, snippets.snippets[1].Package)
	}
	if snippets.snippets[2].Package != "db" {
		t.Fatalf("unrelated package changed: %q", snippets.snippets[2].Package)
	}
}

func TestSnippetTargetGroupsFollowHostGroupRenameAndDelete(t *testing.T) {
	hosts := &fakeHosts{hosts: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "10.0.0.1",
		Protocol: domainvault.ProtocolSSH, Group: "prod/web",
	}}}
	groups := &fakeGroups{items: []domainvault.Group{{Path: "prod", OrderKey: 1000}}}
	snippets := &fakeSnippets{snippets: []domainvault.Snippet{{
		ID: "snippet-1", Label: "deploy", Command: "deploy",
		TargetGroups: []string{"prod", "db"},
	}}}
	service := NewService(hosts,
		WithGroups(groups),
		WithSnippets(snippets),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
	)
	ctx := context.Background()

	if _, err := service.UpdateGroup(ctx, "prod", domainvault.GroupDraft{Path: "platform"}); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if got := snippets.snippets[0].TargetGroups; !reflect.DeepEqual(got, []string{"platform", "db"}) {
		t.Fatalf("target groups after rename = %v", got)
	}

	if err := service.DeleteGroup(ctx, "platform"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if got := snippets.snippets[0].TargetGroups; !reflect.DeepEqual(got, []string{"db"}) {
		t.Fatalf("target groups after delete = %v", got)
	}
	if len(snippets.snippets) != 1 {
		t.Fatalf("the Snippet was deleted with the group")
	}
}
