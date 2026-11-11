package sysmanager

import (
	"math"
	"testing"

	"github.com/mslxl/natcatty/application/hoststats"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestOverviewCoresHeaderShowsClampedLoadOverCores proves the cores section's
// header is load[0] divided by the core count and clamped to 0–100, and says
// "No data" rather than inventing a value when either side is missing.
func TestOverviewCoresHeaderShowsClampedLoadOverCores(t *testing.T) {
	m := locales.In("en")
	noData := m.SysManagerOverviewNoData()
	core := []hoststats.Core{{Percent: 10}}
	tests := []struct {
		name string
		data appsysmanager.Overview
		want string
	}{
		{"over one hundred clamps", appsysmanager.Overview{
			CPUPerCore: core, LoadAverage: []float64{10}, Cores: 4}, "100%"},
		{"half load", appsysmanager.Overview{
			CPUPerCore: core, LoadAverage: []float64{2, 1, 1}, Cores: 4}, "50%"},
		{"negative clamps to zero", appsysmanager.Overview{
			CPUPerCore: core, LoadAverage: []float64{-2}, Cores: 4}, "0%"},
		{"unknown load", appsysmanager.Overview{
			CPUPerCore: core, LoadAverage: []float64{math.NaN()}, Cores: 4}, noData},
		{"missing load", appsysmanager.Overview{
			CPUPerCore: core, Cores: 4}, noData},
		{"missing core count", appsysmanager.Overview{
			CPUPerCore: core, LoadAverage: []float64{1}}, noData},
		{"missing per-core values", appsysmanager.Overview{
			LoadAverage: []float64{1}, Cores: 4}, noData},
	}
	for _, test := range tests {
		if got := overviewLoadText(test.data, m); got != test.want {
			t.Errorf("%s: header = %q, want %q", test.name, got, test.want)
		}
	}
}

// TestOverviewFormattingUsesOneUnitConvention proves the Overview's memory,
// storage, byte, and rate text keeps its promised units and shows "—" for a
// non-finite value instead of zero.
func TestOverviewFormattingUsesOneUnitConvention(t *testing.T) {
	m := locales.In("en")
	memory := []struct {
		mb   float64
		want string
	}{
		{math.NaN(), "—"},
		{math.Inf(1), "—"},
		{512, "512 MB"},
		{512.5, "512.5 MB"},
		{1024, "1.0 GB"},
		{1536, "1.5 GB"},
	}
	for _, test := range memory {
		if got := memoryText(test.mb); got != test.want {
			t.Errorf("memoryText(%v) = %q, want %q", test.mb, got, test.want)
		}
	}

	storage := []struct {
		gb   float64
		want string
	}{
		{math.NaN(), "—"},
		{0.5, "0.5 GB"},
		{140, "140 GB"},
		{140.5, "140.5 GB"},
		{1024, "1.0 TB"},
		{1536, "1.5 TB"},
	}
	for _, test := range storage {
		if got := storageText(test.gb); got != test.want {
			t.Errorf("storageText(%v) = %q, want %q", test.gb, got, test.want)
		}
	}

	bytes := []struct {
		bytes float64
		want  string
	}{
		{math.NaN(), "—"},
		{math.Inf(1), "—"},
		{0, "0 B"},
		{1000, "1000 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{1 << 30, "1.0 GiB"},
		{1 << 40, "1.0 TiB"},
	}
	for _, test := range bytes {
		if got := byteCount(test.bytes); got != test.want {
			t.Errorf("byteCount(%v) = %q, want %q", test.bytes, got, test.want)
		}
		if test.bytes == 1024 && rateText(test.bytes) != "1.0 KiB/s" {
			t.Errorf("rateText(%v) = %q, want a per-second rate", test.bytes, rateText(test.bytes))
		}
	}
	if got := rateText(math.NaN()); got != "—" {
		t.Errorf("rateText(NaN) = %q, want a dash", got)
	}
	if got := memoryDetail(6144, 8192, m); got != m.SysManagerOverviewUsedOf("6.0 GB", "8.0 GB") {
		t.Errorf("memoryDetail = %q, want the used/total pair", got)
	}
	if got := memoryDetail(math.NaN(), 8192, m); got != m.SysManagerUnknown() {
		t.Errorf("unknown memoryDetail = %q, want a dash", got)
	}
	if got := storageDetail(50, 140, m); got != m.SysManagerOverviewUsedOf("50 GB", "140 GB") {
		t.Errorf("storageDetail = %q, want the used/total pair", got)
	}
}

// TestOverviewUptimeUsesDaysHoursMinutes proves the Uptime pill moves down the
// duration units as the value shrinks and never shows a zero for an unknown.
func TestOverviewUptimeUsesDaysHoursMinutes(t *testing.T) {
	m := locales.In("en")
	tests := []struct {
		seconds float64
		want    string
	}{
		{90061, m.SysManagerDurationDays(1, 1)},
		{3661, m.SysManagerDurationHours(1, 1)},
		{61, m.SysManagerDurationMinutes(1)},
		{0, m.SysManagerUnknown()},
		{math.NaN(), m.SysManagerUnknown()},
	}
	for _, test := range tests {
		if got := uptimeText(test.seconds, m); got != test.want {
			t.Errorf("uptimeText(%v) = %q, want %q", test.seconds, got, test.want)
		}
	}
}
