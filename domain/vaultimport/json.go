package vaultimport

import (
	"encoding/json"
	"strings"
	"time"
)

// jsonDocument is the versioned Netcatty JSON envelope. The exporter writes
// KindNetcattyVault and every format version 1; imports accept the same.
type jsonDocument struct {
	Kind            string             `json:"kind"`
	Version         int                `json:"version"`
	Groups          []jsonGroup        `json:"groups"`
	Hosts           []jsonHost         `json:"hosts"`
	Keys            []jsonKey          `json:"keys"`
	Identities      []jsonIdentity     `json:"identities"`
	ProxyProfiles   []jsonProxyProfile `json:"proxyProfiles"`
	KnownHosts      []jsonKnownHost    `json:"knownHosts"`
	Snippets        []jsonSnippet      `json:"snippets"`
	SnippetPackages []string           `json:"snippetPackages"`
	Notes           []jsonNote         `json:"notes"`
	NoteGroups      []string           `json:"noteGroups"`
}

type jsonGroup struct {
	Path string `json:"path"`
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
	Pinned   bool     `json:"pinned"`
	// KeyPath, Passphrase, and Proxy are marked fields, exactly as in CSV.
	KeyPath    string `json:"keyPath"`
	Passphrase string `json:"passphrase"`
	Proxy      string `json:"proxy"`
	Password   string `json:"password"`
	KeyID      string `json:"keyId"`
	// IdentityID, AuthMethod, UseSSHAgent, RequiresMFA, and the advanced
	// options round-trip the Host's authentication and negotiation policy.
	IdentityID             string          `json:"identityId"`
	AuthMethod             string          `json:"authMethod"`
	UseSSHAgent            bool            `json:"useSshAgent"`
	RequiresMFA            bool            `json:"requiresMfa"`
	X11Forwarding          bool            `json:"x11Forwarding"`
	X11WarningAcknowledged bool            `json:"x11WarningAcknowledged"`
	ProxyProfileID         string          `json:"proxyProfileId"`
	LegacyAlgorithms       bool            `json:"legacyAlgorithms"`
	SkipEcdsaHostKey       bool            `json:"skipEcdsaHostKey"`
	Algorithms             *jsonAlgorithms `json:"algorithms"`
	DeviceType             string          `json:"deviceType"`
	HostChain              *struct {
		HostIDs []string `json:"hostIds"`
	} `json:"hostChain"`
}

type jsonAlgorithms struct {
	KeyExchanges []string `json:"kex"`
	Ciphers      []string `json:"cipher"`
	MACs         []string `json:"hmac"`
	HostKeys     []string `json:"serverHostKey"`
}

type jsonKey struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	PublicKey   string `json:"publicKey"`
	Certificate string `json:"certificate"`
	Source      string `json:"source"`
	FilePath    string `json:"filePath"`
	PrivateKey  string `json:"privateKey"`
	Passphrase  string `json:"passphrase"`
}

type jsonIdentity struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Username    string `json:"username"`
	AuthMethod  string `json:"authMethod"`
	KeyID       string `json:"keyId"`
	HasPassword bool   `json:"hasPassword"`
	Password    string `json:"password"`
}

type jsonProxyProfile struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Config   jsonProxy `json:"config"`
	Password string    `json:"password"`
}

type jsonProxy struct {
	Type        string `json:"type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Command     string `json:"command"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
}

type jsonKnownHost struct {
	Hostname          string    `json:"hostname"`
	Port              int       `json:"port"`
	KeyType           string    `json:"keyType"`
	PublicKey         string    `json:"publicKey"`
	Fingerprint       string    `json:"fingerprint"`
	DiscoveredAt      time.Time `json:"discoveredAt"`
	LastSeen          time.Time `json:"lastSeen"`
	ConvertedToHostID string    `json:"convertedToHostId"`
}

type jsonSnippet struct {
	ID              string            `json:"id"`
	Label           string            `json:"label"`
	Command         string            `json:"command"`
	Tags            []string          `json:"tags"`
	Package         string            `json:"package"`
	Targets         []string          `json:"targets"`
	TargetGroups    []string          `json:"targetGroups"`
	TargetsAllHosts bool              `json:"targetsAllHosts"`
	NoAutoRun       bool              `json:"noAutoRun"`
	MultiLineMode   string            `json:"multiLineRunMode"`
	VariableValues  map[string]string `json:"variableValues"`
}

type jsonNote struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Group   string `json:"group"`
}

