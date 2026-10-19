package vault

import (
	"strings"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// IdentityAuthMethod is how an Identity authenticates.
type IdentityAuthMethod string

const (
	// IdentityAuthPassword authenticates with a password.
	IdentityAuthPassword IdentityAuthMethod = "password"
	// IdentityAuthKey authenticates with a selected SSH Key.
	IdentityAuthKey IdentityAuthMethod = "key"
	// IdentityAuthCertificate authenticates with a signed certificate.
	IdentityAuthCertificate IdentityAuthMethod = "certificate"
)

// Identity is a named authentication identity that can be attached to one or
// more Hosts. A password is never stored on this struct; the Vault encrypts it
// into the field-secret store and this record carries HasPassword.
type Identity struct {
	ID          model.IdentityID   `json:"id"`
	Label       string             `json:"label"`
	Username    string             `json:"username"`
	AuthMethod  IdentityAuthMethod `json:"authMethod"`
	KeyID       model.KeyID        `json:"keyId,omitempty"`
	HasPassword bool               `json:"hasPassword,omitempty"`
	OrderKey    int64              `json:"orderKey"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
}

// IdentityDraft is the editable part of an Identity. Password is plaintext
// form input; the Vault encrypts it and never copies it onto Identity.
type IdentityDraft struct {
	Label      string
	Username   string
	AuthMethod IdentityAuthMethod
	KeyID      model.KeyID
	Password   string
	// ClearPassword removes a stored password on Update. An empty Password
	// alone keeps the stored credential.
	ClearPassword bool
}

// NormalizeIdentityDraft trims input, fills defaults, and validates it.
func NormalizeIdentityDraft(draft IdentityDraft) (IdentityDraft, error) {
	draft.Label = strings.TrimSpace(draft.Label)
	draft.Username = strings.TrimSpace(draft.Username)
	draft.AuthMethod = IdentityAuthMethod(strings.ToLower(strings.TrimSpace(string(draft.AuthMethod))))
	if draft.AuthMethod == "" {
		draft.AuthMethod = IdentityAuthPassword
	}
	if draft.Label == "" {
		return IdentityDraft{}, validation(FieldLabel, RuleRequired)
	}
	if err := validateText(FieldLabel, draft.Label, maxLabelLength, false); err != nil {
		return IdentityDraft{}, err
	}
	switch draft.AuthMethod {
	case IdentityAuthPassword:
		// A password Identity has no key selection; keeping a stale reference
		// would make the UI claim a key that will never be used.
		draft.KeyID = ""
		if draft.Username == "" {
			return IdentityDraft{}, validation(FieldUsername, RuleRequired)
		}
	case IdentityAuthKey, IdentityAuthCertificate:
		if draft.KeyID == "" {
			return IdentityDraft{}, validation(FieldKey, RuleRequired)
		}
	default:
		return IdentityDraft{}, validation(FieldAuthMethod, RuleUnsupported)
	}
	if err := validateText(FieldUsername, draft.Username, maxUsernameLength, false); err != nil {
		return IdentityDraft{}, err
	}
	return draft, nil
}
