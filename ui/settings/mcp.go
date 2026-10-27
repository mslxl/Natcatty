package settings

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	"github.com/mslxl/natcatty/ui/locales"
)

// MCPService is the external MCP policy seam the Settings surface drives. The
// app supplies *application/mcp.Service behind a small adapter; tests supply a
// fake. Approve and Deny return the policy's typed error when a prompt expired
// or was already consumed, so the surface can localize the outcome instead of
// rendering Go error text.
type MCPService interface {
	Snapshot() appmcp.Snapshot
	SetMode(mode appmcp.Mode) error
	Approve(id string) error
	Deny(id string) error
	Dismiss(id string)
}

// MCPProblem is the last action outcome the surface shows. It is a stable
// enum, not an error string, so every locale renders it from the catalog.
type MCPProblem int

const (
	MCPProblemNone MCPProblem = iota
	MCPProblemExpired
	MCPProblemStale
	MCPProblemInvalid
	MCPProblemMode
)

// MCPState owns the Settings MCP surface. All fields are mutated only on the
// UI thread; the policy itself is safe for concurrent use.
type MCPState struct {
	service MCPService
	problem MCPProblem
}

// NewMCPState builds the external MCP surface from the policy service.
func NewMCPState(service MCPService) *MCPState {
	return &MCPState{service: service}
}

// Snapshot returns the policy's read model. A nil service renders the
// unavailable state instead of panicking.
func (s *MCPState) Snapshot() appmcp.Snapshot {
	if s == nil || s.service == nil {
		return appmcp.Snapshot{Mode: appmcp.DefaultMode}
	}
	return s.service.Snapshot()
}

// SetMode changes the permission mode. The policy re-reads the mode for every
// call, so the next tool call sees the new value.
func (s *MCPState) SetMode(mode appmcp.Mode) {
	if s == nil || s.service == nil {
		return
	}
	if err := s.service.SetMode(mode); err != nil {
		s.problem = MCPProblemMode
		return
	}
	s.problem = MCPProblemNone
}

// Approve accepts the pending prompt. Single-use is enforced by the policy.
func (s *MCPState) Approve(id string) {
	if s == nil || s.service == nil {
		return
	}
	s.problem = mcpProblemForError(s.service.Approve(id))
}

// Deny rejects the pending prompt and resolves the waiting call as denied.
func (s *MCPState) Deny(id string) {
	if s == nil || s.service == nil {
		return
	}
	s.problem = mcpProblemForError(s.service.Deny(id))
}

// Dismiss removes an expired prompt from the recent list.
func (s *MCPState) Dismiss(id string) {
	if s == nil || s.service == nil {
		return
	}
	s.service.Dismiss(id)
	s.problem = MCPProblemNone
}

// Problem returns the last localized problem, or MCPProblemNone.
func (s *MCPState) Problem() MCPProblem {
	if s == nil {
		return MCPProblemNone
	}
	return s.problem
}

func mcpProblemForError(err error) MCPProblem {
	if err == nil {
		return MCPProblemNone
	}
	var typed *appmcp.Error
	if errors.As(err, &typed) {
		switch typed.Code {
		case appmcp.CodeApprovalExpired:
			return MCPProblemExpired
		case appmcp.CodeStaleGeneration:
			return MCPProblemStale
		}
	}
	return MCPProblemInvalid
}

// mcpModeValues is the selector order: the safe mode first, then the default,
// then the permissive mode.
func mcpModeValues() []appmcp.Mode {
	return []appmcp.Mode{appmcp.ModeObserver, appmcp.ModeConfirm, appmcp.ModeAuto}
}

func mcpModeLabels(m locales.Messages) []string {
	return []string{
		m.SettingsMCPModeObserver(),
		m.SettingsMCPModeConfirm(),
		m.SettingsMCPModeAuto(),
	}
}

func mcpModeName(mode appmcp.Mode, m locales.Messages) string {
	switch appmcp.NormalizeMode(string(mode)) {
	case appmcp.ModeObserver:
		return m.SettingsMCPModeObserver()
	case appmcp.ModeAuto:
		return m.SettingsMCPModeAuto()
	default:
		return m.SettingsMCPModeConfirm()
	}
}

func mcpModeDescription(mode appmcp.Mode, m locales.Messages) string {
	switch appmcp.NormalizeMode(string(mode)) {
	case appmcp.ModeObserver:
		return m.SettingsMCPModeObserverDescription()
	case appmcp.ModeAuto:
		return m.SettingsMCPModeAutoDescription()
	default:
		return m.SettingsMCPModeConfirmDescription()
	}
}

func mcpModeIndex(mode appmcp.Mode) int {
	normalized := appmcp.NormalizeMode(string(mode))
	for index, candidate := range mcpModeValues() {
		if candidate == normalized {
			return index
		}
	}
	return 1
}

