package settings

import (
	"strings"

	domain "github.com/mslxl/natcatty/domain/preferences"
)

// ApplicationPicker asks the user for one executable. The app wires the
// native open dialog; tests and the self-test script it.
type ApplicationPicker func() (domain.ExternalApplication, bool)

// AssociationProblem is the form's validation or save state.
type AssociationProblem string

const (
	AssociationProblemNone                AssociationProblem = ""
	AssociationProblemInvalidExtension    AssociationProblem = "invalid-extension"
	AssociationProblemApplicationRequired AssociationProblem = "application-required"
	AssociationProblemSaveFailed          AssociationProblem = "save-failed"
)

// AssociationsState owns the File associations section: the default opener,
// the per-extension list, and the add/edit form. It is mutated only on the UI
// thread.
type AssociationsState struct {
	prefs  Preferences
	pick   ApplicationPicker
	redraw func()

	extension   string
	kind        domain.OpenerKind
	application domain.ExternalApplication
	editing     string
	problem     AssociationProblem
}

// AssociationsOption customizes an AssociationsState.
type AssociationsOption func(*AssociationsState)

// WithAssociationsRedrawRequest sets the function called after a change.
func WithAssociationsRedrawRequest(fn func()) AssociationsOption {
	return func(s *AssociationsState) { s.redraw = fn }
}

// WithApplicationPicker supplies the executable chooser. Without one, the
// external opener cannot be selected and the error is reported in the form.
func WithApplicationPicker(pick ApplicationPicker) AssociationsOption {
	return func(s *AssociationsState) { s.pick = pick }
}

// NewAssociationsState returns the associations form for service.
func NewAssociationsState(service Preferences, options ...AssociationsOption) *AssociationsState {
	state := &AssociationsState{prefs: service, kind: domain.OpenerSystem}
	for _, option := range options {
		option(state)
	}
	return state
}

// DefaultOpener returns the persisted default, repaired to a usable opener.
func (s *AssociationsState) DefaultOpener() domain.Opener {
	return domain.NormalizeOpener(s.prefs.Settings().SFTPEditor.DefaultOpener)
}

// Associations returns a copy of the configured per-extension associations.
func (s *AssociationsState) Associations() []domain.FileAssociation {
	list := s.prefs.Settings().SFTPEditor.FileAssociations
	return append([]domain.FileAssociation(nil), list...)
}

// SelectDefaultKind persists the default opener kind. Choosing the external
// kind asks for an application first, because an external opener without one
// cannot open anything; cancelling the chooser leaves the setting unchanged.
func (s *AssociationsState) SelectDefaultKind(kind domain.OpenerKind) bool {
	if !domain.ValidOpenerKind(kind) {
		return false
	}
	opener := domain.Opener{Kind: kind}
	if kind == domain.OpenerExternal {
		application := s.DefaultOpener().Application
		if application.Path == "" {
			picked, ok := s.pickApplication()
			if !ok {
				return false
			}
			application = picked
		}
		opener.Application = application
	}
	return s.update(func(settings *domain.Settings) {
		settings.SFTPEditor.DefaultOpener = opener
	})
}

// ChooseDefaultApplication replaces the default's executable and switches the
// default to the external opener.
func (s *AssociationsState) ChooseDefaultApplication() bool {
	application, ok := s.pickApplication()
	if !ok {
		return false
	}
	return s.update(func(settings *domain.Settings) {
		settings.SFTPEditor.DefaultOpener = domain.Opener{
			Kind: domain.OpenerExternal, Application: application,
		}
	})
}

// ResetDefaultOpener restores the default opener's default without touching
// the per-extension associations.
func (s *AssociationsState) ResetDefaultOpener() bool {
	return s.update(func(settings *domain.Settings) {
		*settings = domain.ResetField(*settings, domain.FieldDefaultOpener)
	})
}

// Extension returns the form's bound extension input.
func (s *AssociationsState) Extension() *string { return &s.extension }

// ExtensionValue returns the form's current extension text.
func (s *AssociationsState) ExtensionValue() string { return s.extension }

// Kind returns the form's selected opener kind.
func (s *AssociationsState) Kind() domain.OpenerKind { return s.kind }

