package shell

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/hoststats"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// overviewIdleConnector serves the non-Overview tabs when a test switches
// System Tabs: their own sampling loop needs a Connector, and an empty answer
// is enough. The Overview reads the shared collector, never this connector.
type overviewIdleConnector struct{}

func (overviewIdleConnector) Open(context.Context, domainvault.Host) (appsysmanager.Connection, error) {
	return overviewIdleConnection{}, nil
}

type overviewIdleConnection struct{}

func (overviewIdleConnection) Exec(context.Context, string) (appsysmanager.ExecResult, error) {
	return appsysmanager.ExecResult{}, nil
}

func (overviewIdleConnection) Close() error { return nil }

// This file completes the Overview's content through the same real shell as
// ticket 02: a real System manager State over a real Service over the shared
// Host Stats collector, with a fake Host transport. It covers the four metric
// cards, the disk I/O line, six information pills, the per-core grid, every
// kept disk, the first five interfaces, and the top five processes, in both
// surfaces, both Interface Styles, and en and zh-CN.

// overviewRichDisks has two mounts on one physical capacity key. The Disk card
// must count that capacity once, while the Disks section still lists every
// mount.
const overviewRichDisks = "DISKS:/:10:40:25:fs=ext4:/dev/sda1," +
	"/data:30:60:50:fs=ext4:/dev/sda1,/home:20:80:25:fs=ext4:/dev/sda2"

// overviewRichProcesses is deliberately out of order: the parser must sort by
// memory percent, and the Overview must draw only the first five rows.
const overviewRichProcesses = "PROCS:4242;12.5;postgres,111;9.5;node," +
	"222;22.0;java,333;2.0;sshd,444;7.0;dockerd,555;1.0;bash"

// overviewRichStats is one rich Linux Stats Sample at step*10 seconds. Every
// step keeps the same deltas, so CPU, per-core, disk I/O, and network rates
// stay live and positive across a refresh or a surface reopening.
func overviewRichStats(step int) string {
	coreDeltas := []int{0, 50, 75, 100}
	cores := make([]string, 0, len(coreDeltas))
	for index, delta := range coreDeltas {
		cores = append(cores, fmt.Sprintf("cpu%d:%d:%d",
			index, 100+100*step, 100+delta*step))
	}
	at := func(first, increment int) int { return first + increment*step }
	return fmt.Sprintf("NC_LATENCY_MARK|MODE:linux|CPURAW:%d %d|CORES:4|"+
		"PERCORERAW:%s|MEMINFO:8192 1024 512 512 4096 1024|%s|"+
		"NET:eth0:%d:%d,eth1:%d:%d,wlan0:%d:%d,enp0s3:%d:%d,"+
		"dummy0:%d:%d,extra0:%d:%d|"+
		"DISKIO:%d;vda:%d:%d,|"+
		"HOST:rich-host|OS:Linux 6.8|KERNEL:6.8.0-arch1|UPTIME:%d|LOAD:3.5 3 2",
		at(1000, 1000), at(500, 500), strings.Join(cores, ","),
		overviewRichProcesses,
		at(1000000, 100000), at(2000000, 200000),
		at(500000, 100000), at(600000, 100000),
		at(300000, 100000), at(400000, 100000),
		at(700000, 100000), at(800000, 100000),
		at(900000, 100000), at(1000000, 100000),
		at(1100000, 100000), at(1200000, 100000),
		at(100, 10), at(1000, 200), at(2000, 200),
		at(90061, 10))
}

// overviewRichTransport answers steps samples, each with the same disk table.
func overviewRichTransport(steps int) *overviewTransport {
	outputs := make([]string, 0, steps)
	disks := make([]string, 0, steps)
	for step := 0; step < steps; step++ {
		outputs = append(outputs, overviewRichStats(step))
		disks = append(disks, overviewRichDisks)
	}
	transport := newOverviewTransport(outputs...)
	transport.diskOutputs = disks
	return transport
}

// overviewCoresOnlyStats is a valid Linux sample whose optional lists are all
// empty, so every section shows its own empty text.
const overviewCoresOnlyStats = "NC_LATENCY_MARK|MODE:linux|CPURAW:1000 500|" +
	"CORES:2|MEMINFO:8192 1024 512 512 4096 1024|" +
	"HOST:empty-host|OS:Linux|KERNEL:6.1|UPTIME:100|LOAD:0.5 0.4 0.3"