func mcpProblemText(problem MCPProblem, m locales.Messages) string {
	switch problem {
	case MCPProblemExpired:
		return m.SettingsMCPProblemExpired()
	case MCPProblemStale:
		return m.SettingsMCPProblemStale()
	case MCPProblemInvalid:
		return m.SettingsMCPProblemInvalid()
	case MCPProblemMode:
		return m.SettingsMCPProblemMode()
	default:
		return ""
	}
}

// mcpCurrentPrompt returns the one prompt the surface is asking about: the
// oldest pending request, or the most recent expired one when nothing is
// pending. Keeping one prompt current keeps its action row inside one pinned
// bar at every window size.
func mcpCurrentPrompt(snapshot appmcp.Snapshot) (appmcp.ApprovalView, bool, bool) {
	if len(snapshot.Pending) > 0 {
		return snapshot.Pending[0], false, true
	}
	if len(snapshot.Recent) > 0 {
		return snapshot.Recent[0], true, true
	}
	return appmcp.ApprovalView{}, false, false
}

// mcpRemainingSeconds rounds up so a partially elapsed second still reads as
// time left; exactly zero is the expired state.
func mcpRemainingSeconds(remaining time.Duration) int {
	if remaining <= 0 {
		return 0
	}
	return int(math.Ceil(remaining.Seconds()))
}

// mcpReadableTextColor keeps a preferred text color when it clears 4.5:1 on
// the actual fill, and otherwise falls back to whichever of black or white is
// more readable there. It reuses the package's WCAG helper, so a custom preset
// whose text token is too light (or too dark) cannot drag the MCP body below
// the target.
func mcpReadableTextColor(preferred, fill ui.Color) ui.Color {
	if syncColorContrast(preferred, fill) >= 4.5 {
		return preferred
	}
	black := ui.Hex("#000000")
	white := ui.Hex("#ffffff")
	if syncColorContrast(white, fill) >= syncColorContrast(black, fill) {
		return white
	}
	return black
}

// mcpBodyTextColor is the body token with the accessibility fallback applied.
func mcpBodyTextColor(t *ui.Theme, fill ui.Color) ui.Color {
	return mcpReadableTextColor(t.Text, fill)
}

