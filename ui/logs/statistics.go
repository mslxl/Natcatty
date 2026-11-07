package logs

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// historyModeSelector switches the History tab between the command list and
// the summary of the same filtered result set. It is a view selector, not a
// filter: the selected mode survives filter changes so the summary tracks them
// in place.
func historyModeSelector(c *ui.Context, s *State, m locales.Messages) {
	mode := int(s.HistoryMode())
	if ui.Segmented(c, &mode, m.LogsHistoryModeCommands(), m.LogsStatisticsTitle()).Changed() {
		s.SetHistoryMode(HistoryMode(mode))
		c.Invalidate()
	}
}

// historyStatistics renders the summary of the active History filters. It
// reads State.Statistics, which is computed from the same FilteredEntries the
// command list renders, so the two views cannot disagree about the result set.
func historyStatistics(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	stats := s.Statistics()
	if stats.Total == 0 {
		ui.Text(c, m.LogsStatisticsEmpty()).TextColor(t.TextMuted)
		return
	}
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		ui.Column(c).FillWidth().Padding(10).Gap(3).
			Radius(kit.RadiusControl).Background(t.Surface).Children(func() {
			statisticValue(c, m.LogsStatisticsTotal(stats.Total))
			statisticValue(c, m.LogsStatisticsUnique(stats.Unique))
			statisticValue(c, m.LogsStatisticsFailures(stats.Failures))
			if stats.KnownExits > 0 {
				statisticValue(c, m.LogsStatisticsFailureRate(formatRate(stats.FailureRate)))
			} else {
				statisticValue(c, m.LogsStatisticsFailureRateUnknown())
			}
			if stats.DurationsKnown > 0 {
				statisticValue(c, m.LogsStatisticsAverageDuration(formatDuration(stats.AverageDuration)))
				if stats.DurationsUnknown > 0 {
					statisticNote(c, m.LogsStatisticsDurationExcluded(stats.DurationsUnknown), t)
				} else {
					statisticNote(c, m.LogsStatisticsDurationAllKnown(stats.DurationsKnown), t)
				}
			} else {
				statisticValue(c, m.LogsStatisticsAverageDurationUnknown())
			}
		})

		ui.Column(c).FillWidth().Gap(2).Children(func() {
			ui.Text(c, m.LogsStatisticsTopCommands()).FontSize(13).Bold()
			for _, command := range stats.TopCommands {
				ui.Text(c, m.LogsStatisticsRow(command.Command, command.Count)).
					FontSize(12).SingleLine()
			}
		})

		ui.Column(c).FillWidth().Gap(2).Children(func() {
			ui.Text(c, m.LogsStatisticsTopCwds()).FontSize(13).Bold()
			if len(stats.TopCwds) == 0 {
				statisticNote(c, m.LogsStatisticsNoCwds(), t)
			}
			for _, cwd := range stats.TopCwds {
				ui.Text(c, m.LogsStatisticsRow(cwd.CWD, cwd.Count)).
					FontSize(12).SingleLine()
			}
		})

		if stats.BusiestHour != nil {
			statisticValue(c, m.LogsStatisticsBusiestHour(
				fmt.Sprintf("%02d:00", stats.BusiestHour.Hour), stats.BusiestHour.Count))
		} else {
			statisticValue(c, m.LogsStatisticsBusiestHourNone())
		}

		hostLabels := statisticsHostLabels(m, stats.Hosts)
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			ui.Text(c, m.LogsStatisticsHosts()).FontSize(13).Bold()
			for index, host := range stats.Hosts {
				ui.Text(c, m.LogsStatisticsRow(hostLabels[index], host.Count)).
					FontSize(12).SingleLine()
			}
		})
	})
}

// statisticValue renders one summary measurement.
func statisticValue(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(12)
}

// statisticNote renders the explanation under a measurement, so a missing or
// excluded value is never mistaken for a measured zero.
func statisticNote(c *ui.Context, text string, t *ui.Theme) {
	ui.Text(c, text).FontSize(11).TextColor(t.TextMuted)
}

// statisticsHostLabel names one Host/origin group with the recorded label when
// there is one and a localized marker otherwise.
func statisticsHostLabel(m locales.Messages, host domainhistory.HostStat) string {
	if host.Label != "" {
		return host.Label
	}
	switch host.Key {
	case domainhistory.LocalHostKey:
		return m.LogsHostLocal()
	case domainhistory.UnsavedHostKey:
		return m.LogsHostUnsaved()
	default:
		if strings.HasPrefix(host.Key, domainhistory.OriginHostKeyPrefix) || host.Key == "" {
			return m.LogsHistoryUnknown()
		}
		return host.Key
	}
}

// statisticsHostLabels names every Host/origin group in the same order as the
// statistics. A saved Host label shared by more than one identity gains that
// identity as a qualifier, matching the History filter, so two Hosts with the
// same recorded label cannot render as two indistinguishable rows.
func statisticsHostLabels(m locales.Messages, hosts []domainhistory.HostStat) []string {
	shared := make(map[string]int)
	for _, host := range hosts {
		if host.Label != "" {
			shared[strings.ToLower(host.Label)]++
		}
	}
	labels := make([]string, len(hosts))
	for index, host := range hosts {
		label := statisticsHostLabel(m, host)
		if host.Label != "" && shared[strings.ToLower(host.Label)] > 1 &&
			!isHostMarkerKey(host.Key) {
			label = qualifyHostLabel(label, "", host.Key, false)
		}
		labels[index] = label
	}
	return labels
}

// isHostMarkerKey reports a Host statistic key that is not a saved Host
// identity: local, unsaved, an origin, or the unknown group. Those keys are
// already distinct, so their labels need no identity qualifier.
func isHostMarkerKey(key string) bool {
	return key == "" || key == domainhistory.LocalHostKey ||
		key == domainhistory.UnsavedHostKey ||
		strings.HasPrefix(key, domainhistory.OriginHostKeyPrefix)
}

// formatRate renders a 0..1 rate as a percentage, rounded to one decimal and
// without a trailing ".0", so "50%" and "33.3%" are both stable test values.
func formatRate(rate float64) string {
	if rate <= 0 {
		return "0%"
	}
	percent := math.Round(rate*1000) / 10
	return strconv.FormatFloat(percent, 'f', -1, 64) + "%"
}
