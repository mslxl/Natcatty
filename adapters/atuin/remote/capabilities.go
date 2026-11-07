package remote

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// capabilityProtocolVersion is the only version of the capabilities and
// page-size protocols this client implements. A server advertising a higher
// version has changed semantics this build does not know, so the client fails
// closed instead of guessing.
const capabilityProtocolVersion = 1

// capabilityDocument is the pinned server's capabilities response. Version is
// the server's opaque capability token (not a semver). Enforced is not part of
// v18.24.5 but is read defensively: if a server names a capability it requires
// the client to honour, historysync.Capabilities.Check rejects any name this
// client does not implement.
type capabilityDocument struct {
	Version      string                     `json:"version"`
	Capabilities map[string]json.RawMessage `json:"capabilities"`
	Enforced     json.RawMessage            `json:"enforced"`
}

type capabilityValue struct {
	Version  *uint32 `json:"version"`
	PageSize *uint64 `json:"page_size"`
}

// serverVersion is the semver carried in the Atuin-Version response header.
// Atuin's own client only refuses an older server major version; this client
// requires the pinned major exactly and lets historysync.Capabilities.Check
// apply the same rule.
type serverVersion struct {
	Raw   string
	Major int
	Minor int
	Patch int
}

// parseServerVersion parses the Atuin-Version header. The pinned server sends
// "major.minor.patch" and may carry a prerelease or build suffix.
func parseServerVersion(raw string) (serverVersion, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return serverVersion{}, fmt.Errorf("empty Atuin-Version header")
	}
	core := trimmed
	if index := strings.IndexAny(core, "-+"); index >= 0 {
		core = core[:index]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return serverVersion{}, fmt.Errorf("malformed Atuin-Version header")
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		if part == "" {
			return serverVersion{}, fmt.Errorf("malformed Atuin-Version header")
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || strings.HasPrefix(part, "+") {
			return serverVersion{}, fmt.Errorf("malformed Atuin-Version header")
		}
		numbers[index] = number
	}
	return serverVersion{Raw: trimmed, Major: numbers[0], Minor: numbers[1], Patch: numbers[2]}, nil
}

// parseCapabilities converts the server document into the port's capability
// vocabulary. It fails closed for a missing or malformed capability this
// client uses, an unknown protocol version for one of those capabilities, and
// any enforced capability this client does not implement.
func parseCapabilities(document capabilityDocument, version serverVersion) (historysync.Capabilities, error) {
	incompatible := fmt.Errorf("%w: capabilities do not satisfy this client", historysync.ErrIncompatibleServer)

	capabilities := historysync.Capabilities{
		ServerVersion: version.Raw,
		Major:         version.Major,
		Minor:         version.Minor,
		Patch:         version.Patch,
		Values:        make(map[string]int, len(document.Capabilities)),
	}
	if len(document.Enforced) > 0 && string(document.Enforced) != "null" {
		var enforced []string
		if err := json.Unmarshal(document.Enforced, &enforced); err != nil {
			return historysync.Capabilities{}, incompatible
		}
		capabilities.Enforced = enforced
	}

	isRequired := func(name string) bool {
		return name == historysync.CapabilityServer || name == historysync.CapabilityPageSize
	}
	for name, raw := range document.Capabilities {
		var value capabilityValue
		// An unknown capability with a value shape this build does not know is
		// ignored: the server advertises features the client does not have to
		// use. A required capability is validated below.
		if err := json.Unmarshal(raw, &value); err != nil {
			if isRequired(name) {
				return historysync.Capabilities{}, incompatible
			}
			continue
		}
		if value.Version == nil {
			if isRequired(name) {
				return historysync.Capabilities{}, incompatible
			}
			continue
		}
		capabilities.Values[name] = int(*value.Version)
		if name == historysync.CapabilityPageSize {
			if value.PageSize == nil || *value.PageSize == 0 || *value.PageSize > uint64(math.MaxInt) {
				return historysync.Capabilities{}, incompatible
			}
			capabilities.PageSize = int(*value.PageSize)
		}
	}

	// This build implements protocol version 1 of the two required
	// capabilities. A higher version has semantics it does not know.
	for _, name := range historysync.RequiredCapabilities {
		if capabilities.Values[name] > capabilityProtocolVersion {
			return historysync.Capabilities{}, incompatible
		}
	}
	if err := capabilities.Check(); err != nil {
		return historysync.Capabilities{}, incompatible
	}
	return capabilities, nil
}

// parseRecordStatus parses the pinned status document:
// {"hosts":{"<host uuid>":{"history":<max idx>, ...}}}.
func parseRecordStatus(body []byte) ([]historysync.SeriesStatus, error) {
	var document struct {
		// A pointer distinguishes the pinned response's empty object
		// ({"hosts":{}}) from a malformed document that omits hosts entirely.
		Hosts *map[string]map[string]json.RawMessage `json:"hosts"`
	}
	if err := decodeJSON(body, &document); err != nil {
		return nil, err
	}
	if document.Hosts == nil {
		return nil, fmt.Errorf("%w: status has no hosts object", historysync.ErrRemote)
	}
	statuses := make([]historysync.SeriesStatus, 0, len(*document.Hosts))
	for rawHost, tags := range *document.Hosts {
		host, ok := wire.CanonicalUUID(rawHost)
		if !ok {
			return nil, fmt.Errorf("%w: status names a series host that is not a UUID", historysync.ErrRemote)
		}
		for tag, rawIndex := range tags {
			if tag != domainhistory.RecordTagHistory {
				// Other Atuin tags are out of scope and never downloaded. Their
				// values are ignored even when this build does not know their
				// shape.
				continue
			}
			var index *uint64
			if err := json.Unmarshal(rawIndex, &index); err != nil || index == nil {
				return nil, fmt.Errorf("%w: status carries a malformed series index", historysync.ErrRemote)
			}
			if *index > math.MaxInt64 {
				return nil, fmt.Errorf("%w: status series index is out of range", historysync.ErrRemote)
			}
			statuses = append(statuses, historysync.SeriesStatus{
				Series:   historysync.Series{SeriesHost: host, Tag: domainhistory.RecordTagHistory},
				MaxIndex: int64(*index),
			})
		}
	}
	sortStatuses(statuses)
	return statuses, nil
}

// parseWireRecords decodes one page and rejects a page larger than the request,
// a missing array, or a structurally invalid record. Unknown JSON fields are
// ignored by the wire codec. A null page is rejected rather than treated as the
// end of a download.
func parseWireRecords(body []byte, limit int) ([]wire.Record, error) {
	var records []wire.Record
	if err := decodeJSON(body, &records); err != nil {
		return nil, err
	}
	if records == nil {
		return nil, fmt.Errorf("%w: server returned no record array", historysync.ErrRemote)
	}
	if limit > 0 && len(records) > limit {
		return nil, fmt.Errorf("%w: server returned more records than requested", historysync.ErrRemote)
	}
	return records, nil
}
