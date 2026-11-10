package plugin

import (
	"strconv"
	"strings"
	"testing"
)

func manifestWithView(view ViewContribution) Manifest {
	manifest := validManifest()
	manifest.Permissions.Required = []string{string(PermissionViews)}
	manifest.Contributions.Views = []ViewContribution{view}
	return manifest
}

func validView() ViewContribution {
	return ViewContribution{
		ID:         "counter",
		Title:      "Counter",
		Placements: []Placement{PlacementPane},
		Instances:  InstanceSingleton,
	}
}

func TestViewContributionValidation(t *testing.T) {
	manifest := manifestWithView(validView())
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	view := manifest.Contributions.Views[0]
	if !view.Allows(PlacementPane) || view.Allows(PlacementWindow) {
		t.Fatalf("placements = %v", view.Placements)
	}
	if view.Instances.Multi() {
		t.Fatal("singleton view reports multi-instance")
	}
	if got := QualifiedViewID(manifest.ID, view.ID); got != "plugin.acme.hello.counter" {
		t.Fatalf("qualified view = %q", got)
	}
}

func TestViewPlacementVocabulary(t *testing.T) {
	want := []Placement{PlacementPane, PlacementTab, PlacementPage, PlacementWindow, PlacementAside}
	got := Placements()
	if len(got) != len(want) {
		t.Fatalf("placements = %v", got)
	}
	for i := range want {
		if got[i] != want[i] || !want[i].Valid() {
			t.Fatalf("placement %d = %q", i, got[i])
		}
	}
	if Placement("floating").Valid() {
		t.Fatal("unknown placement reported valid")
	}
}

func TestViewInstancePolicyDefaultsToSingleton(t *testing.T) {
	view := validView()
	view.Instances = ""
	if err := manifestWithView(view).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if view.Instances.Effective() != InstanceSingleton || view.Instances.Multi() {
		t.Fatalf("empty policy = %q", view.Instances.Effective())
	}
	view.Instances = InstanceMulti
	if err := manifestWithView(view).Validate(); err != nil {
		t.Fatalf("Validate multi: %v", err)
	}
	if !view.Instances.Multi() {
		t.Fatal("multi policy not reported")
	}
}

func TestViewContributionValidationRejectsBadFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(*ViewContribution)
	}{
		{"bad id", "contributions.views[0].id", func(v *ViewContribution) { v.ID = "Counter" }},
		{"empty title", "contributions.views[0].title", func(v *ViewContribution) { v.Title = " " }},
		{"no placements", "contributions.views[0].placements", func(v *ViewContribution) { v.Placements = nil }},
		{"unknown placement", "contributions.views[0].placements[0]", func(v *ViewContribution) {
			v.Placements = []Placement{"floating"}
		}},
		{"duplicate placement", "contributions.views[0].placements[1]", func(v *ViewContribution) {
			v.Placements = []Placement{PlacementPane, PlacementPane}
		}},
		{"unknown policy", "contributions.views[0].instances", func(v *ViewContribution) {
			v.Instances = "many"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := validView()
			test.edit(&view)
			err := manifestWithView(view).Validate()
			if err == nil {
				t.Fatal("Validate accepted an invalid view")
			}
			var validation *ValidationError
			if !asValidationError(err, &validation) || validation.Field != test.field {
				t.Fatalf("error = %v, want field %q", err, test.field)
			}
		})
	}
}

func TestDuplicateViewIDsAreRejected(t *testing.T) {
	manifest := manifestWithView(validView())
	manifest.Contributions.Views = append(manifest.Contributions.Views, validView())
	err := manifest.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate view") {
		t.Fatalf("error = %v, want duplicate view", err)
	}
}

func TestViewsRequireTheViewsPermission(t *testing.T) {
	manifest := manifestWithView(validView())
	manifest.Permissions.Required = nil
	err := manifest.Validate()
	if err == nil || !strings.Contains(err.Error(), string(PermissionViews)) {
		t.Fatalf("error = %v, want views permission", err)
	}

	manifest.Permissions.Optional = []string{string(PermissionViews)}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("optional views permission rejected: %v", err)
	}
}

