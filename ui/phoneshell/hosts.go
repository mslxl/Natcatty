package phoneshell

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// HostStore is the Vault read surface the Hosts section needs. The application
// Vault service implements it over the same SQLite store the desktop reads, so
// a Host added, edited, or removed on the desktop appears here after a reload.
//
// The phone lists only remote Hosts. A Local Host would run a shell on the
// phone itself, which the phone has no transport for, and a serial device is
// not a Host record: both stay in the Vault for the desktop and never appear
// on a phone surface.
type HostStore interface {
	List(ctx context.Context) ([]domainvault.Host, error)
}

// hostsStatus is the state of the Hosts list.
type hostsStatus int

const (
	hostsLoading hostsStatus = iota
	hostsReady
	hostsFailed
)

// hostPages owns the Hosts section: the remote-only list, its detail page, and
// the connect action.
type hostPages struct {
	store HostStore
	// create writes a Host the create form asked for. A Shell without it still
	// opens the form but reports that this build cannot create Hosts.
	create HostCreator
	// open opens a Host Session. The Shell sets it so a row tap switches to
	// Sessions and pushes the terminal page there.
	open func(domainvault.Host)
	// details pushes one Host's detail page on the Hosts section's own
	// router. The Shell sets it, because the section owns the router.
	details func(domainvault.Host)
	// created runs after the create form succeeds. The Shell sets it to move
	// the page to the new Host.
	created func(domainvault.Host)
	// form is the prefilled draft a deep link asked for, or nil when no
	// create form is open.
	form *hostForm
	// formProblem is the create form's inline error.
	formProblem string

	status  hostsStatus
	problem string
	hosts   []domainvault.Host
}

func newHostPages() *hostPages { return &hostPages{status: hostsLoading} }

// load replaces the list from the shared store. Local Hosts are dropped here,
// at the phone's composition seam, rather than in the domain: the desktop
// still sees and edits them, and a phone edit can never reach them because
// they never enter the phone's list.
func (h *hostPages) load(ctx context.Context) error {
	if h.store == nil {
		h.status = hostsFailed
		h.problem = "The Vault is not available on this device."
		return nil
	}
	h.status = hostsLoading
	all, err := h.store.List(ctx)
	if err != nil {
		h.status = hostsFailed
		h.problem = "Could not load Hosts."
		return err
	}
	remote := make([]domainvault.Host, 0, len(all))
	for _, host := range all {
		if host.IsRemote() {
			remote = append(remote, host)
		}
	}
	sort.SliceStable(remote, func(i, j int) bool {
		if remote[i].Pinned != remote[j].Pinned {
			return remote[i].Pinned
		}
		return strings.ToLower(remote[i].Label) < strings.ToLower(remote[j].Label)
	})
	h.hosts = remote
	h.status = hostsReady
	h.problem = ""
	return nil
}

// summary is the Hosts section's one-line description on any surface that
// reports it.
func (h *hostPages) summary() string {
	switch h.status {
	case hostsLoading:
		return "Loading…"
	case hostsFailed:
		return "Unavailable"
	}
	switch len(h.hosts) {
	case 0:
		return "No remote Hosts"
	case 1:
		return "1 remote Host"
	default:
		return fmt.Sprintf("%d remote Hosts", len(h.hosts))
	}
}

// host finds a Host by identity in the loaded list.
func (h *hostPages) host(id string) (domainvault.Host, bool) {
	for _, host := range h.hosts {
		if string(host.ID) == id {
			return host, true
		}
	}
	return domainvault.Host{}, false
}

// drawRoot renders the Hosts list. Tapping a row opens the Host; the info
// button pushes its detail page, which is what gives the section a stack of
// its own.
func (h *hostPages) drawRoot(c *ui.Context) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			switch h.status {
			case hostsLoading:
				kit.EmptyHint(c, "Loading Hosts…")
				return
			case hostsFailed:
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.problem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						_ = h.load(context.Background())
					}
				})
				return
			}
			kit.SectionBar(c, "Remote Hosts", func() {
				if kit.GhostButton(c, icons.RefreshCw, "Refresh").Clicked() {
					_ = h.load(context.Background())
				}
			})
			if h.problem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.problem, nil)
			}
			if len(h.hosts) == 0 {
				kit.EmptyHint(c, "No remote Hosts yet. Hosts you add on the desktop appear here.")
				return
			}
			for _, host := range h.hosts {
				host := host
				row := kit.ListRow(c).Label("Open " + hostName(host))
				row.Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, hostName(host)).FontWeight(600).SingleLine()
						ui.Text(c, hostDetail(host)).FontSize(12).
							TextColor(c.Theme().TextMuted).SingleLine()
					})
					if kit.IconButton(c, icons.Info, "Details for "+hostName(host)).Clicked() {
						h.pushDetails(host)
						return
					}
					ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
				})
				if row.Clicked() {
					if h.open != nil {
						h.open(host)
					}
				}
			}
		})
	})
}

// pushDetails moves the Hosts section to one Host's detail page. The Shell owns
// the router, so this is a callback rather than a direct router call.
func (h *hostPages) pushDetails(host domainvault.Host) {
	if h.details != nil {
		h.details(host)
	}
}

// drawDetail renders one Host's non-secret record and its Connect action.
func (h *hostPages) drawDetail(c *ui.Context, id string) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			host, ok := h.host(id)
			if !ok {
				kit.EmptyHint(c, "This Host is no longer in the Vault. Refresh the Hosts list.")
				return
			}
			kit.SectionBar(c, hostName(host), nil)
			detailColumn(c, host)
			// The whole row is the tap target, as every other phone action is;
			// a bare button's text node gives the tester no box to click.
			action := kit.ListRow(c).Label("Connect to " + hostName(host))
			action.Children(func() {
				ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icons.Lucide(icons.Terminal)).TextColor(c.Theme().Accent)
					ui.Text(c, "Connect").FontWeight(600)
				})
				ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
			})
			if action.Clicked() && h.open != nil {
				h.open(host)
			}
		})
	})
}

