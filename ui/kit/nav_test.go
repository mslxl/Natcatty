package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSearchFieldClearEmptiesTheQueryInEveryRole(t *testing.T) {
	for _, role := range []SearchRole{SearchSidebar, SearchFilter} {
		query := "deploy"
		tt := ui.NewTester(func(c *ui.Context) {
			SearchField(c, &query, SearchOptions{
				Role: role, Placeholder: "Search", Clear: "Clear search", ShowClear: query != "",
			})
		}, 400, 80)
		if err := tt.Click("Clear search"); err != nil {
			t.Fatalf("role %d: %v", role, err)
		}
		if query != "" {
			t.Fatalf("role %d: query = %q after clear, want empty", role, query)
		}
		if _, shown := tt.Find("Clear search"); shown {
			t.Fatalf("role %d: clear button still shown with an empty query", role)
		}
	}
}

func TestSearchFieldClearRunsOnClearInsteadOfEmptying(t *testing.T) {
	query, cleared := "deploy", false
	tt := ui.NewTester(func(c *ui.Context) {
		SearchField(c, &query, SearchOptions{
			Role: SearchSidebar, Placeholder: "Search", Clear: "Clear search", ShowClear: true,
			OnClear: func() { cleared = true },
		})
	}, 400, 80)
	if err := tt.Click("Clear search"); err != nil {
		t.Fatal(err)
	}
	if !cleared || query != "deploy" {
		t.Fatalf("cleared = %v, query = %q; want OnClear to run and the query kept", cleared, query)
	}
}
