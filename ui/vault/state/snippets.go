package state

import (
	"context"
	"errors"
	"strings"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Snippets is the Snippet use-case seam. The application Service implements
// it; tests provide a fake.
type Snippets interface {
	ListSnippets(ctx context.Context) ([]domainvault.Snippet, error)
	CreateSnippet(ctx context.Context, draft domainvault.SnippetDraft) (domainvault.Snippet, error)
	UpdateSnippet(ctx context.Context, id string, draft domainvault.SnippetDraft) (domainvault.Snippet, error)
	DeleteSnippet(ctx context.Context, id string) error
	PrepareSnippet(ctx context.Context, id string, values map[string]string) (appvault.SnippetRun, error)
	SaveSnippetVariableValues(ctx context.Context, id string, values map[string]string) error

	ListSnippetPackages(ctx context.Context) ([]domainvault.SnippetPackage, error)
	CreateSnippetPackage(ctx context.Context, draft domainvault.SnippetPackageDraft) (domainvault.SnippetPackage, error)
	RenameSnippetPackage(ctx context.Context, path, newPath string) (domainvault.SnippetPackage, error)
	DeleteSnippetPackage(ctx context.Context, path string) error
}

// SnippetDestination selects where a run delivers.
type SnippetDestination string

const (
	// SnippetDestinationActive sends to the focused Session.
	SnippetDestinationActive SnippetDestination = "active"
	// SnippetDestinationHosts opens a Session for every Host the Snippet
	// targets and sends there.
	SnippetDestinationHosts SnippetDestination = "hosts"
)

// SnippetRunner is the UI's execution seam. The app wires it to the shell and
// Session service; tests record what it received.
type SnippetRunner interface {
	// HasActiveSession reports whether a Session is focused.
	HasActiveSession() bool
	// ActiveHostID reports the Host of the focused Session. A local Session
	// has no Host, so it reports false.
	ActiveHostID() (model.HostID, bool)
	// SendToActiveSession writes the resolved command to the focused Session.
	SendToActiveSession(snippet domainvault.Snippet, command string) error
	// SendToHosts opens a focused Session for each Host and writes the
	// resolved command.
	SendToHosts(hosts []domainvault.Host, snippet domainvault.Snippet, command string) error
}

// SnippetForm is the editable text of one Snippet.
type SnippetForm struct {
	ID               string
	Label            string
	Command          string
	Package          string
	Tags             string
	NoAutoRun        bool
	MultiLineRunMode string
	TargetsAllHosts  bool
	TargetHosts      map[string]bool
	TargetGroups     map[string]bool
}

// SnippetPackageForm is the editable path of one Snippet package.
type SnippetPackageForm struct {
	OriginalPath string
	Path         string
}

// SnippetRunVariable is one {{variable}} the run form asks for, in the order
// it first appears in the command.
type SnippetRunVariable struct {
	Name         string
	Value        string
	DefaultValue string
	HasDefault   bool
}

// SnippetRunForm holds the variable values for a pending run.
type SnippetRunForm struct {
	SnippetID   string
	Destination SnippetDestination
	Variables   []SnippetRunVariable
}

// Value returns the entered value for name, or "".
func (f *SnippetRunForm) Value(name string) string {
	for _, variable := range f.Variables {
		if variable.Name == name {
			return variable.Value
		}
	}
	return ""
}

// SetValue sets the entered value for name. An unknown name is ignored.
func (f *SnippetRunForm) SetValue(name, value string) {
	for index := range f.Variables {
		if f.Variables[index].Name == name {
			f.Variables[index].Value = value
			return
		}
	}
}

// values returns the entered values keyed by name.
func (f *SnippetRunForm) values() map[string]string {
	if f == nil || len(f.Variables) == 0 {
		return nil
	}
	values := make(map[string]string, len(f.Variables))
	for _, variable := range f.Variables {
		values[variable.Name] = variable.Value
	}
	return values
}

// WithSnippets gives the Vault surface Snippet management.
func WithSnippets(snippets Snippets) Option {
	return func(s *State) { s.snippets = snippets }
}

// WithSnippetRunner gives the Vault surface the execution seam.
func WithSnippetRunner(runner SnippetRunner) Option {
	return func(s *State) { s.snippetRunner = runner }
}

// Snippets returns the listed Snippets in repository order.
func (s *State) Snippets() []domainvault.Snippet { return s.snippetItems }

// SnippetCount returns the number of listed Snippets.
func (s *State) SnippetCount() int { return len(s.snippetItems) }

// SnippetPackages returns saved packages plus inferred ancestor paths.
func (s *State) SnippetPackages() []domainvault.SnippetPackage { return s.snippetPackageItems }

// SnippetPackageCount returns the number of visible package paths.
func (s *State) SnippetPackageCount() int { return len(s.snippetPackageItems) }

// SnippetByID returns one listed Snippet.
func (s *State) SnippetByID(id string) (domainvault.Snippet, bool) {
	for _, snippet := range s.snippetItems {
		if snippet.ID == id {
			return snippet, true
		}
	}
	return domainvault.Snippet{}, false
}

// SnippetPackageByPath returns one visible package.
func (s *State) SnippetPackageByPath(path string) (domainvault.SnippetPackage, bool) {
	for _, pkg := range s.snippetPackageItems {
		if pkg.Path == path {
			return pkg, true
		}
	}
	return domainvault.SnippetPackage{}, false
}

// SnippetsInPackage returns the Snippets directly in packagePath. The Vault
// root is "".
func (s *State) SnippetsInPackage(packagePath string) []domainvault.Snippet {
	packagePath = normalizeSnippetPackagePath(packagePath)
	inPackage := make([]domainvault.Snippet, 0, len(s.snippetItems))
	for _, snippet := range s.snippetItems {
		if normalizeSnippetPackagePath(snippet.Package) == packagePath {
			inPackage = append(inPackage, snippet)
		}
	}
	return inPackage
}

// SnippetTargetGroupPaths returns the group paths a Snippet editor can target:
// saved Host Groups plus paths Hosts reference without a saved record.
func (s *State) SnippetTargetGroupPaths() []string {
	seen := make(map[string]bool)
	paths := make([]string, 0, len(s.groupItems))
	add := func(path string) {
		path = domainvault.NormalizeGroup(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		paths = append(paths, path)
	}
	for _, group := range s.groupItems {
		add(group.Path)
	}
	for _, host := range s.items {
		add(host.Group)
	}
	return paths
}

// EditingSnippet reports whether the Snippet editor is open.
func (s *State) EditingSnippet() bool { return s.editingSnippet }

// SnippetForm returns the Snippet editor's form. It is valid while
// EditingSnippet.
func (s *State) SnippetForm() *SnippetForm { return &s.snippetForm }

// PendingDeleteSnippet returns the Snippet awaiting delete confirmation.
func (s *State) PendingDeleteSnippet() string { return s.pendingDeleteSnippet }

// BeginCreateSnippet opens the Snippet editor with an empty command.
func (s *State) BeginCreateSnippet() {
	s.closeEditors()
	s.editingSnippet = true
	s.snippetForm = SnippetForm{
		MultiLineRunMode: string(domainvault.SnippetRunPaste),
		TargetHosts:      make(map[string]bool),
		TargetGroups:     make(map[string]bool),
	}
	s.problem = ProblemNone
	s.redraw()
}

// BeginEditSnippet opens the Snippet editor with a listed Snippet.
func (s *State) BeginEditSnippet(id string) bool {
	snippet, ok := s.SnippetByID(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingSnippet = true
	mode := string(snippet.MultiLineRunMode)
	if mode == "" {
		mode = string(domainvault.SnippetRunPaste)
	}
	s.snippetForm = SnippetForm{
		ID:               snippet.ID,
		Label:            snippet.Label,
		Command:          snippet.Command,
		Package:          snippet.Package,
		Tags:             strings.Join(snippet.Tags, ", "),
		NoAutoRun:        snippet.NoAutoRun,
		MultiLineRunMode: mode,
		TargetsAllHosts:  snippet.TargetsAllHosts,
		TargetHosts:      make(map[string]bool, len(snippet.Targets)),
		TargetGroups:     make(map[string]bool, len(snippet.TargetGroups)),
	}
	for _, target := range snippet.Targets {
		s.snippetForm.TargetHosts[string(target)] = true
	}
	for _, group := range snippet.TargetGroups {
		s.snippetForm.TargetGroups[group] = true
	}
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CancelSnippetEdit closes the Snippet editor without saving.
func (s *State) CancelSnippetEdit() {
	s.editingSnippet = false
	s.snippetForm = SnippetForm{}
	s.problem = ProblemNone
	s.redraw()
}

// SaveSnippet validates and persists the Snippet form.
func (s *State) SaveSnippet() error {
	if s.snippets == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	draft := s.snippetDraft()
	var err error
	if s.snippetForm.ID == "" {
		_, err = s.snippets.CreateSnippet(context.Background(), draft)
	} else {
		_, err = s.snippets.UpdateSnippet(context.Background(), s.snippetForm.ID, draft)
	}
	if err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingSnippet = false
	s.snippetForm = SnippetForm{}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteSnippet opens the delete confirmation for one Snippet.
func (s *State) RequestDeleteSnippet(id string) {
	s.pendingDeleteSnippet = id
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteSnippet closes the Snippet delete confirmation.
func (s *State) CancelDeleteSnippet() {
	s.pendingDeleteSnippet = ""
	s.redraw()
}

// ConfirmDeleteSnippet deletes the Snippet awaiting confirmation.
func (s *State) ConfirmDeleteSnippet() error {
	if s.pendingDeleteSnippet == "" {
		return nil
	}
	id := s.pendingDeleteSnippet
	s.pendingDeleteSnippet = ""
	if s.snippets == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	if err := s.snippets.DeleteSnippet(context.Background(), id); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// EditingSnippetPackage reports whether the package editor is open.
func (s *State) EditingSnippetPackage() bool { return s.editingSnippetPackage }

// SnippetPackageForm returns the package editor's form.
func (s *State) SnippetPackageForm() *SnippetPackageForm { return &s.snippetPackageForm }

// PendingDeleteSnippetPackage returns the package awaiting confirmation.
func (s *State) PendingDeleteSnippetPackage() string { return s.pendingDeleteSnippetPackage }

// BeginCreateSnippetPackage opens the package editor with a new path.
func (s *State) BeginCreateSnippetPackage() {
	s.closeEditors()
	s.editingSnippetPackage = true
	s.snippetPackageForm = SnippetPackageForm{}
	s.problem = ProblemNone
	s.redraw()
}

// BeginEditSnippetPackage opens the package editor for a rename.
func (s *State) BeginEditSnippetPackage(path string) bool {
	pkg, ok := s.SnippetPackageByPath(path)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingSnippetPackage = true
	s.snippetPackageForm = SnippetPackageForm{OriginalPath: pkg.Path, Path: pkg.Path}
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CancelSnippetPackageEdit closes the package editor without saving.
func (s *State) CancelSnippetPackageEdit() {
	s.editingSnippetPackage = false
	s.snippetPackageForm = SnippetPackageForm{}
	s.problem = ProblemNone
	s.redraw()
}

// SaveSnippetPackage creates or renames the edited package.
func (s *State) SaveSnippetPackage() error {
	if s.snippets == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	var err error
	if s.snippetPackageForm.OriginalPath == "" {
		_, err = s.snippets.CreateSnippetPackage(context.Background(),
			domainvault.SnippetPackageDraft{Path: s.snippetPackageForm.Path})
	} else {
		_, err = s.snippets.RenameSnippetPackage(context.Background(),
			s.snippetPackageForm.OriginalPath, s.snippetPackageForm.Path)
	}
	if err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingSnippetPackage = false
	s.snippetPackageForm = SnippetPackageForm{}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteSnippetPackage opens the delete confirmation for one package.
func (s *State) RequestDeleteSnippetPackage(path string) {
	s.pendingDeleteSnippetPackage = path
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteSnippetPackage closes the package delete confirmation.
func (s *State) CancelDeleteSnippetPackage() {
	s.pendingDeleteSnippetPackage = ""
	s.redraw()
}

// ConfirmDeleteSnippetPackage deletes the package awaiting confirmation. Its
// Snippets are kept and move to the Vault root, as the original non-cascading
// delete does.
func (s *State) ConfirmDeleteSnippetPackage() error {
	if s.pendingDeleteSnippetPackage == "" {
		return nil
	}
	path := s.pendingDeleteSnippetPackage
	s.pendingDeleteSnippetPackage = ""
	if s.snippets == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	if err := s.snippets.DeleteSnippetPackage(context.Background(), path); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// SnippetRunOpen reports whether the variable form is open.
func (s *State) SnippetRunOpen() bool { return s.snippetRun != nil }

// SnippetRunForm returns the variable form, or nil when no run is pending.
func (s *State) SnippetRunForm() *SnippetRunForm { return s.snippetRun }

// SnippetRunMissing returns the required variables left blank.
func (s *State) SnippetRunMissing() []string { return s.snippetRunMissing }

// SnippetRunProblem returns the failure the run form or the last run recorded.
func (s *State) SnippetRunProblem() Problem { return s.snippetRunProblem }

// SnippetRunPreview renders the pending command with the entered values.
func (s *State) SnippetRunPreview() string {
	if s.snippetRun == nil {
		return ""
	}
	snippet, ok := s.SnippetByID(s.snippetRun.SnippetID)
	if !ok {
		return ""
	}
	return domainvault.PreviewSnippetCommand(snippet.Command, s.snippetRun.values())
}

// BeginSnippetRun starts a run. A Snippet without variables executes
// immediately; one with variables opens the form so substitution happens
// before anything reaches a Session.
func (s *State) BeginSnippetRun(id string, destination SnippetDestination) bool {
	snippet, ok := s.SnippetByID(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	defs := domainvault.ParseSnippetVariables(snippet.Command)
	if len(defs) == 0 {
		return s.executeSnippet(snippet, destination, nil)
	}
	variables := make([]SnippetRunVariable, 0, len(defs))
	for _, def := range defs {
		variable := SnippetRunVariable{
			Name:         def.Name,
			DefaultValue: def.DefaultValue,
			HasDefault:   def.HasDefault,
		}
		if value, ok := snippet.VariableValues[def.Name]; ok {
			variable.Value = value
		} else if def.HasDefault {
			variable.Value = def.DefaultValue
		}
		variables = append(variables, variable)
	}
	s.snippetRun = &SnippetRunForm{SnippetID: id, Destination: destination, Variables: variables}
	s.snippetRunMissing = nil
	s.snippetRunProblem = ProblemNone
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CancelSnippetRun closes the variable form without running.
func (s *State) CancelSnippetRun() {
	s.snippetRun = nil
	s.snippetRunMissing = nil
	s.snippetRunProblem = ProblemNone
	s.redraw()
}

// ConfirmSnippetRun resolves the pending variables and runs the Snippet.
// A missing required variable keeps the form open and names every missing
// variable; the command never reaches a Session.
func (s *State) ConfirmSnippetRun() bool {
	if s.snippetRun == nil {
		return false
	}
	form := s.snippetRun
	snippet, ok := s.SnippetByID(form.SnippetID)
	if !ok {
		s.snippetRunProblem = ProblemNotFound
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	return s.executeSnippet(snippet, form.Destination, form.values())
}

func (s *State) executeSnippet(snippet domainvault.Snippet, destination SnippetDestination, values map[string]string) bool {
	if s.snippets == nil {
		s.problem = ProblemInternal
		s.snippetRunProblem = ProblemInternal
		s.redraw()
		return false
	}
	run, err := s.snippets.PrepareSnippet(context.Background(), snippet.ID, values)
	if err != nil {
		var variables *appvault.SnippetVariableError
		if errors.As(err, &variables) {
			s.snippetRunMissing = append([]string(nil), variables.Missing...)
			s.snippetRunProblem = ProblemSnippetMissingVariables
			s.problem = ProblemSnippetMissingVariables
			s.redraw()
			return false
		}
		s.snippetRunProblem = problemFor(err)
		s.problem = s.snippetRunProblem
		s.redraw()
		return false
	}

	if err := s.snippets.SaveSnippetVariableValues(context.Background(), snippet.ID, values); err != nil {
		s.snippetRunProblem = problemFor(err)
		s.problem = s.snippetRunProblem
		s.redraw()
		return false
	}

	if s.snippetRunner == nil {
		s.snippetRunProblem = ProblemSnippetNoSession
		s.problem = ProblemSnippetNoSession
		s.redraw()
		return false
	}

	switch destination {
	case SnippetDestinationHosts:
		if len(run.Hosts) == 0 {
			s.snippetRunProblem = ProblemSnippetTargets
			s.problem = ProblemSnippetTargets
			s.redraw()
			return false
		}
		err = s.snippetRunner.SendToHosts(run.Hosts, run.Snippet, run.Command)
	default:
		if !s.snippetRunner.HasActiveSession() {
			s.snippetRunProblem = ProblemSnippetNoSession
			s.problem = ProblemSnippetNoSession
			s.redraw()
			return false
		}
		if !s.snippetRunsInActiveSession(run.Snippet) {
			s.snippetRunProblem = ProblemSnippetTargets
			s.problem = ProblemSnippetTargets
			s.redraw()
			return false
		}
		err = s.snippetRunner.SendToActiveSession(run.Snippet, run.Command)
	}
	if err != nil {
		s.snippetRunProblem = ProblemSnippetRunFailed
		s.problem = ProblemSnippetRunFailed
		s.redraw()
		return false
	}

	s.snippetRun = nil
	s.snippetRunMissing = nil
	s.snippetRunProblem = ProblemNone
	s.problem = ProblemNone
	_ = s.load()
	s.redraw()
	return true
}

// snippetRunsInActiveSession reports whether the focused Session's Host is
// inside the Snippet's targets. An unscoped Snippet runs anywhere.
func (s *State) snippetRunsInActiveSession(snippet domainvault.Snippet) bool {
	if !domainvault.SnippetHasRunTargets(snippet) {
		return true
	}
	id, ok := s.snippetRunner.ActiveHostID()
	if !ok {
		return false
	}
	host := domainvault.SnippetTargetHost{ID: id}
	if listed, found := s.find(id); found {
		host.Group = listed.Group
	}
	return domainvault.SnippetCanRunInTerminal(snippet, host)
}

func (s *State) snippetDraft() domainvault.SnippetDraft {
	draft := domainvault.SnippetDraft{
		Label:            s.snippetForm.Label,
		Command:          s.snippetForm.Command,
		Package:          s.snippetForm.Package,
		Tags:             splitCommaSeparated(s.snippetForm.Tags),
		TargetsAllHosts:  s.snippetForm.TargetsAllHosts,
		NoAutoRun:        s.snippetForm.NoAutoRun,
		MultiLineRunMode: domainvault.SnippetMultiLineRunMode(s.snippetForm.MultiLineRunMode),
	}
	if s.snippetForm.TargetsAllHosts {
		return draft
	}
	for _, host := range s.items {
		if s.snippetForm.TargetHosts[string(host.ID)] {
			draft.Targets = append(draft.Targets, host.ID)
		}
	}
	for _, path := range s.SnippetTargetGroupPaths() {
		if s.snippetForm.TargetGroups[path] {
			draft.TargetGroups = append(draft.TargetGroups, path)
		}
	}
	return draft
}

func splitCommaSeparated(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// normalizeSnippetPackagePath folds a stored or edited path to its display
// form. An invalid path is returned as typed, so it never silently disappears
// from the list.
func normalizeSnippetPackagePath(path string) string {
	normalized, err := domainvault.NormalizeSnippetPackagePath(path)
	if err != nil {
		return path
	}
	return normalized
}