// parseJSON reads a Netcatty JSON document. Hosts without a usable hostname
// are reported by row and skipped; an unknown protocol never becomes SSH. The
// non-Host families are parsed in full so a round trip restores them too.
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

	result.Family = familyFromJSON(document)

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
			Row:                    row,
			Source:                 "json",
			SourceID:               source.ID,
			Label:                  strings.TrimSpace(source.Label),
			Hostname:               hostname,
			Username:               strings.TrimSpace(source.Username),
			Port:                   source.Port,
			Protocol:               protocol,
			Group:                  normalizeGroup(source.Group),
			Tags:                   append([]string(nil), source.Tags...),
			Notes:                  source.Notes,
			Password:               source.Password,
			Pinned:                 source.Pinned,
			IdentityID:             strings.TrimSpace(source.IdentityID),
			KeyID:                  strings.TrimSpace(source.KeyID),
			AuthMethod:             strings.TrimSpace(source.AuthMethod),
			UseSSHAgent:            source.UseSSHAgent,
			RequiresMFA:            source.RequiresMFA,
			X11Forwarding:          source.X11Forwarding,
			X11WarningAcknowledged: source.X11WarningAcknowledged,
			ProxyProfileID:         strings.TrimSpace(source.ProxyProfileID),
			LegacyAlgorithms:       source.LegacyAlgorithms,
			SkipEcdsaHostKey:       source.SkipEcdsaHostKey,
			DeviceType:             strings.TrimSpace(source.DeviceType),
		}
		if source.Algorithms != nil {
			record.Algorithms = &Algorithms{
				KeyExchanges: append([]string(nil), source.Algorithms.KeyExchanges...),
				Ciphers:      append([]string(nil), source.Algorithms.Ciphers...),
				MACs:         append([]string(nil), source.Algorithms.MACs...),
				HostKeys:     append([]string(nil), source.Algorithms.HostKeys...),
			}
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
		if key, found := keys[record.KeyID]; found {
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
	if len(result.Records) == 0 && !hasError(result.Issues) && !result.Family.HasData() {
		result.Issues = append(result.Issues, issue(IssueNothingParsed, 0, "", ""))
	}
	return result
}

func familyFromJSON(document jsonDocument) *Family {
	family := &Family{
		SnippetPackages: append([]string(nil), document.SnippetPackages...),
		NoteGroups:      append([]string(nil), document.NoteGroups...),
	}
	for _, group := range document.Groups {
		if path := normalizeGroup(group.Path); path != "" {
			family.Groups = append(family.Groups, path)
		}
	}
	for _, key := range document.Keys {
		family.Keys = append(family.Keys, FamilyKey{
			ID: key.ID, Label: strings.TrimSpace(key.Label), Type: key.Type,
			PublicKey: key.PublicKey, Certificate: key.Certificate,
			Source:     strings.ToLower(strings.TrimSpace(key.Source)),
			FilePath:   strings.TrimSpace(key.FilePath),
			PrivateKey: key.PrivateKey, Passphrase: key.Passphrase,
		})
	}
	for _, identity := range document.Identities {
		family.Identities = append(family.Identities, FamilyIdentity{
			ID: identity.ID, Label: strings.TrimSpace(identity.Label),
			Username:   strings.TrimSpace(identity.Username),
			AuthMethod: strings.ToLower(strings.TrimSpace(identity.AuthMethod)),
			KeyID:      identity.KeyID, HasPassword: identity.HasPassword,
			Password: identity.Password,
		})
	}
	for _, profile := range document.ProxyProfiles {
		family.ProxyProfiles = append(family.ProxyProfiles, FamilyProxyProfile{
			ID: profile.ID, Label: strings.TrimSpace(profile.Label),
			Config: Proxy{
				Type: strings.ToLower(strings.TrimSpace(profile.Config.Type)),
				Host: strings.TrimSpace(profile.Config.Host), Port: profile.Config.Port,
				Command: profile.Config.Command, Username: profile.Config.Username,
				HasPassword: profile.Config.HasPassword,
			},
			Password: profile.Password,
		})
	}
	for _, known := range document.KnownHosts {
		family.KnownHosts = append(family.KnownHosts, FamilyKnownHost{
			Hostname: known.Hostname, Port: known.Port, KeyType: known.KeyType,
			PublicKey: known.PublicKey, Fingerprint: known.Fingerprint,
			DiscoveredAt: known.DiscoveredAt, LastSeen: known.LastSeen,
			ConvertedToHostID: known.ConvertedToHostID,
		})
	}
	for _, snippet := range document.Snippets {
		family.Snippets = append(family.Snippets, FamilySnippet{
			ID: snippet.ID, Label: strings.TrimSpace(snippet.Label),
			Command: snippet.Command, Package: strings.TrimSpace(snippet.Package),
			Tags:            append([]string(nil), snippet.Tags...),
			Targets:         normalizeTargetIDs(snippet.Targets),
			TargetGroups:    append([]string(nil), snippet.TargetGroups...),
			TargetsAllHosts: snippet.TargetsAllHosts, NoAutoRun: snippet.NoAutoRun,
			MultiLineMode:  snippet.MultiLineMode,
			VariableValues: cloneStringMap(snippet.VariableValues),
		})
	}
	for _, note := range document.Notes {
		family.Notes = append(family.Notes, FamilyNote{
			ID: note.ID, Title: note.Title, Content: note.Content,
			Group: normalizeGroup(note.Group),
		})
	}
	return family
}

func normalizeTargetIDs(targets []string) []string {
	out := make([]string, 0, len(targets))
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, target)
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for name, value := range values {
		out[name] = value
	}
	return out
}
