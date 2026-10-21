package vault

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestNormalizeSnippetDraftValidatesAndCanonicalizes(t *testing.T) {
	draft, err := NormalizeSnippetDraft(SnippetDraft{
		Label:        "  Restart nginx ",
		Command:      "systemctl restart nginx",
		Package:      " ops / linux ",
		Tags:         []string{" prod ", "prod", ""},
		Targets:      []model.HostID{" host-1 ", "host-1", ""},
		TargetGroups: []string{"prod/", "prod"},
	})
	if err != nil {
		t.Fatalf("NormalizeSnippetDraft: %v", err)
	}
	if draft.Label != "Restart nginx" || draft.Package != "ops/linux" {
		t.Fatalf("normalized = %+v", draft)
	}
	if !reflect.DeepEqual(draft.Tags, []string{"prod"}) {
		t.Fatalf("tags = %v", draft.Tags)
	}
	if !reflect.DeepEqual(draft.Targets, []model.HostID{"host-1"}) ||
		!reflect.DeepEqual(draft.TargetGroups, []string{"prod"}) {
		t.Fatalf("targets = %v groups = %v", draft.Targets, draft.TargetGroups)
	}
	if draft.MultiLineRunMode != SnippetRunPaste {
		t.Fatalf("run mode = %q, want paste", draft.MultiLineRunMode)
	}
}

func TestNormalizeSnippetDraftRejectsInvalidInput(t *testing.T) {
	for name, draft := range map[string]SnippetDraft{
		"missing label":   {Command: "echo"},
		"invalid package": {Label: "x", Package: "bad/space name"},
		"invalid mode":    {Label: "x", MultiLineRunMode: "sometimes"},
	} {
		if _, err := NormalizeSnippetDraft(draft); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	var invalid *ValidationError
	_, err := NormalizeSnippetDraft(SnippetDraft{Label: "x", Package: "bad/space name"})
	if !errors.As(err, &invalid) || invalid.Field != FieldSnippetPackage || invalid.Rule != RuleInvalid {
		t.Fatalf("package error = %v", err)
	}
}

func TestNormalizeSnippetDraftAllHostsDropsExplicitTargets(t *testing.T) {
	draft, err := NormalizeSnippetDraft(SnippetDraft{
		Label: "all", TargetsAllHosts: true,
		Targets: []model.HostID{"host-1"}, TargetGroups: []string{"prod"},
	})
	if err != nil {
		t.Fatalf("NormalizeSnippetDraft: %v", err)
	}
	if draft.Targets != nil || draft.TargetGroups != nil {
		t.Fatalf("all-hosts kept targets: %+v", draft)
	}
}

func TestSnippetTargetsAndGroupRename(t *testing.T) {
	snippet := Snippet{Targets: []model.HostID{"host-1"}, TargetGroups: []string{"prod/web"}}
	if !SnippetAppliesToHost(snippet, SnippetTargetHost{ID: "host-1", Group: "other"}) {
		t.Fatal("an explicit target did not apply")
	}
	if !SnippetAppliesToHost(snippet, SnippetTargetHost{ID: "host-2", Group: "prod/web/deep"}) {
		t.Fatal("a descendant group did not apply")
	}
	if SnippetAppliesToHost(snippet, SnippetTargetHost{ID: "host-2", Group: "prod"}) {
		t.Fatal("a parent group applied")
	}
	if !SnippetCanRunInTerminal(Snippet{}, SnippetTargetHost{}) {
		t.Fatal("an unscoped Snippet cannot run in a Session")
	}
	if SnippetCanRunInTerminal(snippet, SnippetTargetHost{ID: "host-9", Group: "db"}) {
		t.Fatal("a scoped Snippet ran outside its targets")
	}

	renamed := RemapSnippetTargetGroups(
		Snippet{TargetGroups: []string{"prod", "prod/web", "db"}}, "prod", "platform")
	want := []string{"platform", "platform/web", "db"}
	if !reflect.DeepEqual(renamed.TargetGroups, want) {
		t.Fatalf("groups = %v, want %v", renamed.TargetGroups, want)
	}
	cleared := RemoveSnippetTargetGroups(Snippet{TargetGroups: []string{"prod", "db"}}, "prod")
	if !reflect.DeepEqual(cleared.TargetGroups, []string{"db"}) {
		t.Fatalf("groups after delete = %v", cleared.TargetGroups)
	}
}

func TestSnippetRunnableHostsFollowsTargetsAndAllHosts(t *testing.T) {
	hosts := []Host{
		{ID: "host-1", Group: "prod/web"},
		{ID: "host-2", Group: "db"},
	}
	got := SnippetRunnableHosts(Snippet{Targets: []model.HostID{"host-2"}}, hosts)
	if len(got) != 1 || got[0].ID != "host-2" {
		t.Fatalf("runnable = %+v", got)
	}
	got = SnippetRunnableHosts(Snippet{TargetGroups: []string{"prod"}}, hosts)
	if len(got) != 1 || got[0].ID != "host-1" {
		t.Fatalf("runnable = %+v", got)
	}
	got = SnippetRunnableHosts(Snippet{TargetsAllHosts: true}, hosts)
	if len(got) != 2 {
		t.Fatalf("all hosts runnable = %+v", got)
	}
}
