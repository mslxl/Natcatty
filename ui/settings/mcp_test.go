package settings

// Ticket 57's Settings surface: the external MCP permission mode, the forced
// scope, the session allowlist, ownership generation, and the single current
// approval prompt with its remaining time or expired state. The decisions are
// pinned below the scrolling content so a short window never cuts them.

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// fakeMCPService is the deterministic policy seam for the surface tests. The
// approval lifecycle is simplified the same way the policy's read model is:
// resolving a prompt removes it from Pending and, for expiry, records it in
// Recent.
type fakeMCPService struct {
	snapshot   appmcp.Snapshot
	modes      []appmcp.Mode
	approved   []string
	denied     []string
	dismissed  []string
	approveErr error
	denyErr    error
	modeErr    error
}

func (f *fakeMCPService) Snapshot() appmcp.Snapshot { return f.snapshot }

func (f *fakeMCPService) SetMode(mode appmcp.Mode) error {
	if f.modeErr != nil {
		return f.modeErr
	}
	f.modes = append(f.modes, mode)
	f.snapshot.Mode = mode
	return nil
}

func (f *fakeMCPService) Approve(id string) error {
	f.approved = append(f.approved, id)
	if f.approveErr != nil {
		return f.approveErr
	}
	f.removePending(id)
	return nil
}

func (f *fakeMCPService) Deny(id string) error {
	f.denied = append(f.denied, id)
	if f.denyErr != nil {
		return f.denyErr
	}
	f.removePending(id)
	return nil
}

func (f *fakeMCPService) Dismiss(id string) {
	f.dismissed = append(f.dismissed, id)
	out := f.snapshot.Recent[:0]
	for _, view := range f.snapshot.Recent {
		if view.ID != id {
			out = append(out, view)
		}
	}
	f.snapshot.Recent = out
}

func (f *fakeMCPService) removePending(id string) {
	out := f.snapshot.Pending[:0]
	for _, view := range f.snapshot.Pending {
		if view.ID != id {
			out = append(out, view)
		}
	}
	f.snapshot.Pending = out
}

func mcpPendingFixture(id string) appmcp.ApprovalView {
	return appmcp.ApprovalView{
		ID:        id,
		Client:    "claude-code/1.2",
		Tool:      "terminal_execute",
		Target:    "web (s1)",
		Summary:   `command="uptime" hostId="h1" password="[redacted]"`,
		Scope:     "__external_mcp__",
		SessionID: "s1",
		CreatedAt: time.Unix(1000, 0),
		Deadline:  time.Unix(1110, 0),
		Status:    appmcp.ApprovalPending,
		Remaining: 110 * time.Second,
	}
}

func mcpSnapshotWithPrompt() appmcp.Snapshot {
	return appmcp.Snapshot{
		Mode:        appmcp.ModeConfirm,
		ForcedScope: "__external_mcp__",
		Allowlist:   []string{"s1", "s2"},
		Generation:  7,
		Pending:     []appmcp.ApprovalView{mcpPendingFixture("mcp_approval_1")},
	}
}

func mcpExpiredFixture() appmcp.ApprovalView {
	view := mcpPendingFixture("mcp_approval_2")
	view.Status = appmcp.ApprovalExpired
	view.Remaining = 0
	view.Expired = true
	return view
}

func mcpViewWithLayout(area Area, config Config, layout *viewLayout) func(*ui.Context) {
	search := NewSearchState()
	search.SetArea(area)
	config.Search = search
	return viewWithLayout(config, layout)
}

