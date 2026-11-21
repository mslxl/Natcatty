package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

	if host, ok := domainvault.MatchHost(s.hosts.hosts, target); ok {
		s.openOrSelectHost(host)
		return nil
	}
	s.promptToCreateHost(target)
	return nil
}

// openOrSelectHost is the link's success path. A Host that already has a Tab
// is selected, so a repeated link never stacks a second Session on the same
// endpoint; otherwise the Host is opened as a new Tab. Either way the
// lifecycle rule hibernates what is no longer visible and never ends it.
func (s *Shell) openOrSelectHost(host domainvault.Host) {
	if id, ok := s.sessions.tabForHost(host.ID); ok {
		s.Select(Sessions)
		route := "/session/" + id
		if s.sections[Sessions].router.Path() != route {
			s.sections[Sessions].router.Push(route)
		}
		return
	}
	s.openHost(host)
}

// promptToCreateHost is the link's unmatched path: an ssh:// link names an
// endpoint the Vault does not have, so the Hosts section asks for a Host with
// the link's user, host, and port already filled in.
func (s *Shell) promptToCreateHost(target launch.Target) {
	s.hosts.form = formFromHost(domainvault.DraftHost(target))
	s.hosts.formProblem = ""
	s.Select(Hosts)
	router := s.sections[Hosts].router
	if router.Path() == createHostPath {
		router.Replace(createHostPath)
		return
	}
	router.Push(createHostPath)
}

// finishCreatedHost moves from the create form to the new Host's page, where
// Connect is one tap away. The Host is added to the loaded list first, so the
// page and a repeat link both find it without a reload.
func (s *Shell) finishCreatedHost(host domainvault.Host) {
	s.hosts.remember(host)
	s.hosts.form = nil
	s.hosts.formProblem = ""
	s.sections[Hosts].router.Replace("/host/" + string(host.ID))
}

// cancelCreateHost leaves the create form without changing the Vault.
func (s *Shell) cancelCreateHost() {
	s.hosts.form = nil
	s.hosts.formProblem = ""
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

// hostForm is the editable state of the create form. It is a form, not a Host:
// the draft built on submit is what the Vault validates and stores.
type hostForm struct {
	label    string
	hostname string
	port     string
	username string
}

// formFromHost prefills the form from an unsaved Host built from the link.
func formFromHost(host domainvault.Host) *hostForm {
	port := ""
	if host.Port > 0 {
		port = strconv.Itoa(host.Port)
	}
	return &hostForm{
		label:    host.Label,
		hostname: host.Hostname,
		port:     port,
		username: host.Username,
	}
}

// endpoint is the address the form names, for the label that explains which
// link asked for a new Host.
func (f *hostForm) endpoint() string {
	return hostEndpoint(f.host())
}

// host is the form's current values as an unsaved Host, used for the summary
// and as the source of the draft.
func (f *hostForm) host() domainvault.Host {
	port, _ := strconv.Atoi(strings.TrimSpace(f.port))
	return domainvault.Host{
		Label:    strings.TrimSpace(f.label),
		Hostname: strings.TrimSpace(f.hostname),
		Port:     port,
		Username: strings.TrimSpace(f.username),
		Protocol: domainvault.ProtocolSSH,
	}
}

// draft validates the form's values and builds the creation draft. Errors are
// the form's inline text: they name the field to fix.
func (f *hostForm) draft() (domainvault.Draft, error) {
	host := f.host()
	if host.Hostname == "" {
		return domainvault.Draft{}, errors.New("Enter the Host's address.")
	}
	if strings.TrimSpace(f.port) != "" && (host.Port < 1 || host.Port > 65535) {
		return domainvault.Draft{}, errors.New("Enter a port between 1 and 65535.")
	}
	return domainvault.Draft{
		Label:    host.Label,
		Hostname: host.Hostname,
		Port:     host.Port,
		Username: host.Username,
		Protocol: domainvault.ProtocolSSH,
	}, nil
}

// createProblem turns a failed creation into an actionable sentence. It keeps
// the Vault's reason so a duplicate endpoint reads differently from a storage
// failure, and names the user's next step.
func createProblem(err error) string {
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		reason = "the Vault refused the Host"
	}
	return fmt.Sprintf("Could not create the Host: %s. Check the address and port, then try again.", reason)
}