// overviewManyCoresStats lists more cores than the 260-DIP grid can show, so
// the section must scroll.
func overviewManyCoresStats(cores int) string {
	entries := make([]string, 0, cores)
	for index := 0; index < cores; index++ {
		entries = append(entries, fmt.Sprintf("cpu%d:%d:%d", index, 1000+index, 500))
	}
	return fmt.Sprintf("NC_LATENCY_MARK|MODE:linux|CPURAW:1000 500|CORES:%d|"+
		"PERCORERAW:%s|MEMINFO:8192 1024 512 512 4096 1024|"+
		"HOST:many-cores|OS:Linux|KERNEL:6.1|UPTIME:100|LOAD:1 1 1",
		cores, strings.Join(entries, ","))
}

// overviewMacReply is a complete macOS answer with no per-core counters and no
// disk I/O, which is how macOS differs from Linux.
const overviewMacReply = "NC_LATENCY_MARK|MODE:darwin|CPU:37|CORES:8|" +
	"MEMINFO:16384 12288 0 0 1024 0|PROCS:10;5.0;WindowServer,11;2.0;launchd|" +
	"DISKS:/:10:40:25:apfs:disk1s1|NET:en0:1000:2000|" +
	"HOST:mac-host|OS:macOS 14|KERNEL:23|UPTIME:100|LOAD:1 2 3"

// overviewPresent requires text or its label in the rendered tree. Surfaces
// scroll, so content below the viewport is present with a clipped rect; the
// card geometry checks prove the surface itself, while presence proves every
// section was rendered.
func overviewPresent(t *testing.T, tester *ui.Tester, name, text string) {
	t.Helper()
	if _, ok := tester.Find(text); !ok {
		t.Fatalf("%s: %q is not rendered: %v", name, text, tester.Texts())
	}
}

// overviewMetricDashValues counts rendered values that are exactly the
// unknown marker. The host header's separator is part of a longer text, so a
// metric that fell back to "—" adds a whole text here; a live surface has none.
func overviewMetricDashValues(tester *ui.Tester) int {
	dashes := 0
	for _, text := range tester.Texts() {
		if text == "—" {
			dashes++
		}
	}
	return dashes
}

// overviewExpectNoMetricDash proves no metric value rendered its unknown
// marker, so a mutation that draws dashes while the application state is live
// fails here even if a card or cell label still exists.
func overviewExpectNoMetricDash(t *testing.T, tester *ui.Tester, name, when string) {
	t.Helper()
	if dashes := overviewMetricDashValues(tester); dashes != 0 {
		t.Fatalf("%s: %d metric values are a dash %s: %v",
			name, dashes, when, tester.Texts())
	}
}

// overviewExpectInside finds text and requires it inside outer.
func overviewExpectInside(t *testing.T, tester *ui.Tester, name, text string, outer ui.Rect) ui.Rect {
	t.Helper()
	rect := overviewFind(t, tester, name, text)
	if !overviewInside(outer, rect) {
		t.Fatalf("%s: %q at %+v is outside %+v", name, text, rect, outer)
	}
	return rect
}

