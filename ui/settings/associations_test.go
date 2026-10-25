package settings

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

type scriptedPicker struct {
	answers []domain.ExternalApplication
	calls   int
}

func (p *scriptedPicker) pick() (domain.ExternalApplication, bool) {
	index := p.calls
	p.calls++
	if index >= len(p.answers) || p.answers[index].Path == "" {
		return domain.ExternalApplication{}, false
	}
	return p.answers[index], true
}

func application(path, name string) domain.ExternalApplication {
	return domain.ExternalApplication{Path: path, Name: name}
}

func TestSelectingTheDefaultOpenerPersistsEachKind(t *testing.T) {
	prefs := newFakePreferences()
	picker := &scriptedPicker{answers: []domain.ExternalApplication{
		application(`C:\Tools\viewer.exe`, "Viewer"),
		application(`C:\Tools\other.exe`, "Other"),
	}}
	state := NewAssociationsState(prefs, WithApplicationPicker(picker.pick))

	if got := state.DefaultOpener().Kind; got != domain.OpenerSystem {
		t.Fatalf("default opener = %q, want the system opener", got)
	}
	if !state.SelectDefaultKind(domain.OpenerInternal) {
		t.Fatal("selecting the internal editor failed")
	}
	if got := prefs.settings.SFTPEditor.DefaultOpener.Kind; got != domain.OpenerInternal {
		t.Errorf("persisted default = %q, want internal", got)
	}
	if picker.calls != 0 {
		t.Errorf("the internal choice asked for an application")
	}

	if !state.SelectDefaultKind(domain.OpenerExternal) {
		t.Fatal("selecting the external opener failed")
	}
	opener := prefs.settings.SFTPEditor.DefaultOpener
	if opener.Kind != domain.OpenerExternal || opener.Application.Name != "Viewer" {
		t.Fatalf("persisted external opener = %+v", opener)
	}

	if !state.ChooseDefaultApplication() {
		t.Fatal("changing the default application failed")
	}
	if got := prefs.settings.SFTPEditor.DefaultOpener.Application.Name; got != "Other" {
		t.Errorf("application after change = %q, want Other", got)
	}

	if !state.ResetDefaultOpener() {
		t.Fatal("resetting the default opener failed")
	}
	if got := prefs.settings.SFTPEditor.DefaultOpener.Kind; got != domain.OpenerSystem {
		t.Errorf("default after reset = %q, want system", got)
	}
	if state.SelectDefaultKind("bogus") {
		t.Error("an invalid opener kind was accepted")
	}
}

func TestSelectingExternalWithoutAnApplicationLeavesTheDefaultAlone(t *testing.T) {
	prefs := newFakePreferences()
	picker := &scriptedPicker{answers: []domain.ExternalApplication{{}}}
	state := NewAssociationsState(prefs, WithApplicationPicker(picker.pick))
	if state.SelectDefaultKind(domain.OpenerExternal) {
		t.Fatal("cancelling the application chooser changed the default")
	}
	if got := prefs.settings.SFTPEditor.DefaultOpener.Kind; got != domain.OpenerSystem {
		t.Errorf("default = %q, want the unchanged system opener", got)
	}

	withoutPicker := NewAssociationsState(newFakePreferences())
	if withoutPicker.SelectDefaultKind(domain.OpenerExternal) {
		t.Error("the external opener was selected without a picker")
	}
}

