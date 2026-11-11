package locales

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The System Tool effort added or changed these catalog keys. The audit is
// scoped to them on purpose: the catalogs already carry unrelated legacy keys,
// and this test must fail when a key one of the new surfaces reads exists in
// only some languages. Across the six groups below they hold 131 unique keys.
// The Side Panel group alone holds six tool ids: sidePanelSystem,
// sidePanelSystemNoHost, sidePanelScripts, sidePanelHistory, sidePanelTheme,
// and sidePanelNotes.
var systemToolAuditGroups = map[string][]string{
	"Overview": {
		"sysManagerOverviewCPU",
		"sysManagerOverviewMemory",
		"sysManagerOverviewDisk",
		"sysManagerOverviewNetwork",
		"sysManagerOverviewCores",
		"sysManagerOverviewUsedOf",
		"sysManagerOverviewNetworkDetail",
		"sysManagerOverviewCard",
		"sysManagerOverviewGauge",
		"sysManagerOverviewHistory",
		"sysManagerOverviewLine",
		"sysManagerOverviewPills",
		"sysManagerOverviewGrid",
		"sysManagerOverviewUsage",
		"sysManagerOverviewList",
		"sysManagerOverviewMemoryBar",
		"sysManagerOverviewCoresSection",
		"sysManagerOverviewDisksSection",
		"sysManagerOverviewNetworkSection",
		"sysManagerOverviewProcessesSection",
		"sysManagerOverviewLoading",
		"sysManagerOverviewEmpty",
		"sysManagerOverviewNoData",
		"sysManagerOverviewNoDiskData",
		"sysManagerOverviewNoInterfaceData",
		"sysManagerOverviewNoProcessData",
		"sysManagerOverviewInfo",
		"sysManagerOverviewHostname",
		"sysManagerOverviewResources",
		"sysManagerOverviewLoad",
		"sysManagerOverviewUptime",
		"sysManagerOverviewSystem",
		"sysManagerOverviewOS",
		"sysManagerOverviewKernel",
		"sysManagerOverviewSwap",
		"sysManagerOverviewCoresLabel",
		"sysManagerOverviewDiskIO",
		"sysManagerOverviewRead",
		"sysManagerOverviewWrite",
		"sysManagerOverviewRX",
		"sysManagerOverviewTX",
		"sysManagerPid",
		"sysManagerLatency",
		"sysManagerLatencyValue",
		"sysManagerUnknown",
		"sysManagerRetry",
		"sysManagerRefresh",
		"sysManagerRefreshNow",
		"sysManagerUpdatedSeconds",
		"sysManagerSeconds",
		"sysManagerStatusIdle",
		"sysManagerStatusConnecting",
		"sysManagerStatusLive",
		"sysManagerStatusLogin",
		"sysManagerStatusError",
		"sysManagerStatusEmpty",
		"sysManagerStatusUnsupported",
		"sysManagerStatusUnavailable",
	},
	"Blocking and inline errors": {
		"sysManagerReasonHostKey",
		"sysManagerReasonAuth",
		"sysManagerReasonNetwork",
		"sysManagerReasonTimeout",
		"sysManagerReasonOS",
		"sysManagerReasonCancelled",
		"sysManagerReasonInternal",
		"sysManagerReasonUnsupported",
		"sysManagerReasonPermission",
		"sysManagerReasonNoExec",
		"sysManagerReasonNoData",
		"sysManagerReasonMissingCommand",
		"sysManagerReasonMissingProcesses",
		"sysManagerReasonMissingPorts",
		"sysManagerReasonMissingSystemctl",
		"sysManagerReasonMissingTmux",
		"sysManagerReasonMissingDocker",
		"sysManagerReasonDockerDaemon",
	},
	"System Tab Strip": {
		"sysManagerSystemTabs",
		"sysManagerTabsLayout",
		"sysManagerCollapsedTabs",
		"sysManagerMoreTabs",
		"sysManagerShowTab",
		"sysManagerCollapseTab",
		"sysManagerHideTab",
		"sysManagerMoveTabEarlier",
		"sysManagerMoveTabLater",
		"sysManagerResetLayout",
		"sysManagerTabOverview",
		"sysManagerTabProcesses",
		"sysManagerTabPorts",
		"sysManagerTabServices",
		"sysManagerTabTmux",
		"sysManagerTabDocker",
		"sysManagerTabGPU",
	},
	"tmux and Docker": {
		"sysManagerTabTmux",
		"sysManagerTabDocker",
		"sysManagerNewSession",
		"sysManagerCreate",
		"sysManagerAttach",
		"sysManagerKill",
		"sysManagerStart",
		"sysManagerStop",
		"sysManagerRemove",
		"sysManagerInspect",
		"sysManagerLogs",
		"sysManagerRestart",
		"sysManagerContainers",
		"sysManagerImages",
		"sysManagerDockerView",
		"sysManagerNoSessions",
		"sysManagerNoContainers",
		"sysManagerNoImages",
		"sysManagerNoMatches",
		"sysManagerDocumentFailed",
		"sysManagerDocumentLoading",
		"sysManagerDocumentTruncated",
		"sysManagerDocumentInspect",
		"sysManagerDocumentLogs",
		"sysManagerConfirmTmuxKill",
		"sysManagerConfirmDocker",
	},
	"Other System Tab filters": {
		"sysManagerRunningOnly",
		"sysManagerFilterAll",
		"sysManagerFilterTCP",
		"sysManagerFilterUDP",
		"sysManagerFilterRunning",
		"sysManagerFilterFailed",
		"sysManagerFilterInactive",
		"sysManagerPpid",
	},
	// Six Side Panel tool ids: the System tool, its no-Host state, and the four
	// peer tools whose strip labels render beside it.
	"Side Panel System Tool": {
		"sidePanelSystem",
		"sidePanelSystemNoHost",
		"sidePanelScripts",
		"sidePanelHistory",
		"sidePanelTheme",
		"sidePanelNotes",
	},
}

