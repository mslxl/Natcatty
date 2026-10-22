package vaultimport

import (
	"encoding/json"
	"strings"
)

// jsonDocument is the versioned Netcatty JSON envelope. The exporter writes
// KindNetcattyVault and version 1; imports accept the same.
type jsonDocument struct {
	Kind    string     `json:"kind"`
	Version int        `json:"version"`
	Hosts   []jsonHost `json:"hosts"`
	Keys    []jsonKey  `json:"keys"`
}

type jsonHost struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Hostname string   `json:"hostname"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	Protocol string   `json:"protocol"`
	Group    string   `json:"group"`
	Tags     []string `json:"tags"`
	Notes    string   `json:"notes"`
	// KeyPath, Passphrase, and Proxy are marked fields, exactly as in CSV.
	KeyPath    string `json:"keyPath"`
	Passphrase string `json:"passphrase"`
	Proxy      string `json:"proxy"`
	Password   string `json:"password"`
	KeyID      string `json:"keyId"`
	HostChain  *struct {
		HostIDs []string `json:"hostIds"`
	} `json:"hostChain"`
}

type jsonKey struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	PublicKey  string `json:"publicKey"`
	FilePath   string `json:"filePath"`
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
}

// parseJSON reads a Netcatty JSON document. Hosts without a usable hostname
// are reported by row and skipped; an unknown protocol never becomes SSH.
func parseJSON(text string) Result {
	result := Result{Format: FormatNetcattyJSON}
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	var document jsonDocument
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		result.Issues = append(result.Issues, errorIssue(IssueInvalidJSON, 0, err.Error()))
		return result
	}
	if document.Kind != "" && document.Kind != KindNetcattyVault {
		result.Issues = append(result.Issues, errorIssue(IssueUnsupportedEntry, 0, document.Kind))
		return result
	}
	if document.Version != 0 && document.Version != 1 {
		result.Issues = append(result.Issues, errorIssue(IssueUnsupportedEntry, 0, "version"))
		return result
	}

	keys := make(map[string]jsonKey, len(document.Keys))
	for _, key := range document.Keys {
		if key.ID != "" {
			keys[key.ID] = key
		}
	}
	// JumpRefs index Result.Records, not document.Hosts, so skipped invalid
	// hosts must not shift them.
	recordIndexByID := make(map[string]int, len(document.Hosts))
	var pendingChains [][]string

	for index, source := range document.Hosts {
		row := index + 1
		hostname := strings.TrimSpace(source.Hostname)
		if hostname == "" || !validHostname(hostname) {
			result.Issues = append(result.Issues,
				issue(IssueInvalidHostname, row, "hostname", hostname))
			continue
		}
		protocol := normalizeProtocol(source.Protocol)
		if source.Protocol != "" && protocol == "" {
			result.Issues = append(result.Issues,
				issue(IssueUnsupportedProtocol, row, "protocol", source.Protocol))
			continue
		}
		if protocol == "" {
			protocol = "ssh"
		}
		record := Record{
			Row:      row,
			Source:   "json",
			Label:    strings.TrimSpace(source.Label),
			Hostname: hostname,
			Username: strings.TrimSpace(source.Username),
			Port:     source.Port,
			Protocol: protocol,
			Group:    normalizeGroup(source.Group),
			Tags:     append([]string(nil), source.Tags...),
			Notes:    source.Notes,
			Password: source.Password,
		}
		if source.Port != 0 && (source.Port < 1 || source.Port > 65535) {
			result.Issues = append(result.Issues,
				issue(IssueInvalidPort, row, "port", itoa(source.Port)))
			record.Port = 0
		}
		if source.KeyPath != "" {
			record.KeyPath = DecodeMarkedField(source.KeyPath, MarkerKeyPath)
		}
		if source.Passphrase != "" {
			record.Passphrase = DecodeMarkedField(source.Passphrase, MarkerPassphrase)
		}
		if key, found := keys[source.KeyID]; found {
			record.KeyRef = key.ID
			if record.KeyPath == "" {
				record.KeyPath = strings.TrimSpace(key.FilePath)
			}
			record.KeyMaterial = key.PrivateKey
			if record.Passphrase == "" {
				record.Passphrase = key.Passphrase
			}
			record.KeyLabelHint = strings.TrimSpace(key.Label)
		}
		if source.Proxy != "" {
			decoded := DecodeMarkedField(source.Proxy, MarkerProxy)
			proxy, ok := parseProxy(decoded)
			if !ok {
				result.Issues = append(result.Issues,
					issue(IssueInvalidProxy, row, "proxy", decoded))
			} else {
				record.Proxy = proxy
			}
		}
		if source.ID != "" {
			recordIndexByID[source.ID] = len(result.Records)
		}
		var chain []string
		if source.HostChain != nil {
			chain = source.HostChain.HostIDs
		}
		pendingChains = append(pendingChains, chain)
		result.Records = append(result.Records, record)
	}
	for index, chain := range pendingChains {
		for _, target := range chain {
			if ref, found := recordIndexByID[target]; found && ref != index {
				result.Records[index].JumpRefs = appendUniqueInt(
					result.Records[index].JumpRefs, ref)
			}
		}
	}
	removeJumpCycles(&result)
	if len(result.Records) == 0 && !hasError(result.Issues) {
		result.Issues = append(result.Issues, issue(IssueNothingParsed, 0, "", ""))
	}
	return result
}