// TestSysManagerOverviewFullContentEverywhere proves the four metric cards,
// the disk I/O line, six information pills, the per-core grid, every kept disk,
// the first five interfaces, and the top five processes all render from one
// shared Stats Sample, in both surfaces, both Interface Styles, and en and
// zh-CN.
func TestSysManagerOverviewFullContentEverywhere(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					transport := overviewRichTransport(2)
					h := newOverviewHarness(t, surface, locale, transport)
					overviewWaitState(t, h, "the first rich sample", overviewLive)

					// The first sample establishes the delta baseline; the next
					// one produces live CPU, per-core, disk-I/O, and network rates.
					h.clock.Advance(10 * time.Second)
					h.manager.Refresh()
					state := overviewWaitState(t, h, "the rich rates",
						func(state appsysmanager.TabState) bool {
							return overviewLive(state) && state.Stats.SampledAt.Equal(overviewBaseTime.Add(10*time.Second))
						})
					if state.Stats.CPU != 50 {
						t.Fatalf("%s: CPU = %v, want the 50%% delta", name, state.Stats.CPU)
					}
					if len(state.Stats.CPUPerCore) != 4 {
						t.Fatalf("%s: per-core = %d values, want four", name, len(state.Stats.CPUPerCore))
					}
					wantCores := []float64{100, 50, 25, 0}
					for index, want := range wantCores {
						if got := state.Stats.CPUPerCore[index].Percent; got != want {
							t.Fatalf("%s: C%d = %v, want %v", name, index+1, got, want)
						}
					}
					if state.Stats.DiskReadBps != 10240 || state.Stats.DiskWriteBps != 10240 {
						t.Fatalf("%s: disk I/O = %v/%v B/s, want the 10 KiB/s deltas",
							name, state.Stats.DiskReadBps, state.Stats.DiskWriteBps)
					}
					if state.Stats.NetworkRxBps != 60000 || state.Stats.NetworkTxBps != 70000 {
						t.Fatalf("%s: network = %v/%v B/s, want 60000/70000",
							name, state.Stats.NetworkRxBps, state.Stats.NetworkTxBps)
					}
					// The state can turn live after the last driven frame; draw
					// the live state before reading the tree.
					h.tester.Frame()

					m := locales.In(locale)
					left, right := overviewSurfaceBounds(overviewWidth, surface)
					cards := []struct {
						label   string
						value   string
						detail  string
						gaugeOK bool
					}{
						{
							label:  m.SysManagerOverviewCPU(),
							value:  "50%",
							detail: m.SysManagerOverviewCores(4),
						},
						{
							label:  m.SysManagerOverviewMemory(),
							value:  "75%",
							detail: m.SysManagerOverviewUsedOf("6.0 GB", "8.0 GB"),
						},
						{
							label:  m.SysManagerOverviewDisk(),
							value:  "36%",
							detail: m.SysManagerOverviewUsedOf("50 GB", "140 GB"),
						},
						{
							label:  m.SysManagerOverviewNetwork(),
							value:  "127.0 KiB/s",
							detail: m.SysManagerOverviewNetworkDetail("58.6 KiB/s", "68.4 KiB/s"),
						},
					}
					for _, card := range cards {
						rect := overviewFind(t, h.tester, name, card.label+" card")
						if rect.X < left-0.5 || rect.X+rect.W > right+0.5 {
							t.Fatalf("%s: the %s card %+v is outside the surface %.0f..%.0f",
								name, card.label, rect, left, right)
						}
						overviewExpectInside(t, h.tester, name, card.value, rect)
						overviewExpectInside(t, h.tester, name, card.detail, rect)
						overviewExpectInside(t, h.tester, name, card.label+" gauge", rect)
						overviewExpectInside(t, h.tester, name, card.label+" history", rect)
					}

					// The disk I/O line sits below the cards and uses one binary
					// rate convention with the network.
					overviewPresent(t, h.tester, name, m.SysManagerOverviewDiskIO()+" line")
					overviewPresent(t, h.tester, name,
						m.SysManagerOverviewRead()+" 10.0 KiB/s · "+
							m.SysManagerOverviewWrite()+" 10.0 KiB/s")

					// Six pills: Load, Uptime, System, Kernel, Swap, and the
					// stats-command round trip labeled Latency. Hostname is not one.
					overviewPresent(t, h.tester, name, m.SysManagerOverviewInfo()+" pills")
					for _, pill := range []struct{ label, value string }{
						{m.SysManagerOverviewLoad(), "3.50 3.00 2.00"},
						{m.SysManagerOverviewUptime(), m.SysManagerDurationDays(1, 1)},
						{m.SysManagerOverviewKernel(), "6.8.0-arch1"},
						{m.SysManagerOverviewSwap(), m.SysManagerOverviewUsedOf("3.0 GB", "4.0 GB")},
						{m.SysManagerLatency(), m.SysManagerLatencyValue(0)},
					} {
						overviewPresent(t, h.tester, name, pill.label)
						overviewPresent(t, h.tester, name, pill.value)
					}
					// "System" also names the navigation column, so its pill is
					// proved by the value only.
					overviewPresent(t, h.tester, name, "Linux 6.8")
					overviewMissing(t, h.tester, name, m.SysManagerOverviewHostname())
					overviewMissing(t, h.tester, name, "rich-host")

					// Every per-core value is in the C1..Cn grid, whose header is
					// load[0] over the core count, clamped to 0–100.
					overviewPresent(t, h.tester, name, m.SysManagerOverviewCoresSection()+" grid")
					overviewPresent(t, h.tester, name, "88%")
					for _, text := range []string{"C1 usage", "C4 usage", "100%", "25%", "0%"} {
						overviewPresent(t, h.tester, name, text)
					}

					// Every kept mount, including two mounts on one capacity,
					// keeps its own row; the card aggregate counted it once.
					overviewPresent(t, h.tester, name, m.SysManagerOverviewDisksSection()+" list")
					for _, text := range []string{
						m.SysManagerOverviewUsedOf("10 GB", "40 GB"),
						m.SysManagerOverviewUsedOf("30 GB", "60 GB"),
						m.SysManagerOverviewUsedOf("20 GB", "80 GB"),
						"/", "/data", "/home",
						"/ usage", "/data usage", "/home usage",
					} {
						overviewPresent(t, h.tester, name, text)
					}

					// The first five interfaces carry cumulative counters and
					// rates; the sixth is not drawn.
					overviewPresent(t, h.tester, name,
						m.SysManagerOverviewNetworkSection()+" list")
					for _, text := range []string{"eth0", "eth1", "wlan0", "enp0s3", "dummy0"} {
						overviewPresent(t, h.tester, name, text)
					}
					overviewMissing(t, h.tester, name, "extra0")
					overviewPresent(t, h.tester, name,
						m.SysManagerOverviewRX()+" 1.0 MiB · 9.8 KiB/s")
					overviewPresent(t, h.tester, name,
						m.SysManagerOverviewTX()+" 2.1 MiB · 19.5 KiB/s")

					// The top five rows come from the Stats Sample's own list,
					// sorted by memory, and the sixth is not drawn.
					overviewPresent(t, h.tester, name,
						m.SysManagerOverviewProcessesSection()+" list")
					for _, text := range []string{
						"java", "postgres", "node", "dockerd", "sshd",
						"22%", m.SysManagerPid() + " 222",
					} {
						overviewPresent(t, h.tester, name, text)
					}
					overviewMissing(t, h.tester, name, "bash")

					commands := transport.received()
					if len(commands) != 4 ||
						commands[0] != hoststats.StatsCommand() ||
						commands[1] != hoststats.DiskCommand() ||
						commands[2] != hoststats.StatsCommand() ||
						commands[3] != hoststats.DiskCommand() {
						t.Fatalf("%s: the transport received %q, want Stats, Disk, Stats, Disk",
							name, commands)
					}
				})
			}
		}
	}
}