// detailColumn shows the fields that let a user tell two Hosts apart, without
// any secret: the endpoint, protocol, group, tags, and notes.
func detailColumn(c *ui.Context, host domainvault.Host) {
	ui.Column(c).FillWidth().PaddingY(12).Gap(6).Background(c.Theme().Surface).Radius(12).
		Children(func() {
			detailLine(c, "Endpoint", hostEndpoint(host))
			detailLine(c, "Protocol", string(protocolOf(host)))
			if host.Group != "" {
				detailLine(c, "Group", host.Group)
			}
			if len(host.Tags) > 0 {
				detailLine(c, "Tags", strings.Join(host.Tags, ", "))
			}
			if host.Notes != "" {
				detailLine(c, "Notes", host.Notes)
			}
		})
}

func detailLine(c *ui.Context, label, value string) {
	ui.Row(c).FillWidth().Gap(12).Children(func() {
		ui.Text(c, label).Width(84).FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Text(c, value).Grow(1).MinWidth(0)
	})
}

// hostName is the label a user recognizes, or the hostname for an unlabelled
// Host, matching the desktop's Tab label.
func hostName(host domainvault.Host) string {
	if label := strings.TrimSpace(host.Label); label != "" {
		return label
	}
	if host.Hostname != "" {
		return host.Hostname
	}
	return string(host.ID)
}

// hostDetail is the second line that distinguishes rows: the account and
// endpoint, plus the protocol when it is not plain SSH.
func hostDetail(host domainvault.Host) string {
	parts := []string{hostEndpoint(host)}
	if protocol := protocolOf(host); protocol != domainvault.ProtocolSSH {
		parts = append(parts, string(protocol))
	}
	if host.Proxy != nil || host.ProxyProfileID != "" {
		parts = append(parts, "via proxy")
	}
	if host.HostChain != nil && len(host.HostChain.HostIDs) > 0 {
		parts = append(parts, fmt.Sprintf("via %d jump Host(s)", len(host.HostChain.HostIDs)))
	}
	if host.NetworkProfileID != "" {
		parts = append(parts, "network profile")
	}
	return strings.Join(parts, " · ")
}

func hostEndpoint(host domainvault.Host) string {
	endpoint := host.Hostname
	if host.Port != 0 {
		endpoint = fmt.Sprintf("%s:%d", host.Hostname, host.Port)
	}
	if host.Username != "" {
		return host.Username + "@" + endpoint
	}
	return endpoint
}

// protocolOf reports the protocol a legacy record stored without one would
// have received at normalization: SSH.
func protocolOf(host domainvault.Host) domainvault.Protocol {
	if host.Protocol == "" {
		return domainvault.ProtocolSSH
	}
	return host.Protocol
}

// drawCreate renders the form a deep link asked for when no saved Host matches
// it. The endpoint the link named is prefilled and the page names it, so the
// user knows which link they are acting on. Creating the Host moves to its
// page, where Connect is one tap away.
func (h *hostPages) drawCreate(c *ui.Context, cancel func()) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			if h.form == nil {
				kit.EmptyHint(c, "This link no longer needs a new Host.")
				if kit.GhostButton(c, icons.X, "Back to Hosts").Clicked() && cancel != nil {
					cancel()
				}
				return
			}
			kit.SectionBar(c, "New Host", nil)
			kit.EmptyHint(c, "No saved Host matches "+h.form.endpoint()+". Create one to connect.")
			if h.formProblem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.formProblem, nil)
			}
			kit.FormRow(c, "Label", func() {
				ui.TextInput(c, &h.form.label).Placeholder("A name you recognize").FillWidth()
			})
			kit.FormRow(c, "Hostname", func() {
				ui.TextInput(c, &h.form.hostname).Placeholder("host.example.com").FillWidth()
			})
			kit.FormRow(c, "Port", func() {
				ui.TextInput(c, &h.form.port).Placeholder("22").FillWidth()
			})
			kit.FormRow(c, "Username", func() {
				ui.TextInput(c, &h.form.username).Placeholder("user").FillWidth()
			})
			if kit.PrimaryButton(c, "", "Create Host").Clicked() {
				h.submitCreate()
			}
			if kit.SecondaryButton(c, "", "Cancel").Clicked() && cancel != nil {
				cancel()
			}
		})
	})
}

// submitCreate validates the form and writes the Host through the Vault. A
// failure stays on the form with an actionable reason, so the user can correct
// the address or retry instead of losing what they entered.
func (h *hostPages) submitCreate() {
	if h.form == nil {
		return
	}
	draft, err := h.form.draft()
	if err != nil {
		h.formProblem = err.Error()
		return
	}
	if h.create == nil {
		h.formProblem = "This build cannot create Hosts."
		return
	}
	host, err := h.create.Create(context.Background(), draft)
	if err != nil {
		h.formProblem = createProblem(err)
		return
	}
	h.form = nil
	h.formProblem = ""
	if h.created != nil {
		h.created(host)
	}
}

// remember adds or replaces one Host in the loaded list, so a newly created
// Host is found by the page that follows it and by a repeat deep link without
// a full reload.
func (h *hostPages) remember(host domainvault.Host) {
	for i, existing := range h.hosts {
		if existing.ID == host.ID {
			h.hosts[i] = host
			return
		}
	}
	h.hosts = append(h.hosts, host)
}
