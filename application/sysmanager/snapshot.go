package sysmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// CollectSnapshotResult is the normalized output of the fixed collectors. A
// collector that the Host lacks or that failed is named in Skipped with its
// typed reason, so one section degrades without losing the others.
type CollectSnapshotResult struct {
	SampledAt  time.Time
	Collectors map[string]json.RawMessage
	Skipped    map[string]string
}

// CollectSnapshot runs the requested built-in collectors against host over one
// lease. It uses the same fixed command templates and host-key trust path as
// the tab sampler; a plugin cannot name a command. The returned payloads are
// host-formatted records (see the JSON tags on Overview, Process, and the
// other sample types).
func (s *Service) CollectSnapshot(ctx context.Context, host domainvault.Host,
	collectors []Tab) (CollectSnapshotResult, error) {
	if s == nil || s.connector == nil {
		return CollectSnapshotResult{}, ErrNoConnector
	}
	if string(host.ID) == "" {
		return CollectSnapshotResult{}, ErrNoHost
	}
	for _, collector := range collectors {
		if !IsSnapshotCollector(collector) {
			return CollectSnapshotResult{}, fmt.Errorf("sysmanager: unknown collector %q", collector)
		}
	}
	if len(collectors) == 0 {
		return CollectSnapshotResult{}, fmt.Errorf("sysmanager: at least one collector is required")
	}
	conn, err := s.connector.Open(ctx, host)
	if err != nil {
		return CollectSnapshotResult{}, err
	}
	defer func() { _ = conn.Close() }()
	caps, err := s.ensureCapabilities(ctx, host, conn, 0, TabOverview)
	if err != nil {
		return CollectSnapshotResult{}, err
	}
	result := CollectSnapshotResult{
		SampledAt:  s.now().UTC(),
		Collectors: make(map[string]json.RawMessage, len(collectors)),
		Skipped:    make(map[string]string),
	}
	seen := make(map[Tab]bool, len(collectors))
	for _, collector := range collectors {
		if seen[collector] {
			continue
		}
		seen[collector] = true
		if ok, reason := caps.Available(collector); !ok {
			result.Skipped[string(collector)] = string(reason)
			continue
		}
		reply, execErr := conn.Exec(ctx, commandFor(collector))
		if execErr != nil {
			result.Skipped[string(collector)] = string(failureReason(execErr))
			continue
		}
		if reply.HasExitCode && reply.ExitCode != 0 && reply.Stdout == "" {
			result.Skipped[string(collector)] =
				string(failureReason(commandFailure(reply, "")))
			continue
		}
		payload, parseErr := marshalCollector(collector, reply.Stdout)
		if parseErr != nil {
			result.Skipped[string(collector)] = string(failureReason(parseErr))
			continue
		}
		result.Collectors[string(collector)] = payload
	}
	return result, nil
}

// jsonFloat turns an unreported metric into zero. The tab surfaces render NaN
// as a dash, but JSON has no NaN, so a snapshot must normalize it before
// encoding or the whole collector would be skipped.
func jsonFloat(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func sanitizeOverview(overview Overview) Overview {
	overview.UptimeSeconds = jsonFloat(overview.UptimeSeconds)
	for index := range overview.LoadAverage {
		overview.LoadAverage[index] = jsonFloat(overview.LoadAverage[index])
	}
	overview.CPUPercent = jsonFloat(overview.CPUPercent)
	overview.MemUsedMB = jsonFloat(overview.MemUsedMB)
	overview.MemTotalMB = jsonFloat(overview.MemTotalMB)
	overview.SwapUsedMB = jsonFloat(overview.SwapUsedMB)
	overview.SwapTotalMB = jsonFloat(overview.SwapTotalMB)
	overview.DiskUsedGB = jsonFloat(overview.DiskUsedGB)
	overview.DiskTotalGB = jsonFloat(overview.DiskTotalGB)
	return overview
}

func marshalCollector(collector Tab, stdout string) (json.RawMessage, error) {
	switch collector {
	case TabOverview:
		sample, err := parseOverview(stdout)
		if err != nil {
			return nil, err
		}
		overview := sample.overview
		overview.CPUPercent = sample.cpuPercent(overviewCounters{})
		return json.Marshal(sanitizeOverview(overview))
	case TabProcesses:
		rows, err := parseProcesses(stdout)
		if err != nil {
			return nil, err
		}
		for index := range rows {
			rows[index].CPUPercent = jsonFloat(rows[index].CPUPercent)
			rows[index].MemPercent = jsonFloat(rows[index].MemPercent)
		}
		return json.Marshal(rows)
	case TabPorts:
		rows, err := parsePorts(stdout)
		if err != nil {
			return nil, err
		}
		return json.Marshal(rows)
	case TabServices:
		rows, err := parseServices(stdout)
		if err != nil {
			return nil, err
		}
		return json.Marshal(rows)
	case TabTmux:
		rows, err := parseTmuxSessions(stdout)
		if err != nil {
			return nil, err
		}
		return json.Marshal(rows)
	case TabDocker:
		containers, images, err := parseDockerList(stdout)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"containers": containers, "images": images})
	case TabGPU:
		info, err := parseGPU(stdout)
		if err != nil {
			return nil, err
		}
		for index := range info.Devices {
			device := &info.Devices[index]
			device.UtilizationPercent = jsonFloat(device.UtilizationPercent)
			device.MemoryUsedMB = jsonFloat(device.MemoryUsedMB)
			device.MemoryTotalMB = jsonFloat(device.MemoryTotalMB)
			device.TemperatureC = jsonFloat(device.TemperatureC)
			device.PowerDrawW = jsonFloat(device.PowerDrawW)
			device.PowerLimitW = jsonFloat(device.PowerLimitW)
			device.FanPercent = jsonFloat(device.FanPercent)
		}
		return json.Marshal(info)
	default:
		return nil, fmt.Errorf("sysmanager: unknown collector %q", collector)
	}
}
