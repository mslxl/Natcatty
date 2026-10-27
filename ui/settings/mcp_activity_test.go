package settings

// Ticket 58's Settings evidence: the last external write outcome is localized
// (including blocked-command, timeout, and truncation notices), uses the shared
// danger/warning contrast helpers, and leaves the approval decisions pinned at
// the real client sizes in both English and Simplified Chinese.

import (
	"fmt"
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

func mcpActivitySnapshot(activity appmcp.ActivityView) appmcp.Snapshot {
	return appmcp.Snapshot{
		Mode:         appmcp.ModeAuto,
		ForcedScope:  "__external_mcp__",
		Allowlist:    []string{"s1"},
		Generation:   3,
		LastActivity: &activity,
	}
}

func mcpActivityFixture(status appmcp.ActivityStatus, rule string) appmcp.ActivityView {
	return appmcp.ActivityView{
		At:     time.Unix(2000, 0),
		Tool:   "terminal_execute",
		Status: status,
		Rule:   rule,
	}
}

func TestMCPActivityShowsTheLocalizedOutcomeAndRule(t *testing.T) {
	cases := []struct {
		status appmcp.ActivityStatus
		label  func(locales.Messages) string
	}{
		{appmcp.ActivityOK, func(m locales.Messages) string { return m.SettingsMCPActivityStatusOK() }},
		{appmcp.ActivityBlocked, func(m locales.Messages) string { return m.SettingsMCPActivityStatusBlocked() }},
		{appmcp.ActivityTimeout, func(m locales.Messages) string { return m.SettingsMCPActivityStatusTimeout() }},
		{appmcp.ActivityTruncated, func(m locales.Messages) string { return m.SettingsMCPActivityStatusTruncated() }},
		{appmcp.ActivityUnavailable, func(m locales.Messages) string { return m.SettingsMCPActivityStatusUnavailable() }},
		{appmcp.ActivityDenied, func(m locales.Messages) string { return m.SettingsMCPActivityStatusDenied() }},
		{appmcp.ActivityFailed, func(m locales.Messages) string { return m.SettingsMCPActivityStatusFailed() }},
	}
	m := locales.In("en")
	for _, test := range cases {
		t.Run(string(test.status), func(t *testing.T) {
			activity := mcpActivityFixture(test.status, "common.rm-recursive-force")
			service := &fakeMCPService{snapshot: mcpActivitySnapshot(activity)}
			state := NewMCPState(service)
			tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
			tester.SetLocale("en")
			tester.Frame()
			for _, text := range []string{
				m.SettingsMCPActivityTitle(),
				m.SettingsMCPActivityTool(activity.Tool),
				test.label(m),
				m.SettingsMCPActivityRule(activity.Rule),
			} {
				if !tester.HasText(text) {
					t.Fatalf("%s: %q is missing: %q", test.status, text, tester.Texts())
				}
			}
		})
	}
}

func TestMCPActivityUsesTheSharedStatusColors(t *testing.T) {
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
			m := locales.In("en")

			blocked := mcpActivityFixture(appmcp.ActivityBlocked, "common.rm-recursive-force")
			service := &fakeMCPService{snapshot: mcpActivitySnapshot(blocked)}
			state := NewMCPState(service)
			tester := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(theme)
				viewAt(AreaMCP, Config{MCP: state})(c)
			}, 1100, 720)
			tester.SetLocale("en")
			tester.Frame()
			rect, ok := tester.Find(m.SettingsMCPActivityStatusBlocked())
			if !ok {
				t.Fatalf("the blocked status is missing: %q", tester.Texts())
			}
			if !testerRectHasColor(tester, rect, mcpDangerTextColor(theme, theme.Surface)) {
				t.Error("the blocked status does not use the shared danger color")
			}

			truncated := mcpActivityFixture(appmcp.ActivityTruncated, "")
			service = &fakeMCPService{snapshot: mcpActivitySnapshot(truncated)}
			state = NewMCPState(service)
			tester = ui.NewTester(func(c *ui.Context) {
				c.SetTheme(theme)
				viewAt(AreaMCP, Config{MCP: state})(c)
			}, 1100, 720)
			tester.SetLocale("en")
			tester.Frame()
			rect, ok = tester.Find(m.SettingsMCPActivityStatusTruncated())
			if !ok {
				t.Fatalf("the truncated status is missing: %q", tester.Texts())
			}
			if !testerRectHasColor(tester, rect, mcpWarningTextColor(theme, theme.Surface)) {
				t.Error("the truncated status does not use the shared warning color")
			}
		})
	}
}