// TestSysManagerOverviewSectionsShowEmptyTexts proves a Host with no cores,
// disks, interfaces, or processes renders each section's own empty text, not a
// zero or an empty box.
func TestSysManagerOverviewSectionsShowEmptyTexts(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					h := newOverviewHarness(t, surface, locale,
						newOverviewTransport(overviewCoresOnlyStats))
					overviewWaitState(t, h, "the empty-list sample", overviewLive)
					h.tester.Frame()
					m := locales.In(locale)
					for _, text := range []string{
						m.SysManagerOverviewNoData(),
						m.SysManagerOverviewNoDiskData(),
						m.SysManagerOverviewNoInterfaceData(),
						m.SysManagerOverviewNoProcessData(),
					} {
						overviewPresent(t, h.tester, name, text)
					}
					overviewMissing(t, h.tester, name, "0%")
				})
			}
		}
	}
}

// TestSysManagerOverviewMacOSGaps proves macOS renders the cards and pills it
// has, says "No data" for per-core values, and keeps the disk I/O line's rates
// as dashes rather than zeros.
func TestSysManagerOverviewMacOSGaps(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					transport := newOverviewTransport(overviewMacReply)
					h := newOverviewHarness(t, surface, locale, transport)
					state := overviewWaitState(t, h, "the macOS sample", overviewLive)
					if len(state.Stats.CPUPerCore) != 0 || !math.IsNaN(state.Stats.DiskReadBps) {
						t.Fatalf("%s: macOS carried per-core %v and disk I/O %v",
							name, state.Stats.CPUPerCore, state.Stats.DiskReadBps)
					}
					h.tester.Frame()

					m := locales.In(locale)
					overviewPresent(t, h.tester, name, m.SysManagerOverviewCPU()+" card")
					overviewPresent(t, h.tester, name, "37%")
					overviewPresent(t, h.tester, name, m.SysManagerOverviewCores(8))
					overviewPresent(t, h.tester, name, m.SysManagerOverviewNoData())
					overviewMissing(t, h.tester, name, "C1 usage")
					overviewPresent(t, h.tester, name,
						m.SysManagerOverviewDiskIO()+" line")
					overviewPresent(t, h.tester, name,
						m.SysManagerOverviewRead()+" — · "+m.SysManagerOverviewWrite()+" —")
					overviewPresent(t, h.tester, name, m.SysManagerLatencyValue(0))
					overviewMissing(t, h.tester, name, "0 B/s")

					if commands := transport.received(); len(commands) != 1 ||
						commands[0] != hoststats.StatsCommand() {
						t.Fatalf("%s: macOS ran %q, want one Stats Sample and no disk probe",
							name, commands)
					}
				})
			}
		}
	}
}