// systemToolAuditKeys is the deduplicated, sorted union of the groups.
func systemToolAuditKeys() []string {
	var keys []string
	for _, group := range systemToolAuditGroups {
		keys = append(keys, group...)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// TestSystemToolSurfaceKeysAreLocalized proves every string the System Tool's
// new surfaces read exists, non-empty, in every shipped catalog. It names the
// missing key and language instead of demanding parity for the whole catalog.
func TestSystemToolSurfaceKeysAreLocalized(t *testing.T) {
	keys := systemToolAuditKeys()
	if len(keys) < 100 {
		t.Fatalf("the System Tool audit covers %d keys, want the full new-surface set", len(keys))
	}
	catalogs := make(map[string]map[string]string, len(Tags()))
	for _, tag := range Tags() {
		catalogs[tag] = readCatalog(t, tag)
	}
	for group, groupKeys := range systemToolAuditGroups {
		if len(groupKeys) == 0 {
			t.Errorf("the %s audit group is empty", group)
		}
	}
	for _, key := range keys {
		for _, tag := range Tags() {
			message, ok := catalogs[tag][key]
			if !ok {
				t.Errorf("%s catalog lacks the System Tool key %q", tag, key)
				continue
			}
			if strings.TrimSpace(message) == "" {
				t.Errorf("%s catalog has an empty System Tool message for %q", tag, key)
			}
		}
	}
}

// TestSystemToolOverviewAccessibilitySuffixesAreLocalized proves the nine
// Overview accessibility-label suffixes are whole catalog messages, so a
// translated surface does not append an English fragment to a localized name.
func TestSystemToolOverviewAccessibilitySuffixesAreLocalized(t *testing.T) {
	en, zh := In("en"), In("zh-CN")
	const name = "CPU"
	cases := []struct {
		key  string
		en   string
		zhCN string
	}{
		{"sysManagerOverviewCard", en.SysManagerOverviewCard(name), zh.SysManagerOverviewCard(name)},
		{"sysManagerOverviewGauge", en.SysManagerOverviewGauge(name), zh.SysManagerOverviewGauge(name)},
		{"sysManagerOverviewHistory", en.SysManagerOverviewHistory(name), zh.SysManagerOverviewHistory(name)},
		{"sysManagerOverviewLine", en.SysManagerOverviewLine(name), zh.SysManagerOverviewLine(name)},
		{"sysManagerOverviewPills", en.SysManagerOverviewPills(name), zh.SysManagerOverviewPills(name)},
		{"sysManagerOverviewGrid", en.SysManagerOverviewGrid(name), zh.SysManagerOverviewGrid(name)},
		{"sysManagerOverviewUsage", en.SysManagerOverviewUsage(name), zh.SysManagerOverviewUsage(name)},
		{"sysManagerOverviewList", en.SysManagerOverviewList(name), zh.SysManagerOverviewList(name)},
		{"sysManagerOverviewMemoryBar", en.SysManagerOverviewMemoryBar(name), zh.SysManagerOverviewMemoryBar(name)},
	}
	for _, c := range cases {
		if !strings.Contains(c.en, name) {
			t.Errorf("%s drops the element name %q from the English label %q", c.key, name, c.en)
		}
		if !strings.Contains(c.zhCN, name) {
			t.Errorf("%s drops the element name %q from the zh-CN label %q", c.key, name, c.zhCN)
		}
		if c.en == c.zhCN {
			t.Errorf("%s is not translated: en and zh-CN both say %q", c.key, c.en)
		}
	}
	if got := en.SysManagerOverviewCard(name); got != "CPU card" {
		t.Errorf("en card label = %q, want %q", got, "CPU card")
	}
	if got := zh.SysManagerOverviewCard(name); got == "CPU card" || strings.Contains(got, "card") {
		t.Errorf("zh-CN card label = %q, want a translated suffix rather than the English word", got)
	}
}

// TestSystemToolHasNoStandaloneMore proves the only overflow vocabulary is the
// System Tab Strip's Collapsed Tabs menu. No audited string is "More" alone, no
// Side Panel tool is named More, and the strip's two labels are the collapse
// vocabulary the spec allows.
func TestSystemToolHasNoStandaloneMore(t *testing.T) {
	catalogs := make(map[string]map[string]string, len(Tags()))
	for _, tag := range Tags() {
		catalogs[tag] = readCatalog(t, tag)
	}
	en, zh := catalogs["en"], catalogs["zh-CN"]
	if got := en["sysManagerMoreTabs"]; got != "More tabs" {
		t.Errorf("en Collapsed Tabs trigger = %q, want %q", got, "More tabs")
	}
	if got := en["sysManagerCollapsedTabs"]; got != "Collapsed tabs" {
		t.Errorf("en Collapsed Tabs menu title = %q, want %q", got, "Collapsed tabs")
	}
	for _, tag := range Tags() {
		for _, key := range []string{"sysManagerMoreTabs", "sysManagerCollapsedTabs"} {
			if strings.TrimSpace(catalogs[tag][key]) == "" {
				t.Errorf("%s has no Collapsed Tabs vocabulary for %q", tag, key)
			}
		}
	}
	if got, english := zh["sysManagerCollapsedTabs"], en["sysManagerCollapsedTabs"]; got == "" || got == english {
		t.Errorf("zh-CN Collapsed Tabs menu title = %q, want a translation of %q", got, english)
	}
	for _, key := range systemToolAuditKeys() {
		for _, tag := range Tags() {
			value := strings.TrimSpace(catalogs[tag][key])
			if strings.EqualFold(value, "more") || value == "更多" {
				t.Errorf("%s uses %q alone for %q; only the System Tab Strip may overflow",
					tag, value, key)
			}
		}
	}
	// No Side Panel Tool is a "More" entry: a key in the side panel family
	// must not be named or worded as one.
	for _, tag := range Tags() {
		for key, value := range catalogs[tag] {
			if !strings.HasPrefix(key, "sidePanel") {
				continue
			}
			if strings.Contains(strings.ToLower(key), "more") ||
				strings.EqualFold(strings.TrimSpace(value), "more") {
				t.Errorf("%s defines a Side Panel More at %q = %q", tag, key, value)
			}
		}
	}
}

// TestGlossarySystemToolVocabulary proves the project glossary carries the
// spec's System Tool vocabulary: Stats Sample is defined, the System Tool and
// System Tab entries keep the seven tabs including tmux and Docker, and the
// retracted peer-tool vocabulary is gone.
func TestGlossarySystemToolVocabulary(t *testing.T) {
	path := filepath.Join("..", "..", "GLOSSARY.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read GLOSSARY.md: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "**Stats Sample**:") {
		t.Error("GLOSSARY.md has no Stats Sample entry")
	}
	stats, ok := glossaryEntry(text, "Stats Sample")
	if !ok {
		t.Fatal("GLOSSARY.md has no Stats Sample entry")
	}
	if !strings.Contains(stats, "normalized collection of Host metrics") {
		t.Errorf("Stats Sample does not use the spec's definition: %q", stats)
	}
	sevenTabs := []string{"Overview", "Processes", "Ports", "Services", "tmux", "Docker", "GPU"}
	for _, name := range []string{"System Tool", "System Tab"} {
		entry, ok := glossaryEntry(text, name)
		if !ok {
			t.Errorf("GLOSSARY.md has no %s entry", name)
			continue
		}
		if name == "System Tab" && !strings.Contains(entry, "seven") {
			t.Errorf("%s does not say it has seven tabs: %q", name, entry)
		}
		for _, tab := range sevenTabs {
			if !strings.Contains(strings.ToLower(entry), strings.ToLower(tab)) {
				t.Errorf("%s does not name the %s tab: %q", name, tab, entry)
			}
		}
	}
	for _, phrase := range []string{"Side Panel More", "Peer Tool", "Capability Store"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(phrase)) {
			t.Errorf("GLOSSARY.md still uses the retracted peer-tool term %q", phrase)
		}
	}
	for _, state := range []string{"Checking", "Detected", "Unavailable"} {
		if strings.Contains(text, state) {
			t.Errorf("GLOSSARY.md still uses the retracted peer-tool state %q", state)
		}
	}
}

// glossaryEntry returns one **Term**: entry's body, from its heading to the
// next heading.
func glossaryEntry(text, term string) (string, bool) {
	heading := "**" + term + "**:"
	start := strings.Index(text, heading)
	if start < 0 {
		return "", false
	}
	body := text[start+len(heading):]
	if end := strings.Index(body, "\n**"); end >= 0 {
		body = body[:end]
	}
	return body, true
}