func TestMCPApprovalPromptShowsEveryFieldAndPinnedDecision(t *testing.T) {
	service := &fakeMCPService{snapshot: mcpSnapshotWithPrompt()}
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	prompt := mcpPendingFixture("mcp_approval_1")
	for _, text := range []string{
		m.SettingsMCPMode(),
		m.SettingsMCPModeConfirm(),
		m.SettingsMCPModeConfirmDescription(),
		m.SettingsMCPScope(),
		m.SettingsMCPScopeValue("__external_mcp__"),
		m.SettingsMCPSessions(),
		m.SettingsMCPSessionsValue("s1, s2"),
		m.SettingsMCPGeneration("7"),
		m.SettingsMCPApprovalTitle(),
		m.SettingsMCPApprovalClient(prompt.Client),
		m.SettingsMCPApprovalTool(prompt.Tool),
		m.SettingsMCPApprovalTarget(prompt.Target),
		m.SettingsMCPApprovalScope(prompt.Scope),
		m.SettingsMCPApprovalSummary(prompt.Summary),
		m.SettingsMCPApprovalRemaining("110"),
		m.SettingsMCPApprove(),
		m.SettingsMCPDeny(),
	} {
		if !tester.HasText(text) {
			t.Errorf("the MCP surface does not show %q: %q", text, tester.Texts())
		}
	}

	if err := tester.Click(m.SettingsMCPApprove()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(service.approved) != 1 || service.approved[0] != prompt.ID {
		t.Fatalf("Approve reached the policy with %v, want %q", service.approved, prompt.ID)
	}
	if len(service.snapshot.Pending) != 0 {
		t.Fatalf("the approved prompt is still pending: %+v", service.snapshot.Pending)
	}

	// Deny is the other single-use decision. A fresh prompt proves the bar's
	// Deny reaches the service with the same id.
	service = &fakeMCPService{snapshot: mcpSnapshotWithPrompt()}
	state = NewMCPState(service)
	tester = ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if err := tester.Click(m.SettingsMCPDeny()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(service.denied) != 1 || service.denied[0] != prompt.ID {
		t.Fatalf("Deny reached the policy with %v, want %q", service.denied, prompt.ID)
	}
}

func TestMCPPromptLocalizesAnUnknownClient(t *testing.T) {
	prompt := mcpPendingFixture("mcp_approval_unknown")
	prompt.Client = ""
	service := &fakeMCPService{snapshot: appmcp.Snapshot{
		Mode: appmcp.ModeConfirm, ForcedScope: "__external_mcp__",
		Allowlist: []string{"s1"}, Generation: 1,
		Pending: []appmcp.ApprovalView{prompt},
	}}
	state := NewMCPState(service)
	for _, tag := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		t.Run(tag, func(t *testing.T) {
			m := locales.In(tag)
			tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
			tester.SetLocale(tag)
			tester.Frame()
			want := m.SettingsMCPApprovalClient(m.SettingsMCPUnknownClient())
			if !tester.HasText(want) {
				t.Fatalf("%s: the localized unknown-client label %q is missing: %q",
					tag, want, tester.Texts())
			}
			if tester.HasText("unknown MCP client") {
				t.Fatalf("%s: an English application string reached the prompt", tag)
			}
		})
	}
}

func TestMCPMorePendingUsesThePluralCount(t *testing.T) {
	first := mcpPendingFixture("mcp_approval_1")
	second := mcpPendingFixture("mcp_approval_2")
	second.ID = "mcp_approval_2"
	service := &fakeMCPService{snapshot: appmcp.Snapshot{
		Mode: appmcp.ModeConfirm, ForcedScope: "__external_mcp__",
		Allowlist: []string{"s1"}, Generation: 1,
		Pending: []appmcp.ApprovalView{first, second},
	}}
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText(m.SettingsMCPMorePending(1)) {
		t.Fatalf("the singular more-pending message is missing: %q", tester.Texts())
	}
	if tester.HasText("1 more requests are waiting.") {
		t.Fatal("the more-pending message did not pluralize")
	}
}

func TestMCPExpiredPromptShowsExpiredStateAndDismisses(t *testing.T) {
	expired := mcpExpiredFixture()
	service := &fakeMCPService{snapshot: appmcp.Snapshot{
		Mode:        appmcp.ModeConfirm,
		ForcedScope: "__external_mcp__",
		Allowlist:   []string{"s1"},
		Generation:  2,
		Recent:      []appmcp.ApprovalView{expired},
	}}
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	for _, text := range []string{
		m.SettingsMCPApprovalExpiredTitle(),
		m.SettingsMCPApprovalExpired(),
		m.SettingsMCPDismiss(),
		m.SettingsMCPApprovalClient(expired.Client),
	} {
		if !tester.HasText(text) {
			t.Errorf("the expired prompt does not show %q: %q", text, tester.Texts())
		}
	}
	if tester.HasText(m.SettingsMCPApprove()) {
		t.Error("an expired prompt still offers Approve")
	}
	if err := tester.Click(m.SettingsMCPDismiss()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(service.dismissed) != 1 || service.dismissed[0] != expired.ID {
		t.Fatalf("Dismiss reached the policy with %v, want %q", service.dismissed, expired.ID)
	}
	if len(service.snapshot.Recent) != 0 {
		t.Fatalf("the expired prompt was not dismissed: %+v", service.snapshot.Recent)
	}
}

func TestMCPApprovalProblemsAreLocalizedNotRawErrors(t *testing.T) {
	service := &fakeMCPService{
		snapshot:   mcpSnapshotWithPrompt(),
		approveErr: &appmcp.Error{Code: appmcp.CodeApprovalExpired, Message: "raw policy text"},
	}
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if err := tester.Click(m.SettingsMCPApprove()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsMCPProblemExpired()) {
		t.Fatalf("the expired problem is not shown: %q", tester.Texts())
	}
	if tester.HasText("raw policy text") {
		t.Fatal("raw policy error text reached the UI")
	}
}

func TestMCPModeSelectionReachesThePolicy(t *testing.T) {
	service := &fakeMCPService{snapshot: mcpSnapshotWithPrompt()}
	state := NewMCPState(service)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText(m.SettingsMCPModeConfirm()) {
		t.Fatalf("the current mode is not shown: %q", tester.Texts())
	}
	state.SetMode(appmcp.ModeObserver)
	tester.Frame()
	if len(service.modes) != 1 || service.modes[0] != appmcp.ModeObserver {
		t.Fatalf("SetMode reached the policy with %v", service.modes)
	}
	if !tester.HasText(m.SettingsMCPModeObserverDescription()) {
		t.Fatalf("the observer description is not shown: %q", tester.Texts())
	}
}

// TestMCPAreaLocalizesInEveryCatalog renders the prompt and the expired state
// in all five locales, so no catalog key falls back to English.
func TestMCPAreaLocalizesInEveryCatalog(t *testing.T) {
	for _, tag := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		t.Run(tag, func(t *testing.T) {
			m := locales.In(tag)
			service := &fakeMCPService{snapshot: mcpSnapshotWithPrompt()}
			state := NewMCPState(service)
			tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
			tester.SetLocale(tag)
			tester.Frame()
			for _, text := range []string{
				m.SettingsAreaMCP(), m.SettingsMCPMode(), m.SettingsMCPModeConfirm(),
				m.SettingsMCPApprovalTitle(), m.SettingsMCPApprove(), m.SettingsMCPDeny(),
				m.SettingsMCPApprovalRemaining("110"),
			} {
				if !tester.HasText(text) {
					t.Errorf("%s: %q is missing: %q", tag, text, tester.Texts())
				}
			}

			expired := mcpExpiredFixture()
			service = &fakeMCPService{snapshot: appmcp.Snapshot{
				Mode: appmcp.ModeConfirm, ForcedScope: "__external_mcp__",
				Allowlist: []string{"s1"}, Generation: 2,
				Recent: []appmcp.ApprovalView{expired},
			}}
			state = NewMCPState(service)
			tester = ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
			tester.SetLocale(tag)
			tester.Frame()
			for _, text := range []string{
				m.SettingsMCPApprovalExpiredTitle(), m.SettingsMCPApprovalExpired(),
				m.SettingsMCPDismiss(),
			} {
				if !tester.HasText(text) {
					t.Errorf("%s expired: %q is missing: %q", tag, text, tester.Texts())
				}
			}
		})
	}
}

func TestMCPApprovalPinnedActionsAtRealClientSizes(t *testing.T) {
	outDir := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "natcatty-mcp-policy-screenshots")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sizes := []struct {
		name          string
		width, height int
	}{{"704x421", 704, 421}, {"1084x661", 1084, 661}}
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		for _, size := range sizes {
			size, locale := size, locale
			t.Run(locale+"/"+size.name, func(t *testing.T) {
				m := locales.In(locale)
				prompt := mcpPendingFixture("mcp_approval_1")
				service := &fakeMCPService{snapshot: mcpSnapshotWithPrompt()}
				state := NewMCPState(service)
				var layout viewLayout
				var context *ui.Context
				view := mcpViewWithLayout(AreaMCP, Config{MCP: state}, &layout)
				tester := ui.NewTester(func(c *ui.Context) {
					context = c
					view(c)
				}, size.width, size.height)
				tester.SetLocale(locale)
				tester.Frame()

				viewport := layout.Content.Bounds(context)
				bar := layout.MCPActions.Bounds(context)
				if viewport.W <= 0 || viewport.H <= 0 {
					t.Fatalf("%s: the content viewport has no size: %+v", size.name, viewport)
				}
				if !insideClientRect(viewport, size.width, size.height) {
					t.Fatalf("%s: the content viewport is outside the client: %+v", size.name, viewport)
				}
				if bar.W <= 0 || bar.H <= 0 {
					t.Fatalf("%s: there is no pinned approval bar: %+v (%q)", size.name, bar, tester.Texts())
				}
				if !insideClientRect(bar, size.width, size.height) {
					t.Fatalf("%s: the pinned bar is outside the client: %+v", size.name, bar)
				}
				if viewport.Y+viewport.H > bar.Y+0.5 {
					t.Fatalf("%s: the content viewport %+v overlaps the pinned bar %+v", size.name, viewport, bar)
				}

				actions := []string{m.SettingsMCPApprove(), m.SettingsMCPDeny()}
				before := map[string]ui.Rect{}
				for _, label := range actions {
					rect, ok := tester.Find(label)
					if !ok {
						t.Fatalf("%s: the action %q is not rendered: %q", size.name, label, tester.Texts())
					}
					if rect.W <= 0 || rect.H < 18 {
						t.Fatalf("%s: the action %q is a clipped sliver: %+v", size.name, label, rect)
					}
					if !insideClientRect(rect, size.width, size.height) {
						t.Fatalf("%s: the action %q is cut at the client edge: %+v", size.name, label, rect)
					}
					if rect.Y < bar.Y-0.5 || rect.Y+rect.H > bar.Y+bar.H+0.5 {
						t.Fatalf("%s: the action %q %+v is not inside the pinned bar %+v", size.name, label, rect, bar)
					}
					before[label] = rect
				}

				required := []string{
					m.SettingsMCPMode(), m.SettingsMCPModeConfirm(),
					m.SettingsMCPScopeValue("__external_mcp__"),
					m.SettingsMCPSessionsValue("s1, s2"),
					m.SettingsMCPGeneration("7"),
					m.SettingsMCPApprovalTitle(),
					m.SettingsMCPApprovalClient(prompt.Client),
					m.SettingsMCPApprovalTool(prompt.Tool),
					m.SettingsMCPApprovalTarget(prompt.Target),
					m.SettingsMCPApprovalSummary(prompt.Summary),
					m.SettingsMCPApprovalRemaining("110"),
				}
				for _, text := range required {
					if !tester.HasText(text) {
						t.Fatalf("%s: %q is missing: %q", size.name, text, tester.Texts())
					}
				}

				// The prompt itself scrolls; keep one top-of-area record at the
				// shortest size so the mode and scope status is visual evidence
				// alongside the per-locale prompt renders taken after scrolling.
				if locale == "en" && size.name == "704x421" {
					writeMCPTestScreenshot(t, tester,
						filepath.Join(outDir, "mcp-en-704x421-status.png"))
				}

				lastText := m.SettingsMCPApprovalSummary(prompt.Summary)
				revealed := func() bool {
					row, ok := tester.Find(lastText)
					return ok && row.H >= 12 && row.Y >= viewport.Y-0.5 &&
						row.Y+row.H <= viewport.Y+viewport.H+0.5
				}
				for step := 0; step < 240 && !revealed(); step++ {
					tester.Scroll(viewport.X+viewport.W-30, viewport.Y+viewport.H-12, 0, 60)
					tester.Frame()
				}
				if !revealed() {
					t.Fatalf("%s: the last prompt row %q never scrolled into the viewport %+v",
						size.name, lastText, viewport)
				}
				for _, label := range actions {
					after, ok := tester.Find(label)
					if !ok {
						t.Fatalf("%s: the pinned action %q disappeared while scrolling", size.name, label)
					}
					if after != before[label] {
						t.Fatalf("%s: the pinned action %q moved from %+v to %+v while scrolling",
							size.name, label, before[label], after)
					}
				}

				path := filepath.Join(outDir, fmt.Sprintf("mcp-%s-%s.png", locale, size.name))
				writeMCPTestScreenshot(t, tester, path)
			})
		}
	}
}