// TestSysManagerOverviewCardsBreakAtThreeHundredTwenty proves the side panel
// keeps two card columns at its normal width and at 320 DIP, collapses to one
// below it, and the full page keeps the same two-column information
// architecture at its width.
func TestSysManagerOverviewCardsBreakAtThreeHundredTwenty(t *testing.T) {
	// waitForCards proves two cards share a row, or that the second starts a
	// new row, and that both have a real rect.
	waitForCards := func(t *testing.T, tester *ui.Tester, name string, firstLabel, secondLabel string, two bool) {
		t.Helper()
		overviewFrameUntil(t, tester, name, func() bool {
			first, okFirst := tester.Find(firstLabel)
			second, okSecond := tester.Find(secondLabel)
			if !okFirst || !okSecond || first.W <= 0 || first.H <= 0 ||
				second.W <= 0 || second.H <= 0 {
				return false
			}
			if two {
				return second.X >= first.X+first.W-1 &&
					second.Y >= first.Y-1 && second.Y <= first.Y+1
			}
			return second.X <= first.X+1 && second.Y >= first.Y+first.H-1
		})
	}
	// waitForSecondRow proves the Disk and Network cards follow the first row,
	// either as a second two-column row or stacked below it.
	waitForSecondRow := func(t *testing.T, tester *ui.Tester, name string,
		diskLabel, networkLabel, previousLabel string, two bool) {
		t.Helper()
		overviewFrameUntil(t, tester, name, func() bool {
			previous, okPrevious := tester.Find(previousLabel)
			disk, okDisk := tester.Find(diskLabel)
			network, okNetwork := tester.Find(networkLabel)
			if !okPrevious || !okDisk || !okNetwork ||
				previous.W <= 0 || previous.H <= 0 ||
				disk.W <= 0 || disk.H <= 0 || network.W <= 0 || network.H <= 0 {
				return false
			}
			if disk.Y < previous.Y+previous.H-1 {
				return false
			}
			if two {
				return network.X >= disk.X+disk.W-1 &&
					network.Y >= disk.Y-1 && network.Y <= disk.Y+1
			}
			return network.X <= disk.X+1 && network.Y >= disk.Y+disk.H-1
		})
	}
	checkRows := func(t *testing.T, tester *ui.Tester, name string, cpuLabel, memoryLabel,
		diskLabel, networkLabel string, two bool) {
		t.Helper()
		waitForCards(t, tester, name+" first row", cpuLabel, memoryLabel, two)
		waitForSecondRow(t, tester, name+" second row", diskLabel, networkLabel,
			memoryLabel, two)
	}
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			t.Run(surface.String()+"/"+style.name, func(t *testing.T) {
				overviewUseStyle(t, style.style)
				h := newOverviewHarness(t, surface, "en",
					newOverviewTransport(overviewMacReply))
				overviewWaitState(t, h, "the live sample", overviewLive)
				m := locales.In("en")
				cpuLabel := m.SysManagerOverviewCPU() + " card"
				memoryLabel := m.SysManagerOverviewMemory() + " card"
				diskLabel := m.SysManagerOverviewDisk() + " card"
				networkLabel := m.SysManagerOverviewNetwork() + " card"

				if surface == overviewFullPage {
					// The full page always uses the same two-by-two layout; its
					// extra width goes to the cards and sections.
					checkRows(t, h.tester, "the full page's card rows",
						cpuLabel, memoryLabel, diskLabel, networkLabel, true)
					return
				}

				width := h.shell.FilesWidth(overviewWidth)
				*width = 420
				checkRows(t, h.tester, "the normal panel's card rows",
					cpuLabel, memoryLabel, diskLabel, networkLabel, true)

				*width = 320
				checkRows(t, h.tester, "the 320 DIP card rows",
					cpuLabel, memoryLabel, diskLabel, networkLabel, true)

				*width = 319
				checkRows(t, h.tester, "the 319 DIP card rows",
					cpuLabel, memoryLabel, diskLabel, networkLabel, false)
			})
		}
	}
}

