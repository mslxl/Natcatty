package vault

import (
	"strings"
	"time"
)

// ProxyProfile is a saved proxy configuration usable by Hosts and Host Groups.
// It is a Vault record of its own so one proxy definition can be shared; a
// Host or Host Group holds only a soft reference, which the Vault service
// clears when the profile is deleted.
type ProxyProfile struct {
	ID     string      `json:"id"`
	Label  string      `json:"label"`
	Config ProxyConfig `json:"config"`
	// OrderKey orders profiles in the Vault, in steps of 1000.
	OrderKey  int64     `json:"orderKey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProxyProfileDraft is the editable part of a Proxy Profile.
type ProxyProfileDraft struct {
	Label  string
	Config ProxyConfig
}

// NormalizeProxyProfileDraft trims and validates a Proxy Profile. A command
// profile needs only a command; an HTTP or SOCKS5 profile needs a host and a
// port.
func NormalizeProxyProfileDraft(draft ProxyProfileDraft) (ProxyProfileDraft, error) {
	draft.Label = strings.TrimSpace(draft.Label)
	config := normalizeProxy(&draft.Config)
	if config == nil {
		return ProxyProfileDraft{}, validation(FieldProxy, RuleRequired)
	}
	draft.Config = *config

	if draft.Label == "" {
		return ProxyProfileDraft{}, validation(FieldLabel, RuleRequired)
	}
	if err := validateText(FieldLabel, draft.Label, maxLabelLength, false); err != nil {
		return ProxyProfileDraft{}, err
	}
	switch draft.Config.Type {
	case ProxyCommand:
		if draft.Config.Host != "" || draft.Config.Port != 0 ||
			draft.Config.Username != "" || draft.Config.Password != "" {
			return ProxyProfileDraft{}, validation(FieldProxy, RuleInvalid)
		}
	case ProxyHTTP, ProxySOCKS5:
		if draft.Config.Command != "" {
			return ProxyProfileDraft{}, validation(FieldProxy, RuleInvalid)
		}
	default:
		return ProxyProfileDraft{}, validation(FieldProxy, RuleUnsupported)
	}
	if err := validateProxy(&draft.Config); err != nil {
		return ProxyProfileDraft{}, err
	}
	return draft, nil
}
