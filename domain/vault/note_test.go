package vault

import (
	"strings"
	"testing"
)

func TestNormalizeNoteDraft(t *testing.T) {
	draft, err := NormalizeNoteDraft(NoteDraft{
		Title:   "  Runbook  ",
		Group:   " ops\\db ",
		Content: "# Restart\n\nRun `systemctl restart db`.",
	})
	if err != nil {
		t.Fatalf("NormalizeNoteDraft: %v", err)
	}
	if draft.Title != "Runbook" || draft.Group != "ops/db" {
		t.Fatalf("draft = %+v", draft)
	}
	if !strings.Contains(draft.Content, "systemctl") {
		t.Fatalf("content = %q", draft.Content)
	}
}

func TestNormalizeNoteDraftRejectsBadInput(t *testing.T) {
	cases := []struct {
		name  string
		draft NoteDraft
		field Field
		rule  Rule
	}{
		{"empty title", NoteDraft{Content: "x"}, FieldNote, RuleRequired},
		{"long title", NoteDraft{Title: strings.Repeat("x", maxNoteTitleLength+1)}, FieldNote, RuleTooLong},
		{"long content", NoteDraft{Title: "x", Content: strings.Repeat("x", maxNoteContentLength+1)}, FieldNoteContent, RuleTooLong},
		{"control group", NoteDraft{Title: "x", Group: "a\x00b"}, FieldNoteGroup, RuleInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeNoteDraft(tc.draft)
			validation, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("err = %v", err)
			}
			if validation.Field != tc.field || validation.Rule != tc.rule {
				t.Fatalf("validation = %+v", validation)
			}
		})
	}
}

func TestNormalizeNoteGroupDraft(t *testing.T) {
	draft, err := NormalizeNoteGroupDraft(NoteGroupDraft{Path: "  ops\\db  "})
	if err != nil {
		t.Fatalf("NormalizeNoteGroupDraft: %v", err)
	}
	if draft.Path != "ops/db" {
		t.Fatalf("path = %q", draft.Path)
	}
	if _, err := NormalizeNoteGroupDraft(NoteGroupDraft{}); err == nil {
		t.Fatalf("an empty note group path was accepted")
	}
}

func TestSearchNotesMatchesTitleContentAndGroupCaseInsensitively(t *testing.T) {
	notes := []Note{
		{ID: "1", Title: "Restart database", Content: "systemctl restart postgres", Group: "ops/db"},
		{ID: "2", Title: "Web deploy", Content: "CI deploys the WEB tier", Group: "ops/web"},
		{ID: "3", Title: "Notes", Content: "unrelated", Group: ""},
	}
	for _, query := range []string{"WEB", "web", "  web  ", "ci deploy"} {
		found := SearchNotes(notes, query)
		if len(found) != 1 || found[0].ID != "2" {
			t.Fatalf("query %q found %+v", query, found)
		}
	}
	if found := SearchNotes(notes, "ops"); len(found) != 2 {
		t.Fatalf("group search found %+v", found)
	}
	if found := SearchNotes(notes, "postgres restart"); len(found) != 1 || found[0].ID != "1" {
		t.Fatalf("multi-token search found %+v", found)
	}
	if found := SearchNotes(notes, "missing"); len(found) != 0 {
		t.Fatalf("unrelated query found %+v", found)
	}
	if found := SearchNotes(notes, "   "); len(found) != len(notes) {
		t.Fatalf("empty query found %d notes", len(found))
	}
}

func TestCollectNoteGroupPathsAddsAncestors(t *testing.T) {
	paths := CollectNoteGroupPaths(
		[]string{"ops"},
		[]Note{{Group: "ops/db/prod"}},
	)
	want := []string{"ops", "ops/db", "ops/db/prod"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
}
