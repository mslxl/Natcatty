package vault

import (
	"reflect"
	"testing"
)

func TestParseSnippetVariablesFindsEveryVariableInFirstOccurrenceOrder(t *testing.T) {
	defs := ParseSnippetVariables("echo {{test}}\necho {{test2}} and {{test}}")
	want := []SnippetVariableDef{{Name: "test"}, {Name: "test2"}}
	if !reflect.DeepEqual(defs, want) {
		t.Fatalf("variables = %+v, want %+v", defs, want)
	}
	if SnippetHasVariables("ls -la") {
		t.Fatal("a plain command declares a variable")
	}
}

func TestParseSnippetVariablesReadsDefaultsAndUnicodeNames(t *testing.T) {
	defs := ParseSnippetVariables("fallocate -l {{内存大小:4}}G")
	want := []SnippetVariableDef{{Name: "内存大小", DefaultValue: "4", HasDefault: true}}
	if !reflect.DeepEqual(defs, want) {
		t.Fatalf("variables = %+v, want %+v", defs, want)
	}
	defs = ParseSnippetVariables("echo {{name:}}")
	if len(defs) != 1 || !defs[0].HasDefault || defs[0].DefaultValue != "" {
		t.Fatalf("an empty default was lost: %+v", defs)
	}
}

func TestParseSnippetVariablesIgnoresDockerGoTemplates(t *testing.T) {
	for _, command := range []string{
		"ls -lh $(docker inspect --format='{{.LogPath}}' moviepilot)",
		"ls -lh $(docker inspect --format='{{- .LogPath -}}' moviepilot)",
		"docker inspect --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' moviepilot",
		"docker inspect --format='{{range $k, $v := .NetworkSettings.Networks}}{{$v.IPAddress}}{{end}}' moviepilot",
		"docker inspect --format='{{$p := .LogPath}}{{$p}}' moviepilot",
		"docker inspect --format='{{json .}}' moviepilot",
	} {
		if defs := ParseSnippetVariables(command); len(defs) != 0 {
			t.Errorf("%q declared variables %+v", command, defs)
		}
	}
}

func TestApplySnippetVariablesSubstitutesValuesAndDefaults(t *testing.T) {
	command := "fallocate -l {{内存大小:4}}G\nswapon {{内存大小:4}}"
	got, missing := ApplySnippetVariables(command, map[string]string{"内存大小": "8"})
	if len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	if want := "fallocate -l 8G\nswapon 8"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}

	got, missing = ApplySnippetVariables("echo {{name:world}}", map[string]string{"name": "   "})
	if len(missing) != 0 || got != "echo world" {
		t.Fatalf("blank value did not fall back to the default: %q %v", got, missing)
	}
}

func TestApplySnippetVariablesReportsMissingAndChangesNothing(t *testing.T) {
	got, missing := ApplySnippetVariables("echo {{a}} {{b:fallback}} {{c}}", map[string]string{"a": "A"})
	if got != "" {
		t.Fatalf("a half-filled command was substituted: %q", got)
	}
	if !reflect.DeepEqual(missing, []string{"c"}) {
		t.Fatalf("missing = %v, want [c]", missing)
	}
}

func TestApplySnippetVariablesPreservesGoTemplatesAndSubstitutesNeighbours(t *testing.T) {
	command := "docker inspect --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' {{container:moviepilot}}"
	got, missing := ApplySnippetVariables(command, nil)
	if len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	want := "docker inspect --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' moviepilot"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestApplySnippetVariablesKeepsControlNamesThatAreRealVariables(t *testing.T) {
	got, missing := ApplySnippetVariables("echo {{end}}", map[string]string{"end": "done"})
	if len(missing) != 0 || got != "echo done" {
		t.Fatalf("control-looking variable = %q %v", got, missing)
	}
}

func TestPreviewSnippetCommandLeavesRequiredPlaceholders(t *testing.T) {
	command := "echo {{a}} {{b:fallback}}"
	if got := PreviewSnippetCommand(command, map[string]string{"a": "A"}); got != "echo A fallback" {
		t.Fatalf("preview = %q", got)
	}
	if got := PreviewSnippetCommand(command, nil); got != "echo {{a}} fallback" {
		t.Fatalf("preview = %q", got)
	}
}
