package phoneshell

import (
	"context"
	"errors"
	"strings"

	launch "github.com/mslxl/natcatty/domain/model/launch"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// HostCreator creates a Host from a Draft. The application Vault service
// implements it over the shared SQLite store, so a Host created from a link is
// the same record the desktop lists and syncs.
type HostCreator interface {
	Create(ctx context.Context, draft domainvault.Draft) (domainvault.Host, error)
}

// WithHostCreator gives the Hosts section the Vault write surface an unmatched
// deep link needs. Without it the create form still opens but reports that the
// build cannot create Hosts instead of failing silently.
func WithHostCreator(creator HostCreator) Option {
	return func(s *Shell) { s.hosts.create = creator }
}

// createHostPath is the Hosts section's create form.
const createHostPath = "/host/new"

// HandleDeepLink applies one ssh:// URL that the OS handed the app. Before the
// shell has loaded and shown, the link is buffered and applied by Ready: a
// cold start can deliver the link before the Vault list is readable, and
// matching against an empty list would offer to create a Host that exists.
//
// A link delivered before Ready returns nil because it is only buffered; the
// rejection of a buffered link is discovered and shown by Ready instead. Once
// ready, the returned error is the link's rejection (malformed or unsupported),
// and it is also recorded as an inline problem so the user sees why nothing
// opened.
func (s *Shell) HandleDeepLink(raw string) error {
	if !s.ready {
		s.pendingLinks = append(s.pendingLinks, raw)
		return nil
	}
	return s.applyDeepLink(raw)
}

// Ready marks the shell loaded and shown, and applies the links that arrived
// while it was starting. The phone root calls it after Load and Show; calling
// it again delivers only links buffered since the last call.
func (s *Shell) Ready() {
	s.ready = true
	pending := s.pendingLinks
	s.pendingLinks = nil
	for _, raw := range pending {
		_ = s.applyDeepLink(raw)
	}
}

// applyDeepLink is the one place a link changes navigation. A valid link
// either opens or selects the matching Host's Tab, or moves the Hosts section
// to a prefilled create form. A rejected link changes nothing but the inline
// problem, so a malformed link never leaves the user on a blank page.
func (s *Shell) applyDeepLink(raw string) error {
	target, err := launch.ParseDeepLink(raw)
	if err != nil {
		s.reportDeepLinkProblem(err)
		return err
	}
	// Only ssh:// reaches this app: it is the scheme registered on both
	// platforms, and the phone's Hosts all open over a remote transport.
	if target.Kind != launch.TargetSSH {
		s.reportDeepLinkProblem(launch.ErrUnsupportedScheme)
		return launch.ErrUnsupportedScheme
	}
	s.deepLinkProblem = ""

	if host, ok := s.hosts.matchEffectiveHost(target); ok {
		// A link names an existing endpoint: open that record in the editor.
		// Saving is the explicit step; connecting only follows a second tap.
		s.beginDeepLinkHostEdit(host)
		return nil
	}
	s.promptToCreateHost(target)
	return nil
}

// matchEffectiveHost finds the one saved Host a link names, using the same
// effective connection path a Session would use. A Host that inherits its
// non-default port or username from a Host Group therefore matches the link
// that names that effective endpoint, not only its raw record.
func (h *hostPages) matchEffectiveHost(target launch.Target) (domainvault.Host, bool) {
	effective := make([]domainvault.Host, 0, len(h.hosts))
	for _, host := range h.hosts {
		effective = append(effective, h.effectiveHost(host))
	}
	matched, ok := domainvault.MatchHost(effective, target)
	if !ok {
		return domainvault.Host{}, false
	}
	for _, host := range h.hosts {
		if host.ID == matched.ID {
			return host, true
		}
	}
	return domainvault.Host{}, false
}

// promptToCreateHost is the link's unmatched path: an ssh:// link names an
// endpoint the Vault does not have, so the Hosts section opens the editor
// prefilled with the link's user, host, and port. Here Save and connect leads,
// because opening the link's address is what the user asked for.
func (s *Shell) promptToCreateHost(target launch.Target) {
	editor := s.hosts.editor
	if editor == nil {
		s.deepLinkProblem = "This build cannot open the Host editor."
		return
	}
	draft := domainvault.DraftHost(target)
	editor.beginCreate(&draft, true)
	s.Select(Hosts)
	pushHostEditorRoute(s, createHostPath)
}

// pushHostEditorRoute never stacks editors. A second link, or a link that
// arrives while an editor is open, replaces that route, so Cancel returns to
// the page the editor was opened from rather than to a draft it has already
// discarded.
func pushHostEditorRoute(s *Shell, route string) {
	router := s.sections[Hosts].router
	if route == router.Path() || isHostEditorPath(router.Path()) {
		router.Replace(route)
		return
	}
	router.Push(route)
}

func isHostEditorPath(path string) bool {
	return path == createHostPath || strings.HasSuffix(path, "/edit")
}

// beginNewHostEditor opens the manual create form. Saving leads; connecting is
// the secondary action.
func (s *Shell) beginNewHostEditor() {
	editor := s.hosts.editor
	if editor == nil {
		return
	}
	editor.beginCreate(nil, false)
	s.Select(Hosts)
	pushHostEditorRoute(s, createHostPath)
}

// beginHostEdit opens one saved record from a detail page.
func (s *Shell) beginHostEdit(host domainvault.Host) {
	s.openHostEditor(host, false)
}

// beginDeepLinkHostEdit opens the record a deep link matched.
func (s *Shell) beginDeepLinkHostEdit(host domainvault.Host) {
	s.openHostEditor(host, true)
}

func (s *Shell) openHostEditor(host domainvault.Host, fromLink bool) {
	editor := s.hosts.editor
	if editor == nil || !editor.beginEdit(host, fromLink) {
		if fromLink {
			s.deepLinkProblem = "This Host is no longer in the Vault, so there is nothing to edit."
		} else {
			s.hosts.detailProblem = "This build cannot edit the Host."
		}
		return
	}
	s.Select(Hosts)
	route := "/host/" + string(host.ID) + "/edit"
	pushHostEditorRoute(s, route)
}

// finishHostEditorSave moves off the editor after a save. A refresh failure
// closes the editor and returns to the list without connecting; a successful
// save goes to the new Host's page, or to its Session when the user chose Save
// and connect.
func (s *Shell) finishHostEditorSave(host domainvault.Host, connect, refreshFailed bool) {
	router := s.sections[Hosts].router
	if refreshFailed {
		router.Replace("/")
		return
	}
	if connect {
		router.Replace("/")
		s.openHost(host)
		return
	}
	router.Replace("/host/" + string(host.ID))
}

// cancelHostEditor leaves the editor without changing the Vault. The shared
// state drops the draft, including any typed password or authenticator.
func (s *Shell) cancelHostEditor() {
	if s.hosts.editor != nil {
		s.hosts.editor.state.CancelEdit()
	}
	s.hosts.detailProblem = ""
	s.sections[Hosts].router.Back()
}

// reportDeepLinkProblem records an actionable, localized-enough sentence for
// the inline banner every section draws. The raw error is never shown: the
// user gets the next step instead of a parser message.
func (s *Shell) reportDeepLinkProblem(err error) {
	if errors.Is(err, launch.ErrUnsupportedScheme) {
		s.deepLinkProblem = "Natcatty does not support that link. Only ssh:// links open a Host."
		return
	}
	s.deepLinkProblem = "Natcatty could not open that link. Check the address and port, then try again."
}