// mcpWarningTextColor starts from ticket 54's shared warning text color and
// applies the same fallback ladder only when a custom preset's fill pushes the
// shared amber below 4.5:1. The default light and dark presets keep the shared
// value exactly.
func mcpWarningTextColor(t *ui.Theme, fill ui.Color) ui.Color {
	base := syncWarningTextColor(t)
	if syncColorContrast(base, fill) >= 4.5 {
		return base
	}
	candidates := []ui.Color{ui.Hex("#7a4800"), ui.Hex("#5f3800"), ui.Hex("#3d2400"), ui.Hex("#000000")}
	if t.Dark {
		candidates = []ui.Color{ui.Hex("#ffb84d"), ui.Hex("#ffcc80"), ui.Hex("#ffd699"), ui.Hex("#ffe6b3"), ui.Hex("#ffffff")}
	}
	for _, candidate := range candidates {
		if syncColorContrast(candidate, fill) >= 4.5 {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

// mcpDangerTextColor starts from ticket 54's shared danger text color and
// applies a safer ladder only if that value cannot clear the current fill.
func mcpDangerTextColor(t *ui.Theme, fill ui.Color) ui.Color {
	base := syncDangerTextColor(t)
	if syncColorContrast(base, fill) >= 4.5 {
		return base
	}
	candidates := []ui.Color{ui.Hex("#8a1010"), ui.Hex("#6e0c0c"), ui.Hex("#500808"), ui.Hex("#000000")}
	if t.Dark {
		candidates = []ui.Color{ui.Hex("#ff9999"), ui.Hex("#ffb3b3"), ui.Hex("#ffd1d1"), ui.Hex("#ffffff")}
	}
	for _, candidate := range candidates {
		if syncColorContrast(candidate, fill) >= 4.5 {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

// mcpArea renders the external MCP status and the current approval prompt.
func mcpArea(c *ui.Context, state *MCPState, m locales.Messages, t *ui.Theme) {
	if state == nil || state.service == nil {
		ui.Text(c, m.SettingsMCPUnavailable()).FontSize(12).TextColor(mcpBodyTextColor(t, t.Surface))
		return
	}
	snapshot := state.Snapshot()
	body := mcpBodyTextColor(t, t.Surface)
	sectionBody(c, t, func() {
		mcpModeField(c, state, snapshot, m, t)
		ui.Text(c, mcpModeDescription(snapshot.Mode, m)).FontSize(12).TextColor(body)

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SettingsMCPScope()).FontSize(11).TextColor(body)
			ui.Text(c, m.SettingsMCPScopeValue(snapshot.ForcedScope)).FontSize(12).TextColor(body)
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SettingsMCPSessions()).FontSize(11).TextColor(body)
			ui.Text(c, mcpAllowlistLabel(snapshot.Allowlist, m)).FontSize(12).TextColor(body)
		})
		ui.Text(c, m.SettingsMCPGeneration(strconv.FormatUint(snapshot.Generation, 10))).
			FontSize(12).TextColor(body)
		ui.Text(c, m.SettingsMCPBypassHint()).FontSize(11).TextColor(body)

		if problem := mcpProblemText(state.Problem(), m); problem != "" {
			ui.Text(c, problem).FontSize(12).TextColor(mcpWarningTextColor(t, t.Surface))
		}

		prompt, expired, ok := mcpCurrentPrompt(snapshot)
		if !ok {
			return
		}
		mcpPromptCard(c, prompt, expired, m, t)
		if len(snapshot.Pending) > 1 {
			ui.Text(c, m.SettingsMCPMorePending(len(snapshot.Pending)-1)).
				FontSize(11).TextColor(body)
		}
		if !expired {
			// The policy owns the deadline; this only asks for a frame so the
			// remaining label counts down while the client waits.
			delay := prompt.Remaining
			if delay <= 0 || delay > time.Second {
				delay = time.Second
			}
			c.After(delay)
		}
	})
}

func mcpModeField(c *ui.Context, state *MCPState, snapshot appmcp.Snapshot,
	m locales.Messages, t *ui.Theme) {
	labels := mcpModeLabels(m)
	selected := labels[mcpModeIndex(snapshot.Mode)]
	ui.Field(c, m.SettingsMCPMode(), func() {
		if ui.Select(c, &selected, labels).Width(220).Changed() {
			values := mcpModeValues()
			for index, label := range labels {
				if label == selected {
					state.SetMode(values[index])
					break
				}
			}
			c.Invalidate()
		}
	})
}

func mcpAllowlistLabel(allowlist []string, m locales.Messages) string {
	if len(allowlist) == 0 {
		return m.SettingsMCPNoSessions()
	}
	return m.SettingsMCPSessionsValue(strings.Join(allowlist, ", "))
}

// mcpPromptCard shows the client, tool, target, scope, already-redacted
// argument summary, and either the countdown or the expired state. Approve,
// Deny, and Dismiss live in the pinned bar so they are never clipped by this
// scrolling card.
func mcpPromptCard(c *ui.Context, prompt appmcp.ApprovalView, expired bool,
	m locales.Messages, t *ui.Theme) {
	title := m.SettingsMCPApprovalTitle()
	client := strings.TrimSpace(prompt.Client)
	if client == "" {
		client = m.SettingsMCPUnknownClient()
	}
	border := t.Warning
	titleColor := mcpWarningTextColor(t, t.Background)
	if expired {
		title = m.SettingsMCPApprovalExpiredTitle()
		border = t.Danger
		titleColor = mcpDangerTextColor(t, t.Background)
	}
	body := mcpBodyTextColor(t, t.Background)
	ui.Column(c).Padding(12).Gap(6).Background(t.Background).Border(1, border).Children(func() {
		ui.Text(c, title).FontSize(13).Bold().TextColor(titleColor)
		ui.Text(c, m.SettingsMCPApprovalClient(client)).FontSize(12).TextColor(body)
		ui.Text(c, m.SettingsMCPApprovalTool(prompt.Tool)).FontSize(12).TextColor(body)
		ui.Text(c, m.SettingsMCPApprovalTarget(prompt.Target)).FontSize(12).TextColor(body)
		ui.Text(c, m.SettingsMCPApprovalScope(prompt.Scope)).FontSize(12).TextColor(body)
		ui.Text(c, m.SettingsMCPApprovalSummary(prompt.Summary)).FontSize(12).TextColor(body)
		if expired {
			ui.Text(c, m.SettingsMCPApprovalExpired()).FontSize(12).TextColor(mcpDangerTextColor(t, t.Background))
			return
		}
		ui.Text(c, m.SettingsMCPApprovalRemaining(strconv.Itoa(mcpRemainingSeconds(prompt.Remaining)))).
			FontSize(12).TextColor(mcpWarningTextColor(t, t.Background))
	})
}

// mcpApprovalPinnedBar draws the current prompt's decision below the bounded
// content viewport. The bar is a sibling of the scroll area, so it is never
// cut at the scroll edge; only one prompt is current, so the row always fits.
func mcpApprovalPinnedBar(c *ui.Context, state *MCPState, snapshot appmcp.Snapshot,
	m locales.Messages, t *ui.Theme, layout *viewLayout) {
	if state == nil || state.service == nil {
		return
	}
	prompt, expired, ok := mcpCurrentPrompt(snapshot)
	if !ok {
		return
	}
	bar := ui.Column(c).FillWidth().Padding(10).Gap(6).
		Background(t.Surface).Border(1, t.Border)
	if layout != nil {
		bar = bar.Bind(&layout.MCPActions)
	}
	bar.Children(func() {
		syncActionRow(c, func() {
			if expired {
				if ui.Button(c, m.SettingsMCPDismiss()).Clicked() {
					state.Dismiss(prompt.ID)
					c.Invalidate()
				}
				return
			}
			if ui.PrimaryButton(c, m.SettingsMCPApprove()).Clicked() {
				state.Approve(prompt.ID)
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsMCPDeny()).Clicked() {
				state.Deny(prompt.ID)
				c.Invalidate()
			}
		})
	})
}
