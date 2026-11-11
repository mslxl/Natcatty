package hoststats

import (
	"strings"
	"testing"
)

func TestStatsCommandCoversBothOperatingSystems(t *testing.T) {
	command := StatsCommand()
	for _, want := range []string{
		"uname -s", "Darwin", "Linux", "UNSUPPORTED_OS", "/proc/stat", "vm_stat",
		"ps -eo pid,%mem,comm", "netstat -ibn", "$0~/<Link#/", "DISKIO:", "PROCS:",
		"MODE:linux", "MODE:darwin", "SReclaimable", "/proc/uptime", "/sys/block",
	} {
		if !strings.Contains(command, want) {
			t.Errorf("stats command is missing %q", want)
		}
	}
	if len(command) > 9000 {
		t.Errorf("stats command is %d bytes, above the Dropbear 9000-byte budget", len(command))
	}
	t.Logf("stats command is %d bytes", len(command))
	if command == DiskCommand() {
		t.Error("disk probe must be a separate command")
	}
	if !strings.Contains(DiskCommand(), "df -kP") {
		t.Error("disk probe is missing df -kP")
	}
}

func TestLinuxReplySelectsTheDiskProbe(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"linux mode", "NC_LATENCY_MARK|MODE:linux|CPURAW:1 1|", true},
		{"linux without mode", "NC_LATENCY_MARK|CPURAW:1 1|", true},
		{"macOS", "NC_LATENCY_MARK|MODE:darwin|CPU:10|", false},
		{"macOS without mode", "NC_LATENCY_MARK|CPU:10|", false},
		{"unsupported", "NC_LATENCY_MARK|UNSUPPORTED_OS:FreeBSD", false},
		{"empty", "", false},
	}
	for _, test := range tests {
		if got := LinuxReply(test.output); got != test.want {
			t.Errorf("%s: LinuxReply = %v, want %v", test.name, got, test.want)
		}
	}
}