func TestWebviewViewContributionDefaultsAndOrigin(t *testing.T) {
	view := validView()
	view.Surface = ViewSurfaceWebview
	manifest := manifestWithView(view)
	manifest.Permissions.Required = append(manifest.Permissions.Required, string(PermissionUIWebview))
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	view = manifest.Contributions.Views[0]
	if view.Surface.Effective() != ViewSurfaceWebview || !view.Surface.Webview() {
		t.Fatalf("surface = %q", view.Surface)
	}
	if got := view.EffectiveEntry(); got != DefaultWebviewEntry {
		t.Fatalf("entry = %q, want %q", got, DefaultWebviewEntry)
	}
	if got := WebviewOrigin("acme.hello"); got != "natcatty-plugin://acme.hello" {
		t.Fatalf("origin = %q", got)
	}
	if got := WebviewURL("acme.hello", ""); got != "natcatty-plugin://acme.hello/ui/index.html" {
		t.Fatalf("url = %q", got)
	}
}

func TestNativeViewMayDeclareBridgeMethodsForAnEmbeddedWebview(t *testing.T) {
	view := validView()
	view.Surface = ViewSurfaceNative
	view.Bridge = []string{"load", "save_note"}
	manifest := manifestWithView(view)
	manifest.Permissions.Required = append(manifest.Permissions.Required, string(PermissionUIWebview))
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := manifest.Contributions.Views[0].Surface.Effective(); got != ViewSurfaceNative {
		t.Fatalf("surface = %q", got)
	}
}

func TestWebviewViewRejectsBadFields(t *testing.T) {
	tests := []struct {
		name       string
		field      string
		permission bool
		edit       func(*ViewContribution)
	}{
		{"unknown surface", "contributions.views[0].surface", true, func(v *ViewContribution) {
			v.Surface = "browser"
		}},
		{"traversal entry", "contributions.views[0].entry", true, func(v *ViewContribution) {
			v.Surface = ViewSurfaceWebview
			v.Entry = "ui/../plugin.wasm"
		}},
		{"absolute entry", "contributions.views[0].entry", true, func(v *ViewContribution) {
			v.Surface = ViewSurfaceWebview
			v.Entry = "/ui/index.html"
		}},
		{"entry outside ui", "contributions.views[0].entry", true, func(v *ViewContribution) {
			v.Surface = ViewSurfaceWebview
			v.Entry = "plugin.wasm"
		}},
		{"non html entry", "contributions.views[0].entry", true, func(v *ViewContribution) {
			v.Surface = ViewSurfaceWebview
			v.Entry = "ui/app.js"
		}},
		{"entry on native view", "contributions.views[0].entry", true, func(v *ViewContribution) {
			v.Entry = "ui/embed.html"
		}},
		{"webview without permission", "permissions", false, func(v *ViewContribution) {
			v.Surface = ViewSurfaceWebview
		}},
		{"bridge without permission", "permissions", false, func(v *ViewContribution) {
			v.Bridge = []string{"load"}
		}},
		{"bridge method shape", "contributions.views[0].bridge[0]", true, func(v *ViewContribution) {
			v.Bridge = []string{"Load"}
		}},
		{"duplicate bridge method", "contributions.views[0].bridge[1]", true, func(v *ViewContribution) {
			v.Bridge = []string{"load", "load"}
		}},
		{"too many bridge methods", "contributions.views[0].bridge", true, func(v *ViewContribution) {
			v.Bridge = make([]string, MaxWebviewBridgeMethods+1)
			for i := range v.Bridge {
				v.Bridge[i] = "m" + strconv.Itoa(i)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := validView()
			test.edit(&view)
			manifest := manifestWithView(view)
			if test.permission {
				manifest.Permissions.Required = append(manifest.Permissions.Required,
					string(PermissionUIWebview))
			}
			err := manifest.Validate()
			if err == nil {
				t.Fatal("Validate accepted an invalid webview view")
			}
			var validation *ValidationError
			if !asValidationError(err, &validation) || validation.Field != test.field {
				t.Fatalf("error = %v, want field %q", err, test.field)
			}
		})
	}
}

// asValidationError is a local errors.As for the typed manifest error.
func asValidationError(err error, target **ValidationError) bool {
	value, ok := err.(*ValidationError)
	if ok {
		*target = value
	}
	return ok
}