func TestAssociationsAreAddedEditedAndRemoved(t *testing.T) {
	prefs := newFakePreferences()
	state := NewAssociationsState(prefs)

	*state.Extension() = ".TXT"
	if !state.SelectKind(domain.OpenerInternal) {
		t.Fatal("selecting the internal opener failed")
	}
	if !state.Save() {
		t.Fatalf("saving the association failed: %s", state.Problem())
	}
	association, ok := prefs.settings.SFTPEditor.AssociationFor("txt")
	if !ok || association.Kind != domain.OpenerInternal {
		t.Fatalf("stored association = %+v, %v", association, ok)
	}
	if state.ExtensionValue() != "" || state.Kind() != domain.OpenerSystem || state.Editing() != "" {
		t.Errorf("the form was not cleared: %q %q %q",
			state.ExtensionValue(), state.Kind(), state.Editing())
	}

	if !state.BeginEdit("txt") {
		t.Fatal("editing an existing association failed")
	}
	*state.Extension() = "md"
	if !state.SelectKind(domain.OpenerSystem) || !state.Save() {
		t.Fatalf("saving the edited association failed: %s", state.Problem())
	}
	if _, ok := prefs.settings.SFTPEditor.AssociationFor("txt"); ok {
		t.Error("the old extension survived the edit")
	}
	if association, ok := prefs.settings.SFTPEditor.AssociationFor("md"); !ok ||
		association.Kind != domain.OpenerSystem {
		t.Errorf("edited association = %+v, %v", association, ok)
	}

	if !state.BeginEdit("md") {
		t.Fatal("editing the renamed association failed")
	}
	state.CancelEdit()
	if state.Editing() != "" || state.ExtensionValue() != "" {
		t.Error("CancelEdit did not clear the form")
	}

	if !state.Remove(".MD") {
		t.Fatal("removing the association failed")
	}
	if len(prefs.settings.SFTPEditor.FileAssociations) != 0 {
		t.Errorf("associations after removal = %+v", prefs.settings.SFTPEditor.FileAssociations)
	}
	// Removing an extension that has no association still succeeds; the list
	// simply does not change.
	if !state.Remove("missing") {
		t.Error("removing an unconfigured extension failed")
	}
	if state.Remove("") {
		t.Error("an empty extension was removed")
	}
}

func TestAssociationFormValidatesAndReportsSaveFailures(t *testing.T) {
	prefs := newFakePreferences()
	picker := &scriptedPicker{answers: []domain.ExternalApplication{
		application("/usr/bin/editor", "Editor"),
	}}
	state := NewAssociationsState(prefs, WithApplicationPicker(picker.pick))

	if state.Save() || state.Problem() != AssociationProblemInvalidExtension {
		t.Errorf("empty extension problem = %q", state.Problem())
	}
	*state.Extension() = "a/b"
	if state.Save() || state.Problem() != AssociationProblemInvalidExtension {
		t.Errorf("path extension problem = %q", state.Problem())
	}

	*state.Extension() = "txt"
	if !state.SelectKind(domain.OpenerExternal) {
		t.Fatal("selecting external failed")
	}
	if state.Save() || state.Problem() != AssociationProblemApplicationRequired {
		t.Errorf("external without application problem = %q", state.Problem())
	}
	if !state.ChooseAssociationApplication() {
		t.Fatal("choosing the association application failed")
	}
	if got := state.Application().Name; got != "Editor" {
		t.Errorf("chosen application = %q", got)
	}
	if !state.Save() {
		t.Fatalf("saving after choosing the application failed: %s", state.Problem())
	}
	association, _ := prefs.settings.SFTPEditor.AssociationFor("txt")
	if association.Kind != domain.OpenerExternal || association.Application.Path != "/usr/bin/editor" {
		t.Errorf("stored external association = %+v", association)
	}

	// A duplicate extension updates in place.
	*state.Extension() = "TXT"
	if !state.SelectKind(domain.OpenerSystem) || !state.Save() {
		t.Fatalf("replacing the association failed: %s", state.Problem())
	}
	if got := len(prefs.settings.SFTPEditor.FileAssociations); got != 1 {
		t.Errorf("associations after replacing = %d, want 1", got)
	}

	prefs.err = errors.New("disk full")
	*state.Extension() = "md"
	if state.Save() || state.Problem() != AssociationProblemSaveFailed {
		t.Errorf("save failure problem = %q", state.Problem())
	}
	if state.ExtensionValue() != "md" {
		t.Error("a failed save cleared the form")
	}
	if state.Remove("txt") {
		t.Error("a failed remove reported success")
	}
}

