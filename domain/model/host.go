package model

// HostID identifies a Host for its lifetime. It is opaque: callers may
// compare and display it, never parse it.
type HostID string

// KeyID identifies an SSH Key for its lifetime. It is opaque: callers may
// compare and display it, never parse it.
type KeyID string

// IdentityID identifies an Identity for its lifetime. It is opaque: callers
// may compare and display it, never parse it.
type IdentityID string

// ForwardingRuleID identifies a Port Forwarding Rule for its lifetime. It is
// opaque: callers may compare and display it, never parse it.
type ForwardingRuleID string