// TestSysManagerOverviewLiveRatesSurviveTabSwitches proves per-core, disk-I/O,
// and network rates stay live after leaving the Overview and returning, and
// after closing and reopening the side panel.
func TestSysManagerOverviewLiveRatesSurviveTabSwitches(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			for _, locale := range overviewLocales {
				name := fmt.Sprintf("%s/%s/%s", surface, style.name, locale)
				t.Run(name, func(t *testing.T) {
					overviewUseStyle(t, style.style)
					h := newOverviewHarnessWithConnector(t, surface, locale,
						overviewRichTransport(4), 60, overviewDefaultHost(),
						overviewIdleConnector{})
					overviewWaitState(t, h, "the first rich sample", overviewLive)

					h.clock.Advance(10 * time.Second)
					h.manager.Refresh()
					overviewWaitState(t, h, "the rich rates", func(state appsysmanager.TabState) bool {
						return overviewLive(state) && !math.IsNaN(state.Stats.DiskReadBps)
					})
					h.tester.Frame()

					m := locales.In(locale)
					diskLine := m.SysManagerOverviewRead() + " 10.0 KiB/s · " +
						m.SysManagerOverviewWrite() + " 10.0 KiB/s"
					overviewFind(t, h.tester, name, diskLine)

					// Leave the Overview: the strip draws another tab, so the
					// Overview's cards are gone.
					h.manager.SetTab(appsysmanager.TabProcesses)
					overviewFrameUntil(t, h.tester, "the Processes tab", func() bool {
						_, present := h.tester.Find(m.SysManagerOverviewCPU() + " card")
						return !present
					})

					networkDetail := m.SysManagerOverviewNetworkDetail(
						"58.6 KiB/s", "68.4 KiB/s")
					// Return with the clock moved on, so a new sample carries
					// another positive delta. The concrete cell percentages and
					// the absence of a dash prove the values rendered, not just
					// that the grid's labels exist.
					h.clock.Advance(10 * time.Second)
					h.manager.SetTab(appsysmanager.TabOverview)
					overviewFrameUntil(t, h.tester, "the live Overview rates", func() bool {
						return h.tester.HasText(diskLine) &&
							h.tester.HasText(networkDetail) &&
							h.tester.HasText("100%") &&
							h.tester.HasText("25%") &&
							h.tester.HasText("0%") &&
							overviewMetricDashValues(h.tester) == 0
					})
					for _, text := range []string{"C1 usage", "C3 usage", "C4 usage",
						"100%", "25%", "0%", diskLine, networkDetail} {
						overviewPresent(t, h.tester, name, text)
					}
					overviewExpectNoMetricDash(t, h.tester, name, "after the tab switch")

					if surface == overviewSidePanel {
						// Close the whole panel: the Overview's cards leave the
						// frame and the hidden System Tool stops sampling.
						h.shell.CloseFiles(h.tab)
						overviewFrameUntil(t, h.tester, "the side panel to close", func() bool {
							_, present := h.tester.Find(m.SysManagerOverviewCPU() + " card")
							return !present
						})

						// Reopening shows the System Tool again. A Refresh asks
						// the shared collector for the next delta at the moved
						// clock: the baseline is the +10s sample, so a +30s
						// sample spreads the same counters over 20 seconds.
						h.clock.Advance(10 * time.Second)
						reopenedAt := overviewBaseTime.Add(30 * time.Second)
						if !h.shell.ToggleActiveFiles() {
							t.Fatal("the side panel did not reopen")
						}
						overviewFrameUntil(t, h.tester, "the reopened side panel", func() bool {
							_, present := h.tester.Find(m.SysManagerOverviewCPU() + " card")
							return present
						})
						h.manager.Refresh()
						reopened := overviewWaitState(t, h, "the reopened sample",
							func(state appsysmanager.TabState) bool {
								return overviewLive(state) &&
									state.Stats.SampledAt.Equal(reopenedAt) &&
									state.Stats.DiskReadBps > 0 &&
									state.Stats.DiskWriteBps > 0 &&
									state.Stats.NetworkRxBps > 0 &&
									state.Stats.NetworkTxBps > 0
							})
						if reopened.Stats.NetworkRxBps <= 0 || reopened.Stats.NetworkTxBps <= 0 {
							t.Fatalf("%s: reopened network = %v/%v B/s, want live rates",
								name, reopened.Stats.NetworkRxBps, reopened.Stats.NetworkTxBps)
						}
						h.tester.Frame()
						overviewPresent(t, h.tester, name,
							m.SysManagerOverviewDiskIO()+" line")
						// The reopened Refresh sees the same counters spread
						// over the 20 seconds since the last Overview sample.
						// Network rates are halved; disk I/O has two steps of
						// counters, so its rate stays at the same concrete value.
						reopenedDiskLine := m.SysManagerOverviewRead() + " 10.0 KiB/s · " +
							m.SysManagerOverviewWrite() + " 10.0 KiB/s"
						reopenedNetworkDetail := m.SysManagerOverviewNetworkDetail(
							"29.3 KiB/s", "34.2 KiB/s")
						for _, text := range []string{"C1 usage", "C3 usage", "C4 usage",
							"100%", "25%", "0%", reopenedDiskLine, reopenedNetworkDetail} {
							overviewPresent(t, h.tester, name, text)
						}
						overviewExpectNoMetricDash(t, h.tester, name,
							"after reopening the side panel")
						if !h.tester.HasText(reopenedDiskLine) {
							t.Fatalf("%s: the reopened disk I/O is %v, want %q",
								name, h.tester.Texts(), reopenedDiskLine)
						}
						if !h.tester.HasText(reopenedNetworkDetail) {
							t.Fatalf("%s: the reopened network detail is %v, want %q",
								name, h.tester.Texts(), reopenedNetworkDetail)
						}
					}
				})
			}
		}
	}
}

