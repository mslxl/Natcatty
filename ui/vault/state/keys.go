package state

import (
	"context"
	"errors"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// KeyForm is the editable text of one SSH Key. PrivateKey and Passphrase are
// plaintext form input only; the application encrypts them and never renders
// them back.
type KeyForm struct {
	ID               model.KeyID
	Label            string
	Type             domainvault.KeyType
	PrivateKey       string
	Passphrase       string
	SavePassphrase   bool
	Certificate      string
	ClearPassphrase  bool
	ClearCertificate bool
	HasPrivateKey    bool
	HasPassphrase    bool
	HasCertificate   bool
}

// EditingKey reports whether the SSH Key editor is open.
func (s *State) EditingKey() bool { return s.editingKey }

// KeyForm returns the SSH Key editor's form. It is valid while EditingKey.
func (s *State) KeyForm() *KeyForm { return &s.keyForm }

// Keys returns the listed SSH Keys in repository order.
func (s *State) Keys() []domainvault.Key { return s.keyItems }

// KeyCount returns the number of listed SSH Keys.
func (s *State) KeyCount() int { return len(s.keyItems) }

// PendingDeleteKey returns the SSH Key awaiting delete confirmation, or "".
func (s *State) PendingDeleteKey() model.KeyID { return s.pendingDeleteKey }

// BeginCreateKey opens the SSH Key editor with empty material.
func (s *State) BeginCreateKey() {
	s.closeEditors()
	s.editingKey = true
	s.keyForm = KeyForm{SavePassphrase: true}
	s.redraw()
}

// BeginEditKey opens the SSH Key editor with a listed Key's non-secret values.
// Key material and the passphrase are never loaded back into the form.
func (s *State) BeginEditKey(id model.KeyID) bool {
	key, ok := s.findKey(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingKey = true
	s.keyForm = KeyForm{
		ID:             key.ID,
		Label:          key.Label,
		Type:           key.Type,
		Certificate:    key.Certificate,
		HasPrivateKey:  key.HasPrivateKey,
		HasPassphrase:  key.HasPassphrase,
		HasCertificate: key.Certificate != "",
	}
	s.redraw()
	return true
}

// CancelKeyEdit closes the SSH Key editor without saving.
func (s *State) CancelKeyEdit() {
	s.editingKey = false
	s.keyForm = KeyForm{}
	s.redraw()
}

// SaveKey validates and persists the SSH Key form. Failures keep the editor
// open so the user can correct the input.
func (s *State) SaveKey() error {
	if s.keys == nil {
		s.problem = ProblemSecretsUnavailable
		s.redraw()
		return nil
	}
	draft := domainvault.KeyDraft{
		Label:            s.keyForm.Label,
		PrivateKey:       s.keyForm.PrivateKey,
		Passphrase:       s.keyForm.Passphrase,
		SavePassphrase:   s.keyForm.SavePassphrase,
		Certificate:      s.keyForm.Certificate,
		ClearPassphrase:  s.keyForm.ClearPassphrase,
		ClearCertificate: s.keyForm.ClearCertificate,
	}
	var err error
	if s.keyForm.ID == "" {
		_, err = s.keys.CreateKey(context.Background(), draft)
	} else {
		_, err = s.keys.UpdateKey(context.Background(), s.keyForm.ID, draft)
	}
	if err != nil {
		s.problem = keyProblemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingKey = false
	s.keyForm = KeyForm{}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteKey opens the confirmation for one SSH Key.
func (s *State) RequestDeleteKey(id model.KeyID) {
	s.pendingDeleteKey = id
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteKey closes the SSH Key delete confirmation.
func (s *State) CancelDeleteKey() {
	s.pendingDeleteKey = ""
	s.redraw()
}

// ConfirmDeleteKey deletes the SSH Key awaiting confirmation. Host and
// Identity references are deliberately left dangling.
func (s *State) ConfirmDeleteKey() error {
	if s.pendingDeleteKey == "" {
		return nil
	}
	id := s.pendingDeleteKey
	s.pendingDeleteKey = ""
	if s.keys == nil {
		s.problem = ProblemSecretsUnavailable
		s.redraw()
		return nil
	}
	if err := s.keys.DeleteKey(context.Background(), id); err != nil {
		s.problem = keyProblemFor(err)
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

func (s *State) findKey(id model.KeyID) (domainvault.Key, bool) {
	for _, key := range s.keyItems {
		if key.ID == id {
			return key, true
		}
	}
	return domainvault.Key{}, false
}

// closeEditors closes every Vault editor, so only one form is ever shown.
func (s *State) closeEditors() {
	s.editing = false
	s.editingKey = false
	s.editingIdentity = false
	s.problem = ProblemNone
	s.pendingDelete = ""
	s.pendingDeleteKey = ""
	s.pendingDeleteIdentity = ""
	s.closeEntityEditors()
}

// closeEntityEditors closes the Host Group and Proxy Profile editors and their
// pending confirmations. It is separate so the Host editor can reset them
// without clearing its own open state.
func (s *State) closeEntityEditors() {
	s.editingGroup = false
	s.groupForm = GroupForm{}
	s.pendingDeleteGroup = ""
	s.editingProxy = false
	s.proxyForm = ProxyForm{}
	s.pendingDeleteProxy = ""
	s.pendingDeleteKnown = ""
}

func keyProblemFor(err error) Problem {
	var validation *domainvault.ValidationError
	if errors.As(err, &validation) {
		switch validation.Field {
		case domainvault.FieldKeyMaterial, domainvault.FieldPrivateKey:
			return ProblemKeyMaterial
		case domainvault.FieldPassphrase:
			return ProblemKeyPassphrase
		case domainvault.FieldCertificate:
			return ProblemKeyCertificate
		case domainvault.FieldKeyType:
			return ProblemKeyUnsupported
		case domainvault.FieldKey:
			return ProblemKeyRequired
		}
	}
	if errors.Is(err, appvault.ErrKeyPassphraseInvalid) {
		return ProblemKeyPassphrase
	}
	return problemFor(err)
}
