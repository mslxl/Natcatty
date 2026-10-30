// Package section names the pages of the Vault. The shell's sidebar chooses
// one and the Vault surface draws it, as the original's Vault sidebar did, so
// neither package needs the other.
package section

// Section is one page of the Vault.
type Section string

const (
	// All draws every family on one page, for a Vault with no sidebar.
	All Section = ""
	// Hosts lists the Host Groups and Hosts.
	Hosts Section = "hosts"
	// Keychain lists the SSH Keys and Identities.
	Keychain Section = "keychain"
	// Proxies lists the Proxy Profiles.
	Proxies Section = "proxies"
	// Forwarding lists the Port Forwarding Rules.
	Forwarding Section = "forwarding"
	// Snippets lists the Snippets and their packages.
	Snippets Section = "snippets"
	// Notes lists the Notes and their groups.
	Notes Section = "notes"
	// KnownHosts lists the trusted host keys.
	KnownHosts Section = "known-hosts"
)

// Pages returns the Vault's pages in sidebar order.
func Pages() []Section {
	return []Section{Hosts, Keychain, Proxies, Forwarding, Snippets, Notes, KnownHosts}
}

// Shows reports whether the page current draws the family of page s.
func (current Section) Shows(s Section) bool { return current == All || current == s }