// overviewToneStats is a Linux sample with diska usage at 90, 70, and 50
// percent, so the same frame draws danger, warning, and accent bars.
const overviewToneStats = "NC_LATENCY_MARK|MODE:linux|CPURAW:1000 500|CORES:2|" +
	"MEMINFO:8192 1024 512 512 4096 1024|HOST:tone-host|OS:Linux|KERNEL:6.1|" +
	"UPTIME:100|LOAD:0.5 0.4 0.3"

const overviewToneDisks = "DISKS:/:90:100:90:fs=ext4:/dev/sda1," +
	"/data:70:100:70:fs=ext4:/dev/sda2,/home:50:100:50:fs=ext4:/dev/sda3"

// TestSysManagerOverviewToneThresholdsMatchTheKit proves the Overview's
// resource bars use the shared thresholds in the browser: above 85 percent
// danger, above 60 warning, otherwise accent, in both Interface Styles.
func TestSysManagerOverviewToneThresholdsMatchTheKit(t *testing.T) {
	for _, style := range overviewStyles {
		for _, surface := range overviewSurfaces {
			t.Run(surface.String()+"/"+style.name, func(t *testing.T) {
				overviewUseStyle(t, style.style)
				transport := newOverviewTransport(overviewToneStats)
				transport.diskOutputs = []string{overviewToneDisks}
				h := newOverviewHarness(t, surface, "en", transport)
				overviewWaitState(t, h, "the tone sample", overviewLive)

				bars := []struct {
					label string
					tone  kit.GaugeTone
				}{
					{"/ usage", kit.GaugeDanger},
					{"/data usage", kit.GaugeWarning},
					{"/home usage", kit.GaugeAccent},
				}
				// Scroll the surface that owns the disk rows: the whole page,
				// or the side panel at its own width.
				scrollX := float32(overviewWidth / 2)
				if surface == overviewSidePanel {
					scrollX = overviewSideWidth / 2
				}
				var rects []ui.Rect
				overviewFrameUntil(t, h.tester, "the disk usage bars", func() bool {
					rects = rects[:0]
					for _, bar := range bars {
						rect, ok := h.tester.Find(bar.label)
						if !ok || rect.W <= 0 || rect.H <= 0 ||
							rect.Y < 0 || rect.Y+rect.H > overviewHeight {
							h.tester.Scroll(scrollX, overviewHeight/2, 0, 100)
							return false
						}
						rects = append(rects, rect)
					}
					return true
				})

				image := h.tester.Image()
				theme := ui.LightTheme()
				for index, bar := range bars {
					rect := rects[index]
					x := int(rect.X + rect.W*0.25)
					y := int(rect.Y + rect.H/2)
					got := image.RGBAAt(x, y)
					want := kit.GaugeColor(theme, bar.tone)
					if got.R != want.R || got.G != want.G || got.B != want.B {
						t.Errorf("%s: bar at %v is %v, want %v",
							bar.label, ui.Rect{X: rect.X, Y: rect.Y}, got, want)
					}
				}
			})
		}
	}
}