// save expired-state evidence in English at both client sizes. The expired
// state is localized and asserted in every locale above; this pair is the
// visual record of the state itself.
func TestMCPExpiredStateScreenshots(t *testing.T) {
	outDir := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "natcatty-mcp-policy-screenshots")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := locales.In("en")
	expired := mcpExpiredFixture()
	for _, size := range []struct {
		name          string
		width, height int
	}{{"704x421", 704, 421}, {"1084x661", 1084, 661}} {
		size := size
		t.Run(size.name, func(t *testing.T) {
			service := &fakeMCPService{snapshot: appmcp.Snapshot{
				Mode: appmcp.ModeConfirm, ForcedScope: "__external_mcp__",
				Allowlist: []string{"s1"}, Generation: 2,
				Recent: []appmcp.ApprovalView{expired},
			}}
			state := NewMCPState(service)
			var layout viewLayout
			var context *ui.Context
			view := mcpViewWithLayout(AreaMCP, Config{MCP: state}, &layout)
			tester := ui.NewTester(func(c *ui.Context) {
				context = c
				view(c)
			}, size.width, size.height)
			tester.SetLocale("en")
			tester.Frame()
			dismiss, ok := tester.Find(m.SettingsMCPDismiss())
			if !ok {
				t.Fatalf("the expired Dismiss action is missing: %q", tester.Texts())
			}
			bar := layout.MCPActions.Bounds(context)
			if !insideClientRect(dismiss, size.width, size.height) ||
				dismiss.Y < bar.Y-0.5 || dismiss.Y+dismiss.H > bar.Y+bar.H+0.5 {
				t.Fatalf("the expired Dismiss action %+v is not inside its pinned bar %+v", dismiss, bar)
			}

			// Bring the expired card into the viewport before the evidence
			// render; at 704x421 only part of the status and prompt fits at once.
			viewport := layout.Content.Bounds(context)
			expiredLine := m.SettingsMCPApprovalExpired()
			revealed := func() bool {
				row, ok := tester.Find(expiredLine)
				return ok && row.H >= 12 && row.Y >= viewport.Y-0.5 &&
					row.Y+row.H <= viewport.Y+viewport.H+0.5
			}
			for step := 0; step < 240 && !revealed(); step++ {
				tester.Scroll(viewport.X+viewport.W-30, viewport.Y+viewport.H-12, 0, 60)
				tester.Frame()
			}
			if !revealed() {
				t.Fatalf("%s: the expired line never scrolled into the viewport %+v", size.name, viewport)
			}
			after, ok := tester.Find(m.SettingsMCPDismiss())
			if !ok || after != dismiss {
				t.Fatalf("%s: the pinned Dismiss action moved while scrolling: %+v -> %+v", size.name, dismiss, after)
			}

			path := filepath.Join(outDir, fmt.Sprintf("mcp-expired-en-%s.png", size.name))
			writeMCPTestScreenshot(t, tester, path)
		})
	}
}

