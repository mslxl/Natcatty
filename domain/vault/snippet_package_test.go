package vault

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeSnippetPackagePathCanonicalizesAndValidates(t *testing.T) {
	for raw, want := range map[string]string{
		"":              "",
		"  ops  ":       "ops",
		" ops / linux ": "ops/linux",
		"ops//linux/":   "ops/linux",
		`ops\linux`:     "ops/linux",
		"生产_环境-2/linux": "生产_环境-2/linux",
	} {
		got, err := NormalizeSnippetPackagePath(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q normalized to %q, want %q", raw, got, want)
		}
	}

	var invalid *ValidationError
	_, err := NormalizeSnippetPackagePath("ops/bad name")
	if !errors.As(err, &invalid) || invalid.Field != FieldSnippetPackage || invalid.Rule != RuleInvalid {
		t.Fatalf("invalid path error = %v", err)
	}
}

func TestNormalizeSnippetPackageDraftRequiresAPath(t *testing.T) {
	if _, err := NormalizeSnippetPackageDraft(SnippetPackageDraft{Path: "  "}); err == nil {
		t.Fatal("an empty package path was accepted")
	}
	draft, err := NormalizeSnippetPackageDraft(SnippetPackageDraft{Path: " ops/linux "})
	if err != nil || draft.Path != "ops/linux" {
		t.Fatalf("draft = %+v, err = %v", draft, err)
	}
}

func TestCollectSnippetPackagePathsInfersAncestors(t *testing.T) {
	got := CollectSnippetPackagePaths(
		[]string{"ops", "db/mysql"},
		[]Snippet{{Package: "prod/web/deep"}, {Package: "ops"}, {}},
	)
	want := []string{"ops", "db", "db/mysql", "prod", "prod/web", "prod/web/deep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func TestSnippetPackagePathHelpers(t *testing.T) {
	if !SnippetPackageAtOrBelow("ops/linux", "ops") ||
		SnippetPackageAtOrBelow("ops2", "ops") {
		t.Fatal("at-or-below is wrong")
	}
	if got := ReplaceSnippetPackagePrefix("ops/linux", "ops", "platform"); got != "platform/linux" {
		t.Fatalf("rewrite = %q", got)
	}
	if got := SnippetPackageAncestors("a/b/c"); !reflect.DeepEqual(got, []string{"a", "a/b", "a/b/c"}) {
		t.Fatalf("ancestors = %v", got)
	}
}
