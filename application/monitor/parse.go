package monitor

import (
	"errors"

	"github.com/mslxl/natcatty/application/hoststats"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
)

// Error is a typed monitor failure. Reason is a stable code the surface
// localizes; Cause is a developer diagnostic, never display text.
type Error struct {
	Reason Reason
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	text := "monitor: " + string(e.Reason)
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	return text
}

func (e *Error) Unwrap() error { return e.Cause }

func monitorError(reason Reason, cause error) error {
	return &Error{Reason: reason, Cause: cause}
}

// samplerState is the per-Host delta baseline owned by the shared parser.
type samplerState = hoststats.State

// parseStats delegates to the shared Host Stats Sample parser and translates
// its typed reason into the monitoring surface's reason.
func parseStats(output string) (hoststats.Parsed, error) {
	parsed, err := hoststats.Parse(output)
	if err == nil {
		return parsed, nil
	}
	var typed *hoststats.Error
	if errors.As(err, &typed) {
		reason := ReasonUnsupported
		if typed.Reason == hoststats.ReasonOS {
			reason = ReasonOS
		}
		return parsed, monitorError(reason, typed.Cause)
	}
	return parsed, monitorError(ReasonInternal, err)
}

// domainSample projects the shared Stats Sample onto the monitoring surface's
// Sample without changing any field's meaning.
func domainSample(shared hoststats.Sample) domainmonitor.Sample {
	sample := domainmonitor.Sample{
		At:            shared.SampledAt,
		CPU:           shared.CPU,
		CPUPerCore:    make([]float64, 0, len(shared.CPUPerCore)),
		CPUCores:      shared.Cores,
		MemUsedMB:     shared.MemUsedMB,
		MemTotalMB:    shared.MemTotalMB,
		SwapUsedMB:    shared.SwapUsedMB,
		SwapTotalMB:   shared.SwapTotalMB,
		Disk:          shared.DiskPercent,
		DiskUsedGB:    shared.DiskUsedGB,
		DiskTotalGB:   shared.DiskTotalGB,
		Disks:         append([]domainmonitor.Disk{}, shared.Disks...),
		Rx:            shared.NetworkRxBps,
		Tx:            shared.NetworkTxBps,
		Latency:       shared.LatencyMS,
		GPU:           shared.GPU,
		GPUName:       shared.GPUName,
		GPUMemUsedMB:  shared.GPUMemUsedMB,
		GPUMemTotalMB: shared.GPUMemTotalMB,
		Hostname:      shared.Hostname,
		OSName:        shared.OSName,
		KernelRelease: shared.KernelRelease,
		UptimeSeconds: shared.UptimeSeconds,
		LoadAverage:   append([]float64{}, shared.LoadAverage...),
	}
	for _, core := range shared.CPUPerCore {
		sample.CPUPerCore = append(sample.CPUPerCore, core.Percent)
	}
	sample.Memory = domainmonitor.MemoryPercent(sample)
	return sample
}