func TestMCPActivityLocalizesInEveryCatalog(t *testing.T) {
	for _, tag := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		t.Run(tag, func(t *testing.T) {
			m := locales.In(tag)
			activity := mcpActivityFixture(appmcp.ActivityTimeout, "common.system-power")
			service := &fakeMCPService{snapshot: mcpActivitySnapshot(activity)}
			state := NewMCPState(service)
			tester := ui.NewTester(viewAt(AreaMCP, Config{MCP: state}), 1100, 720)
			tester.SetLocale(tag)
			tester.Frame()
			for _, text := range []string{
				m.SettingsMCPActivityTitle(),
				m.SettingsMCPActivityTool(activity.Tool),
				m.SettingsMCPActivityStatusTimeout(),
				m.SettingsMCPActivityRule(activity.Rule),
			} {
				if !tester.HasText(text) {
					t.Fatalf("%s: %q is missing: %q", tag, text, tester.Texts())
				}
			}
			if tag != "en" && tester.HasText("Timed out") {
				t.Fatalf("%s: the English status reached the surface: %q", tag, tester.Texts())
			}
		})
	}
}

func revealMCPActivityRow(t *testing.T, tester *ui.Tester, viewport ui.Rect, text string) {
	t.Helper()
	revealed := func() bool {
		row, ok := tester.Find(text)
		return ok && row.H >= 11 && row.Y >= viewport.Y-0.5 &&
			row.Y+row.H <= viewport.Y+viewport.H+0.5
	}
	for step := 0; step < 240 && !revealed(); step++ {
		tester.Scroll(viewport.X+viewport.W-30, viewport.Y+viewport.H-12, 0, 60)
		tester.Frame()
	}
	if !revealed() {
		t.Fatalf("the activity row %q never scrolled into the viewport %+v: %q",
			text, viewport, tester.Texts())
	}
}