// TestSysManagerOverviewCoreGridScrollsAtItsCap proves a long per-core list is
// capped at 260 DIP and still holds every C1..Cn value in a two-column grid.
func TestSysManagerOverviewCoreGridScrollsAtItsCap(t *testing.T) {
	for _, surface := range overviewSurfaces {
		for _, style := range overviewStyles {
			name := fmt.Sprintf("%s/%s", surface, style.name)
			t.Run(name, func(t *testing.T) {
				overviewUseStyle(t, style.style)
				h := newOverviewHarness(t, surface, "en",
					newOverviewTransport(overviewManyCoresStats(32)))
				state := overviewWaitState(t, h, "the many-core sample", overviewLive)
				if len(state.Overview.CPUPerCore) != 32 {
					t.Fatalf("%s: the projection carries %d cores, want 32",
						name, len(state.Overview.CPUPerCore))
				}
				h.tester.Frame()
				m := locales.In("en")
				label := m.SysManagerOverviewCoresSection() + " grid"
				grid, ok := h.tester.Find(label)
				if !ok || grid.H <= 0 {
					// The grid starts below the side panel's viewport; scroll
					// the page until it is visible, checking a real rect.
					overviewFrameUntil(t, h.tester, "the visible core grid", func() bool {
						rect, found := h.tester.Find(label)
						if found && rect.W > 0 && rect.H > 0 {
							grid = rect
							return true
						}
						h.tester.Scroll(overviewSideWidth/2, 200, 0, 100)
						return false
					})
				}
				if grid.H > 260+1 {
					t.Fatalf("%s: the core grid is %.0f DIP high, want at most 260", name, grid.H)
				}
				c1 := overviewFind(t, h.tester, name, "C1 usage")
				c2 := overviewFind(t, h.tester, name, "C2 usage")
				if c2.X < c1.X+c1.W-1 || c2.Y < c1.Y-1 || c2.Y > c1.Y+1 {
					t.Fatalf("%s: C2 at %+v is not the second column of C1's row %+v",
						name, c2, c1)
				}
				c3 := overviewFind(t, h.tester, name, "C3 usage")
				if c3.Y < c1.Y+c1.H-1 || c3.X > c1.X+1 {
					t.Fatalf("%s: C3 at %+v does not start the next two-column row below %+v",
						name, c3, c1)
				}
				// The capped grid scrolls; the last core becomes visible after
				// one downward scroll inside it.
				h.tester.Scroll(grid.X+grid.W/2, grid.Y+grid.H/2, 0, 400)
				overviewFrameUntil(t, h.tester, "the last core after scrolling", func() bool {
					_, ok := h.tester.Find("C32 usage")
					return ok
				})
				overviewPresent(t, h.tester, name, m.SysManagerOverviewCores(32))
			})
		}
	}
}
