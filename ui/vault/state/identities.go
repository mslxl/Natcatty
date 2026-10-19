package state

import (
	"context"
	"errors"
	"strings"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// IdentityForm is the editable text of one Identity. Password is plaintext
// form input only; the application encrypts it and never renders it back.
type IdentityForm struct {
	ID            model.IdentityID
	Label         string
	Username      string
	AuthMethod    string
	KeyID         string
	Password      string
	ClearPassword bool
	HasPassword   bool
}

// EditingIdentity reports whether the Identity editor is open.
func (s *State) EditingIdentity() bool { return s.editingIdentity }

// IdentityForm returns the Identity editor's form. It is valid while
// EditingIdentity.
func (s *State) IdentityForm() *IdentityForm { return &s.identityForm }

// Identities returns the listed Identities in repository order.
func (s *State) Identities() []domainvault.Identity { return s.identityItems }

// IdentityCount returns the number of listed Identities.
func (s *State) IdentityCount() int { return len(s.identityItems) }

// PendingDeleteIdentity returns the Identity awaiting delete confirmation.
func (s *State) PendingDeleteIdentity() model.IdentityID { return s.pendingDeleteIdentity }

// BeginCreateIdentity opens the Identity editor with a password method, the
// original default.
func (s *State) BeginCreateIdentity() {
	s.closeEditors()
	s.editingIdentity = true
	s.identityForm = IdentityForm{AuthMethod: string(domainvault.IdentityAuthPassword)}
	s.redraw()
}

// BeginEditIdentity opens the Identity editor with a listed Identity's
// non-secret values.
func (s *State) BeginEditIdentity(id model.IdentityID) bool {
	identity, ok := s.findIdentity(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingIdentity = true
	s.identityForm = IdentityForm{
		ID:          identity.ID,
		Label:       identity.Label,
		Username:    identity.Username,
		AuthMethod:  string(identity.AuthMethod),
		KeyID:       string(identity.KeyID),
		HasPassword: identity.HasPassword,
	}
	s.redraw()
	return true
}

// CancelIdentityEdit closes the Identity editor without saving.
func (s *State) CancelIdentityEdit() {
	s.editingIdentity = false
	s.identityForm = IdentityForm{}
	s.redraw()
}

// SaveIdentity validates and persists the Identity form.
func (s *State) SaveIdentity() error {
	if s.identities == nil {
		s.problem = ProblemSecretsUnavailable
		s.redraw()
		return nil
	}
	draft := domainvault.IdentityDraft{
		Label:         s.identityForm.Label,
		Username:      s.identityForm.Username,
		AuthMethod:    domainvault.IdentityAuthMethod(strings.ToLower(s.identityForm.AuthMethod)),
		KeyID:         model.KeyID(s.identityForm.KeyID),
		Password:      s.identityForm.Password,
		ClearPassword: s.identityForm.ClearPassword,
	}
	var err error
	if s.identityForm.ID == "" {
		_, err = s.identities.CreateIdentity(context.Background(), draft)
	} else {
		_, err = s.identities.UpdateIdentity(context.Background(), s.identityForm.ID, draft)
	}
	if err != nil {
		s.problem = identityProblemFor(err, draft)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingIdentity = false
	s.identityForm = IdentityForm{}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteIdentity opens the confirmation for one Identity.
func (s *State) RequestDeleteIdentity(id model.IdentityID) {
	s.pendingDeleteIdentity = id
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteIdentity closes the Identity delete confirmation.
func (s *State) CancelDeleteIdentity() {
	s.pendingDeleteIdentity = ""
	s.redraw()
}

// ConfirmDeleteIdentity deletes the Identity awaiting confirmation. Host
// references are deliberately left dangling.
func (s *State) ConfirmDeleteIdentity() error {
	if s.pendingDeleteIdentity == "" {
		return nil
	}
	id := s.pendingDeleteIdentity
	s.pendingDeleteIdentity = ""
	if s.identities == nil {
		s.problem = ProblemSecretsUnavailable
		s.redraw()
		return nil
	}
	if err := s.identities.DeleteIdentity(context.Background(), id); err != nil {
		s.problem = identityProblemFor(err, domainvault.IdentityDraft{})
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

func (s *State) findIdentity(id model.IdentityID) (domainvault.Identity, bool) {
	for _, identity := range s.identityItems {
		if identity.ID == id {
			return identity, true
		}
	}
	return domainvault.Identity{}, false
}

func identityProblemFor(err error, draft domainvault.IdentityDraft) Problem {
	var validation *domainvault.ValidationError
	if errors.As(err, &validation) {
		switch validation.Field {
		case domainvault.FieldKey:
			return ProblemIdentityKeyRequired
		case domainvault.FieldUsername:
			if validation.Rule == domainvault.RuleRequired {
				return ProblemIdentityUsernameRequired
			}
			return ProblemUsername
		case domainvault.FieldAuthMethod:
			return ProblemAuthMethod
		case domainvault.FieldLabel:
			if validation.Rule == domainvault.RuleRequired {
				return ProblemLabelRequired
			}
			return ProblemLabelInvalid
		}
	}
	if draft.AuthMethod == domainvault.IdentityAuthKey || draft.AuthMethod == domainvault.IdentityAuthCertificate {
		if draft.KeyID == "" {
			return ProblemIdentityKeyRequired
		}
	}
	return problemFor(err)
}