// Application returns the form's selected external application.
func (s *AssociationsState) Application() domain.ExternalApplication { return s.application }

// Editing returns the extension being edited, or "" when adding.
func (s *AssociationsState) Editing() string { return s.editing }

// Problem returns the form's current validation or save problem.
func (s *AssociationsState) Problem() AssociationProblem { return s.problem }

// SelectKind changes the form's opener kind. An external kind is saved only
// once an application is chosen.
func (s *AssociationsState) SelectKind(kind domain.OpenerKind) bool {
	if !domain.ValidOpenerKind(kind) {
		return false
	}
	s.kind = kind
	s.problem = AssociationProblemNone
	s.redrawState()
	return true
}

// ChooseAssociationApplication asks for the association form's executable and
// switches the form to the external opener.
func (s *AssociationsState) ChooseAssociationApplication() bool {
	application, ok := s.pickApplication()
	if !ok {
		return false
	}
	s.application = application
	s.kind = domain.OpenerExternal
	s.problem = AssociationProblemNone
	s.redrawState()
	return true
}

// BeginEdit loads an existing association into the form.
func (s *AssociationsState) BeginEdit(extension string) bool {
	association, ok := s.prefs.Settings().SFTPEditor.AssociationFor(extension)
	if !ok {
		return false
	}
	s.editing = association.Extension
	s.extension = association.Extension
	s.kind = association.Kind
	s.application = association.Application
	s.problem = AssociationProblemNone
	s.redrawState()
	return true
}

// CancelEdit clears the form back to "add".
func (s *AssociationsState) CancelEdit() {
	s.resetForm()
	s.redrawState()
}

// Remove deletes one association.
func (s *AssociationsState) Remove(extension string) bool {
	extension = domain.NormalizeExtension(extension)
	if extension == "" {
		return false
	}
	ok := s.update(func(settings *domain.Settings) {
		settings.SFTPEditor = settings.SFTPEditor.RemoveAssociation(extension)
	})
	if !ok {
		return false
	}
	if s.editing == extension {
		s.resetForm()
	}
	s.redrawState()
	return true
}

// Save adds the form's association, or replaces the extension being edited.
func (s *AssociationsState) Save() bool {
	extension := domain.NormalizeExtension(s.extension)
	if extension == "" {
		s.problem = AssociationProblemInvalidExtension
		s.redrawState()
		return false
	}
	if s.kind == domain.OpenerExternal && strings.TrimSpace(s.application.Path) == "" {
		s.problem = AssociationProblemApplicationRequired
		s.redrawState()
		return false
	}
	association := domain.FileAssociation{
		Extension: extension, Kind: s.kind, Application: s.application,
	}
	if _, ok := (domain.SFTPEditor{}).SetAssociation(association); !ok {
		s.problem = AssociationProblemInvalidExtension
		s.redrawState()
		return false
	}
	editing := s.editing
	ok := s.update(func(settings *domain.Settings) {
		editor := settings.SFTPEditor
		if editing != "" && editing != extension {
			editor = editor.RemoveAssociation(editing)
		}
		editor, _ = editor.SetAssociation(association)
		settings.SFTPEditor = editor
	})
	if !ok {
		return false
	}
	s.resetForm()
	s.redrawState()
	return true
}

func (s *AssociationsState) update(change func(*domain.Settings)) bool {
	if err := s.prefs.Update(change); err != nil {
		s.problem = AssociationProblemSaveFailed
		s.redrawState()
		return false
	}
	s.problem = AssociationProblemNone
	s.redrawState()
	return true
}

func (s *AssociationsState) pickApplication() (domain.ExternalApplication, bool) {
	if s.pick == nil {
		return domain.ExternalApplication{}, false
	}
	application, ok := s.pick()
	if !ok || strings.TrimSpace(application.Path) == "" {
		return domain.ExternalApplication{}, false
	}
	return application, true
}

func (s *AssociationsState) resetForm() {
	s.extension = ""
	s.kind = domain.OpenerSystem
	s.application = domain.ExternalApplication{}
	s.editing = ""
	s.problem = AssociationProblemNone
}

func (s *AssociationsState) redrawState() {
	if s.redraw != nil {
		s.redraw()
	}
}