func writeMCPTestScreenshot(t *testing.T, tester *ui.Tester, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, tester.Image()); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("screenshot %s was not written: %v", path, err)
	}
	t.Logf("screenshot: %s", path)
}

// TestMCPBodyWarningDangerContrastIsAccessible checks the shared helpers
// against every light and dark preset. The prompt body uses the theme text
// token; the remaining-time warning and the expired danger use the shared
// helpers that ticket 54 introduced.
func TestMCPBodyWarningDangerContrastIsAccessible(t *testing.T) {
	for _, test := range []struct {
		name    string
		dark    bool
		presets []domaintheme.Preset
	}{
		{"light", false, domaintheme.LightPresets()},
		{"dark", true, domaintheme.DarkPresets()},
	} {
		for _, preset := range test.presets {
			theme := uitheme.UI(test.dark, preset, uitheme.Options{})
			for _, fill := range []struct {
				name  string
				color ui.Color
			}{{"surface", theme.Surface}, {"background", theme.Background}} {
				for _, candidate := range []struct {
					name  string
					color ui.Color
				}{
					{"body", mcpBodyTextColor(theme, fill.color)},
					{"warning", mcpWarningTextColor(theme, fill.color)},
					{"danger", mcpDangerTextColor(theme, fill.color)},
				} {
					if ratio := syncColorContrast(candidate.color, fill.color); ratio < 4.5 {
						t.Errorf("%s preset %s on the %s: %s text = %.2f:1, want at least 4.5:1",
							test.name, preset.ID, fill.name, candidate.name, ratio)
					}
				}
			}
		}
	}
}