// TestMCPWritesEvidenceScreenshotsAtRealClientSizes renders the approval of a
// real executing write, plus blocked-command, timeout, and truncation notices
// at both client sizes in English and Simplified Chinese. Each PNG is written
// to NATCATTY_SCREENSHOT_DIR (or a temp directory) for 100% review.
func TestMCPWritesEvidenceScreenshotsAtRealClientSizes(t *testing.T) {
	outDir := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "natcatty-mcp-writes-screenshots")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sizes := []struct {
		name          string
		width, height int
	}{{"704x421", 704, 421}, {"1084x661", 1084, 661}}

	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range sizes {
			locale, size := locale, size
			t.Run(locale+"/"+size.name, func(t *testing.T) {
				m := locales.In(locale)
				prompt := mcpPendingFixture("mcp_approval_58")
				prompt.Tool = "vault_hosts_create"
				prompt.Target = "write host (h1)"
				prompt.Summary = `hosts="[{\"hostname\":\"new.example\",\"password\":\"[redacted]\"}]"`
				activity := mcpActivityFixture(appmcp.ActivityOK, "")
				service := &fakeMCPService{snapshot: appmcp.Snapshot{
					Mode:         appmcp.ModeConfirm,
					ForcedScope:  "__external_mcp__",
					Allowlist:    []string{"s1"},
					Generation:   4,
					Pending:      []appmcp.ApprovalView{prompt},
					LastActivity: &activity,
				}}
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
				if viewport.W <= 0 || viewport.H <= 0 || bar.W <= 0 || bar.H <= 0 {
					t.Fatalf("missing viewport or pinned bar: %+v %+v", viewport, bar)
				}
				before := map[string]ui.Rect{}
				for _, label := range []string{m.SettingsMCPApprove(), m.SettingsMCPDeny()} {
					rect, ok := tester.Find(label)
					if !ok || !insideClientRect(rect, size.width, size.height) ||
						rect.Y < bar.Y-0.5 || rect.Y+rect.H > bar.Y+bar.H+0.5 {
						t.Fatalf("the pinned action %q is clipped or outside its bar: %+v (%q)",
							label, rect, tester.Texts())
					}
					before[label] = rect
				}
				for _, text := range []string{
					m.SettingsMCPActivityTitle(),
					m.SettingsMCPActivityStatusOK(),
					m.SettingsMCPApprovalTitle(),
					m.SettingsMCPApprovalClient(prompt.Client),
					m.SettingsMCPApprovalTool(prompt.Tool),
					m.SettingsMCPApprovalTarget(prompt.Target),
					m.SettingsMCPApprovalSummary(prompt.Summary),
				} {
					if !tester.HasText(text) {
						t.Fatalf("%s: %q is missing: %q", size.name, text, tester.Texts())
					}
				}
				if size.name == "704x421" {
					// The shortest viewport cannot show the status strip and the
					// whole prompt at once. Keep one top-of-area render where the
					// status and prompt heading are both visible.
					writeMCPTestScreenshot(t, tester, filepath.Join(outDir,
						fmt.Sprintf("mcp-writes-approval-status-%s-%s.png", locale, size.name)))
				}
				revealMCPActivityRow(t, tester, viewport, m.SettingsMCPApprovalSummary(prompt.Summary))
				for label, want := range before {
					after, ok := tester.Find(label)
					if !ok || after != want {
						t.Fatalf("%s: the pinned action %q moved while scrolling: %+v -> %+v",
							size.name, label, want, after)
					}
				}
				writeMCPTestScreenshot(t, tester, filepath.Join(outDir,
					fmt.Sprintf("mcp-writes-approval-%s-%s.png", locale, size.name)))

				notices := []struct {
					name     string
					status   appmcp.ActivityStatus
					rule     string
					label    func(locales.Messages) string
					fileName string
				}{
					{"blocked", appmcp.ActivityBlocked, "common.rm-recursive-force",
						func(m locales.Messages) string { return m.SettingsMCPActivityStatusBlocked() }, "blocked"},
					{"timeout", appmcp.ActivityTimeout, "",
						func(m locales.Messages) string { return m.SettingsMCPActivityStatusTimeout() }, "timeout"},
					{"truncated", appmcp.ActivityTruncated, "",
						func(m locales.Messages) string { return m.SettingsMCPActivityStatusTruncated() }, "truncated"},
				}
				for _, notice := range notices {
					activity := mcpActivityFixture(notice.status, notice.rule)
					service := &fakeMCPService{snapshot: mcpActivitySnapshot(activity)}
					state := NewMCPState(service)
					var noticeLayout viewLayout
					var noticeContext *ui.Context
					noticeView := mcpViewWithLayout(AreaMCP, Config{MCP: state}, &noticeLayout)
					noticeTester := ui.NewTester(func(c *ui.Context) {
						noticeContext = c
						noticeView(c)
					}, size.width, size.height)
					noticeTester.SetLocale(locale)
					noticeTester.Frame()
					noticeViewport := noticeLayout.Content.Bounds(noticeContext)
					target := notice.label(m)
					if notice.rule != "" {
						target = m.SettingsMCPActivityRule(notice.rule)
					}
					revealMCPActivityRow(t, noticeTester, noticeViewport, target)
					if notice.rule != "" && !noticeTester.HasText(m.SettingsMCPActivityRule(notice.rule)) {
						t.Fatalf("%s %s: the matched rule is missing: %q",
							locale, notice.name, noticeTester.Texts())
					}
					writeMCPTestScreenshot(t, noticeTester, filepath.Join(outDir,
						fmt.Sprintf("mcp-writes-%s-%s-%s.png", notice.fileName, locale, size.name)))
				}
			})
		}
	}
}