func TestAssociationsViewAddsEditsAndRemovesThroughTheControls(t *testing.T) {
	prefs := newFakePreferences()
	picker := &scriptedPicker{answers: []domain.ExternalApplication{
		application(`C:\Tools\viewer.exe`, "Viewer"),
	}}
	state := NewAssociationsState(prefs, WithApplicationPicker(picker.pick))
	tester := ui.NewTester(viewAt(AreaSFTPEditor, Config{
		Preferences:  NewPreferencesState(prefs),
		Associations: state,
	}), 1100, 1400)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	for _, label := range []string{
		m.SettingsDefaultOpener(),
		m.SettingsDefaultOpenerDescription(),
		m.SettingsFileAssociations(),
		m.SettingsFileAssociationsDescription(),
		m.SettingsAssociationEmpty(),
		m.SettingsAssociationAdd(),
	} {
		if !tester.HasText(label) {
			t.Errorf("the associations section does not show %q; texts %q", label, tester.Texts())
		}
	}

	if err := tester.Click(m.SettingsAssociationExtension()); err != nil {
		t.Fatalf("click the extension field: %v", err)
	}
	tester.Type("txt")
	tester.Frame()
	if got := state.ExtensionValue(); got != "txt" {
		t.Fatalf("typed extension = %q, want txt", got)
	}
	if err := tester.Click(m.SettingsAssociationAdd()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(".txt") || !tester.HasText(m.SettingsOpenerSystem()) {
		t.Errorf("the saved association is not listed; texts %q", tester.Texts())
	}

	if err := tester.Click(m.SettingsAssociationEdit()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAssociationEditing("txt")) ||
		!tester.HasText(m.SettingsAssociationSave()) {
		t.Errorf("the edit form did not open; texts %q", tester.Texts())
	}
	if err := tester.Click(m.SettingsCancel()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAssociationAdd()) {
		t.Error("cancelling the edit did not return to the add form")
	}

	if err := tester.Click(m.SettingsAssociationRemove()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAssociationEmpty()) {
		t.Errorf("the removed association is still listed; texts %q", tester.Texts())
	}
}

func TestAssociationsViewChoosesAnExternalApplicationAndResetsTheDefault(t *testing.T) {
	prefs := newFakePreferences()
	picker := &scriptedPicker{answers: []domain.ExternalApplication{
		application(`C:\Tools\viewer.exe`, "Viewer"),
		application(`C:\Tools\next.exe`, "Next"),
	}}
	state := NewAssociationsState(prefs, WithApplicationPicker(picker.pick))
	tester := ui.NewTester(viewAt(AreaSFTPEditor, Config{
		Preferences:  NewPreferencesState(prefs),
		Associations: state,
	}), 1100, 1400)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	// Selecting the external opener on the form shows its chooser, and the
	// chooser's result is what the form displays.
	if !state.SelectKind(domain.OpenerExternal) {
		t.Fatal("selecting the external kind failed")
	}
	tester.Frame()
	if !tester.HasText(m.SettingsChooseApplication()) {
		t.Fatalf("the external form has no chooser; texts %q", tester.Texts())
	}
	if err := tester.Click(m.SettingsChooseApplication()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText("Viewer") || state.Application().Name != "Viewer" {
		t.Errorf("the chosen application is not shown; texts %q", tester.Texts())
	}

	// The default opener's own chooser works the same way, and its reset
	// control restores the default without touching the form.
	if !state.SelectDefaultKind(domain.OpenerExternal) {
		t.Fatal("selecting the external default failed")
	}
	tester.Frame()
	if !tester.HasText("Next") {
		t.Errorf("the default application is not shown; texts %q", tester.Texts())
	}
	if err := tester.Click(m.SettingsReset()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if got := prefs.settings.SFTPEditor.DefaultOpener.Kind; got != domain.OpenerSystem {
		t.Errorf("default after the reset control = %q, want system", got)
	}
}

func TestAssociationsViewShowsValidationProblems(t *testing.T) {
	prefs := newFakePreferences()
	state := NewAssociationsState(prefs)
	tester := ui.NewTester(viewAt(AreaSFTPEditor, Config{
		Preferences:  NewPreferencesState(prefs),
		Associations: state,
	}), 1100, 1400)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsAssociationAdd()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAssociationInvalid()) {
		t.Errorf("an empty extension shows no problem; texts %q", tester.Texts())
	}

	*state.Extension() = "txt"
	state.SelectKind(domain.OpenerExternal)
	tester.Frame()
	if err := tester.Click(m.SettingsAssociationAdd()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAssociationNeedApp()) {
		t.Errorf("an external association without an application shows no problem; texts %q",
			tester.Texts())
	}
}

func TestAssociationsStateRedrawsAfterEveryChange(t *testing.T) {
	prefs := newFakePreferences()
	redraws := 0
	state := NewAssociationsState(prefs,
		WithAssociationsRedrawRequest(func() { redraws++ }))
	*state.Extension() = "txt"
	state.Save()
	state.BeginEdit("txt")
	state.CancelEdit()
	state.SelectKind(domain.OpenerInternal)
	state.Remove("txt")
	state.SelectDefaultKind(domain.OpenerInternal)
	state.ResetDefaultOpener()
	if redraws == 0 {
		t.Error("no change requested a redraw")
	}
}