// TestMCPPromptUsesTheAccessibleTextColors reads the rendered pixels: the
// pending prompt's remaining line carries the shared warning color and the
// expired line carries the shared danger color, not the raw theme tokens.
func TestMCPPromptUsesTheAccessibleTextColors(t *testing.T) {
	m := locales.In("en")
	for _, test := range []struct {
		name   string
		dark   bool
		preset string
	}{
		{"light", false, "snow"},
		{"dark", true, "midnight"},
	} {
		t.Run(test.name, func(t *testing.T) {
			preset, ok := domaintheme.LookupPreset(test.dark, test.preset)
			if !ok {
				t.Fatalf("%s preset is missing", test.preset)
			}
			theme := uitheme.UI(test.dark, preset, uitheme.Options{})
			service := &fakeMCPService{snapshot: mcpSnapshotWithPrompt()}
			state := NewMCPState(service)
			tester := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(theme)
				viewAt(AreaMCP, Config{MCP: state})(c)
			}, 1100, 720)
			tester.SetLocale("en")
			tester.Frame()
			remaining := m.SettingsMCPApprovalRemaining("110")
			rect, ok := tester.Find(remaining)
			if !ok {
				t.Fatalf("the remaining line is missing: %q", tester.Texts())
			}
			if !testerRectHasColor(tester, rect, mcpWarningTextColor(theme, theme.Background)) {
				t.Errorf("the remaining line does not use the accessible warning color")
			}

			expiredService := &fakeMCPService{snapshot: appmcp.Snapshot{
				Mode: appmcp.ModeConfirm, ForcedScope: "__external_mcp__",
				Allowlist: []string{"s1"}, Generation: 2,
				Recent: []appmcp.ApprovalView{mcpExpiredFixture()},
			}}
			expiredState := NewMCPState(expiredService)
			tester = ui.NewTester(func(c *ui.Context) {
				c.SetTheme(theme)
				viewAt(AreaMCP, Config{MCP: expiredState})(c)
			}, 1100, 720)
			tester.SetLocale("en")
			tester.Frame()
			rect, ok = tester.Find(m.SettingsMCPApprovalExpired())
			if !ok {
				t.Fatalf("the expired line is missing: %q", tester.Texts())
			}
			if !testerRectHasColor(tester, rect, mcpDangerTextColor(theme, theme.Background)) {
				t.Errorf("the expired line does not use the accessible danger color")
			}
		})
	}
}
