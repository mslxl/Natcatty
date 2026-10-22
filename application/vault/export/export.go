// Package vaultexport serializes Vault data into the portable formats the
// original application wrote. It has no application dependencies: callers
// supply records and credential plaintext, and it never reads the database,
// the filesystem, or the secret store.
//
// Every serializer is deterministic. Records are ordered by their stored order
// key and then identity, so exporting the same Vault twice produces identical
// bytes. CSV keeps the original header order and marked fields so the
// ticket-24 importer can read an export back.
package vaultexport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/markdown"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

// Format names one export format.
type Format string

const (
	// FormatJSON is the versioned Netcatty JSON document.
	FormatJSON Format = "netcatty_json"
	// FormatCSV is the Netcatty CSV Host inventory.
	FormatCSV Format = "csv"
	// FormatSnippets is the portable Snippets JSON document.
	FormatSnippets Format = "snippets"
	// FormatNotesMarkdown writes every Note as one Markdown document.
	FormatNotesMarkdown Format = "notes_markdown"
	// FormatNotesHTML writes every Note as one HTML document.
	FormatNotesHTML Format = "notes_html"
	// FormatNotesZIP writes one Markdown archive entry per Note.
	FormatNotesZIP Format = "notes_zip"
	// FormatKeys writes SSH Key metadata plus the public authorized_keys line,
	// and private material with IncludeSecrets.
	FormatKeys Format = "keys"
	// FormatThemes writes the appearance selection.
	FormatThemes Format = "themes"
	// FormatLogsJSON writes connection logs and command history as versioned
	// JSON.
	FormatLogsJSON Format = "logs_json"
	// FormatLogsCSV writes connection logs and command history as CSV.
	FormatLogsCSV Format = "logs_csv"
)

// Formats returns every format in menu order.
func Formats() []Format {
	return []Format{
		FormatJSON, FormatCSV, FormatSnippets,
		FormatNotesMarkdown, FormatNotesHTML, FormatNotesZIP,
		FormatKeys, FormatThemes, FormatLogsJSON, FormatLogsCSV,
	}
}

// Valid reports whether f is a supported export format.
func (f Format) Valid() bool {
	for _, candidate := range Formats() {
		if candidate == f {
			return true
		}
	}
	return false
}

// Extension returns the file extension a format writes, without the dot.
func (f Format) Extension() string {
	switch f {
	case FormatCSV, FormatLogsCSV:
		return "csv"
	case FormatSnippets, FormatKeys, FormatThemes, FormatLogsJSON:
		return "json"
	case FormatNotesMarkdown:
		return "md"
	case FormatNotesHTML:
		return "html"
	case FormatNotesZIP:
		return "zip"
	default:
		return "json"
	}
}

// Kind names are the versioned document markers. They match the original
// Snippets document and are new for the other families.
const (
	KindVault    = "netcatty.vault"
	KindSnippets = "netcatty.snippets"
	KindKeys     = "netcatty.keys"
	KindThemes   = "netcatty.themes"
	KindLogs     = "netcatty.logs"
	// Version is the document version every new export writes.
	Version = 1
)

// Host pairs a stored Host with the credential plaintext an export may need.
// The plaintext is empty when secrets are excluded or unreadable.
type Host struct {
	Host          domainvault.Host
	Password      string
	KeyPath       string
	Passphrase    string
	ProxyPassword string
}

// Key pairs an SSH Key with its decrypted material.
type Key struct {
	Key        domainvault.Key
	PrivateKey string
	Passphrase string
}

// Dataset is everything one export may serialize. A format uses the families
// it owns and ignores the rest.
type Dataset struct {
	Groups          []domainvault.Group
	Hosts           []Host
	Keys            []Key
	Identities      []domainvault.Identity
	ProxyProfiles   []domainvault.ProxyProfile
	KnownHosts      []domainconn.KnownHost
	Snippets        []domainvault.Snippet
	SnippetPackages []domainvault.SnippetPackage
	Notes           []domainvault.Note
	NoteGroups      []domainvault.NoteGroup
	Logs            []domainhistory.ConnectionLog
	History         []domainhistory.Entry
	// IdentityPasswords and ProxyProfilePasswords carry credential plaintext
	// for the profiles a JSON export restores. They are keyed by source id and
	// are populated only when secrets are included.
	IdentityPasswords     map[string]string
	ProxyProfilePasswords map[string]string
	// ActiveTheme is the appearance theme name the settings surface reports.
	ActiveTheme string
}

// Options configure one export.
type Options struct {
	// Title is the document title of a Notes HTML export. The application
	// passes the localized title; an empty value falls back to English.
	Title string
	// IncludeSecrets writes passwords, key passphrases, and private key
	// material in plaintext. Without it only non-secret metadata is written.
	IncludeSecrets bool
	// ExportedAt stamps the document. A zero value uses the current time, so
	// callers that compare bytes must set it.
	ExportedAt time.Time
}

func (o Options) exportedAt() string {
	when := o.ExportedAt
	if when.IsZero() {
		when = time.Now()
	}
	return when.UTC().Format(time.RFC3339)
}

// Export renders d in format.
func Export(d Dataset, format Format, opts Options) ([]byte, error) {
	switch format {
	case FormatJSON:
		return exportJSON(d, opts)
	case FormatCSV:
		return exportCSV(d, opts)
	case FormatSnippets:
		return exportSnippets(d, opts)
	case FormatNotesMarkdown:
		return exportNotesMarkdown(d), nil
	case FormatNotesHTML:
		return exportNotesHTML(d, opts), nil
	case FormatNotesZIP:
		return exportNotesZIP(d)
	case FormatKeys:
		return exportKeys(d, opts)
	case FormatThemes:
		return exportThemes(d, opts)
	case FormatLogsJSON:
		return exportLogsJSON(d, opts)
	case FormatLogsCSV:
		return exportLogsCSV(d), nil
	default:
		return nil, fmt.Errorf("vaultexport: unknown format %q", format)
	}
}

// ---------------------------------------------------------------- JSON

type jsonDocument struct {
	Kind            string              `json:"kind"`
	Version         int                 `json:"version"`
	ExportedAt      string              `json:"exportedAt"`
	Groups          []domainvault.Group `json:"groups"`
	Hosts           []jsonHost          `json:"hosts"`
	Keys            []jsonKey           `json:"keys"`
	Identities      []jsonIdentity      `json:"identities"`
	ProxyProfiles   []jsonProxyProfile  `json:"proxyProfiles"`
	KnownHosts      []jsonKnownHost     `json:"knownHosts"`
	Snippets        []jsonSnippet       `json:"snippets"`
	SnippetPackages []string            `json:"snippetPackages"`
	Notes           []domainvault.Note  `json:"notes"`
	NoteGroups      []string            `json:"noteGroups"`
}

// jsonIdentity adds the credential plaintext the domain Identity deliberately
// omits. It is written only with IncludeSecrets.
type jsonIdentity struct {
	domainvault.Identity
	Password string `json:"password,omitempty"`
}

// jsonProxyProfile adds a Proxy Profile's credential plaintext. The domain
// Config never serializes it.
type jsonProxyProfile struct {
	domainvault.ProxyProfile
	Password string `json:"password,omitempty"`
}

// jsonHost adds the fields the domain Host intentionally omits: credential
// plaintext and the marked CSV-shaped reference fields.
type jsonHost struct {
	domainvault.Host
	Password   string `json:"password,omitempty"`
	KeyPath    string `json:"keyPath,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Proxy      string `json:"proxy,omitempty"`
}

type jsonKey struct {
	domainvault.Key
	PrivateKey string `json:"privateKey,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

type jsonKnownHost struct {
	Hostname          string    `json:"hostname"`
	Port              int       `json:"port"`
	KeyType           string    `json:"keyType"`
	PublicKey         string    `json:"publicKey,omitempty"`
	Fingerprint       string    `json:"fingerprint"`
	DiscoveredAt      time.Time `json:"discoveredAt"`
	LastSeen          time.Time `json:"lastSeen"`
	ConvertedToHostID string    `json:"convertedToHostId,omitempty"`
}

type jsonSnippet struct {
	ID              string                              `json:"id,omitempty"`
	Label           string                              `json:"label"`
	Command         string                              `json:"command"`
	Tags            []string                            `json:"tags,omitempty"`
	Package         string                              `json:"package,omitempty"`
	Targets         []string                            `json:"targets,omitempty"`
	TargetGroups    []string                            `json:"targetGroups,omitempty"`
	TargetsAllHosts bool                                `json:"targetsAllHosts,omitempty"`
	NoAutoRun       bool                                `json:"noAutoRun,omitempty"`
	MultiLineMode   domainvault.SnippetMultiLineRunMode `json:"multiLineRunMode,omitempty"`
	VariableValues  map[string]string                   `json:"variableValues,omitempty"`
}

func exportJSON(d Dataset, opts Options) ([]byte, error) {
	document := jsonDocument{
		Kind:            KindVault,
		Version:         Version,
		ExportedAt:      opts.exportedAt(),
		Groups:          sortedGroups(d.Groups),
		SnippetPackages: sortedStrings(snippetPackagePaths(d)),
		Notes:           sortedNotes(d.Notes),
		NoteGroups:      sortedStrings(noteGroupPaths(d)),
	}
	for _, identity := range sortedIdentities(d.Identities) {
		entry := jsonIdentity{Identity: identity}
		if opts.IncludeSecrets {
			entry.Password = d.IdentityPasswords[string(identity.ID)]
		}
		document.Identities = append(document.Identities, entry)
	}
	for _, profile := range sortedProxies(d.ProxyProfiles) {
		entry := jsonProxyProfile{ProxyProfile: profile}
		if opts.IncludeSecrets {
			entry.Password = d.ProxyProfilePasswords[profile.ID]
		}
		document.ProxyProfiles = append(document.ProxyProfiles, entry)
	}
	keys := sortedKeys(d.Keys)
	keyByID := make(map[string]domainvault.Key, len(keys))
	document.Keys = make([]jsonKey, 0, len(keys))
	for _, key := range keys {
		keyByID[string(key.Key.ID)] = key.Key
		entry := jsonKey{Key: key.Key}
		if opts.IncludeSecrets {
			entry.PrivateKey = key.PrivateKey
			entry.Passphrase = key.Passphrase
		}
		document.Keys = append(document.Keys, entry)
	}
	document.KnownHosts = knownHostDocuments(d.KnownHosts)

	for _, host := range sortedHosts(d.Hosts) {
		entry := jsonHost{Host: host.Host}
		if opts.IncludeSecrets {
			entry.Password = host.Password
		}
		entry.KeyPath = vaultimport.EncodeMarkedField(host.KeyPath, vaultimport.MarkerKeyPath)
		if opts.IncludeSecrets {
			entry.Passphrase = vaultimport.EncodeMarkedField(host.Passphrase, vaultimport.MarkerPassphrase)
		}
		if proxy := host.Host.Proxy; proxy != nil {
			value := FormatProxy(*proxy, host.ProxyPassword, opts.IncludeSecrets)
			entry.Proxy = vaultimport.EncodeMarkedField(value, vaultimport.MarkerProxy)
		}
		document.Hosts = append(document.Hosts, entry)
	}
	document.Snippets = snippetDocuments(sortedSnippets(d.Snippets))

	return marshal(document)
}

func marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// ---------------------------------------------------------------- CSV

// Header is the canonical Netcatty CSV header, in the exact order the
// ticket-24 importer expects.
func Header() []string { return vaultimport.CSVHeader() }

// Column indexes of the Host CSV header. These cells carry raw passwords and
// already-marked references, so they skip the formula guard; every other cell
// is guarded.
const (
	csvPasswordColumn   = 8
	csvLocalShellColumn = 11
)

func exportCSV(d Dataset, opts Options) ([]byte, error) {
	var out strings.Builder
	out.WriteString(strings.Join(Header(), ","))
	out.WriteString("\r\n")
	for _, host := range sortedHosts(d.Hosts) {
		password, passphrase := "", ""
		if opts.IncludeSecrets {
			password, passphrase = host.Password, host.Passphrase
		}
		proxy := ""
		if host.Host.Proxy != nil {
			proxy = vaultimport.EncodeMarkedField(
				FormatProxy(*host.Host.Proxy, host.ProxyPassword, opts.IncludeSecrets),
				vaultimport.MarkerProxy)
		}
		row := []string{
			host.Host.Group,
			host.Host.Label,
			strings.Join(host.Host.Tags, ","),
			host.Host.Notes,
			formatHostname(host.Host.Hostname),
			string(host.Host.Protocol),
			portText(host.Host),
			host.Host.Username,
			password,
			vaultimport.EncodeMarkedField(host.KeyPath, vaultimport.MarkerKeyPath),
			vaultimport.EncodeMarkedField(passphrase, vaultimport.MarkerPassphrase),
			proxy,
			"",
		}
		for index, value := range row {
			if index > 0 {
				out.WriteByte(',')
			}
			// Passwords and marked reference fields stay byte-exact; every
			// other cell gets the spreadsheet formula guard.
			guard := index < csvPasswordColumn || index > csvLocalShellColumn
			out.WriteString(vaultimport.EscapeCSVField(value, guard))
		}
		out.WriteString("\r\n")
	}
	return []byte(out.String()), nil
}

// FormatProxy renders a proxy in the CSV proxy representation. The password
// is included only when includeSecrets is true.
func FormatProxy(proxy domainvault.ProxyConfig, password string, includeSecrets bool) string {
	if proxy.Type == domainvault.ProxyCommand {
		return "command://" + proxy.Command
	}
	scheme := "http"
	if proxy.Type == domainvault.ProxySOCKS5 {
		scheme = "socks5"
	}
	username := strings.TrimSpace(proxy.Username)
	auth := ""
	if username != "" {
		auth = vaultimport.EscapeComponent(username)
		if includeSecrets && password != "" {
			auth += ":" + vaultimport.EscapeComponent(password)
		}
		auth += "@"
	}
	host := strings.TrimSpace(proxy.Host)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return scheme + "://" + auth + host + ":" + strconv.Itoa(proxy.Port)
}

// formatHostname brackets a bare IPv6 address so the port separator stays
// unambiguous.
func formatHostname(hostname string) string {
	if strings.Contains(hostname, ":") && !strings.HasPrefix(hostname, "[") {
		return "[" + hostname + "]"
	}
	return hostname
}

func portText(host domainvault.Host) string {
	port := host.Port
	if port == 0 {
		if host.Protocol == domainvault.ProtocolLocal {
			return "0"
		}
		port = 22
		if host.Protocol == domainvault.ProtocolTelnet {
			port = 23
		}
	}
	return strconv.Itoa(port)
}

// ---------------------------------------------------------------- Snippets

type snippetsDocument struct {
	Kind            string        `json:"kind"`
	Version         int           `json:"version"`
	ExportedAt      string        `json:"exportedAt"`
	SnippetPackages []string      `json:"snippetPackages"`
	Snippets        []jsonSnippet `json:"snippets"`
}

func exportSnippets(d Dataset, opts Options) ([]byte, error) {
	return marshal(snippetsDocument{
		Kind:            KindSnippets,
		Version:         Version,
		ExportedAt:      opts.exportedAt(),
		SnippetPackages: sortedStrings(snippetPackagePaths(d)),
		Snippets:        snippetDocuments(sortedSnippets(d.Snippets)),
	})
}

// snippetDocuments keeps explicit Host targets as references. They may be
// dangling in the destination Vault, but dropping them loses the Snippet's
// configured scope; group scope and the all-hosts scope survive as before.
func snippetDocuments(snippets []domainvault.Snippet) []jsonSnippet {
	out := make([]jsonSnippet, 0, len(snippets))
	for _, snippet := range snippets {
		entry := jsonSnippet{
			ID:             snippet.ID,
			Label:          snippet.Label,
			Command:        snippet.Command,
			Tags:           append([]string(nil), snippet.Tags...),
			Package:        snippet.Package,
			NoAutoRun:      snippet.NoAutoRun,
			MultiLineMode:  snippet.MultiLineRunMode,
			VariableValues: cloneStringMap(snippet.VariableValues),
		}
		if snippet.TargetsAllHosts {
			entry.TargetsAllHosts = true
		} else {
			entry.TargetGroups = append([]string(nil), snippet.TargetGroups...)
			for _, target := range snippet.Targets {
				entry.Targets = append(entry.Targets, string(target))
			}
		}
		out = append(out, entry)
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

func snippetPackagePaths(d Dataset) []string {
	paths := make([]string, 0, len(d.SnippetPackages))
	for _, pkg := range d.SnippetPackages {
		paths = append(paths, pkg.Path)
	}
	for _, snippet := range d.Snippets {
		path := snippet.Package
		for path != "" {
			paths = append(paths, path)
			if index := strings.LastIndexByte(path, '/'); index >= 0 {
				path = path[:index]
				continue
			}
			path = ""
		}
	}
	return paths
}

// ---------------------------------------------------------------- Notes

func exportNotesMarkdown(d Dataset) []byte {
	var out strings.Builder
	for index, note := range sortedNotes(d.Notes) {
		if index > 0 {
			out.WriteString("\n\n---\n\n")
		}
		if group := domainvault.NormalizeGroup(note.Group); group != "" {
			fmt.Fprintf(&out, "<!-- group: %s -->\n\n", group)
		}
		fmt.Fprintf(&out, "# %s\n\n", note.Title)
		out.WriteString(note.Content)
	}
	return []byte(out.String())
}

func exportNotesHTML(d Dataset, opts Options) []byte {
	notes := sortedNotes(d.Notes)
	var source strings.Builder
	for index, note := range notes {
		if index > 0 {
			source.WriteString("\n\n---\n\n")
		}
		fmt.Fprintf(&source, "# %s\n\n%s", note.Title, note.Content)
	}
	title := opts.Title
	if title == "" {
		title = "Vault Notes"
	}
	return []byte(markdown.HTMLPage(title, source.String()))
}

func exportNotesZIP(d Dataset) ([]byte, error) {
	return NotesArchive(d.Notes)
}

// NotesArchive writes one Markdown entry per Note plus a versioned manifest.
// The directory layout stays human-readable; the manifest records the exact
// Note Group, title, and identity, so duplicate titles and titles containing
// path separators round-trip without ambiguity. The manifest entry name is
// reserved before Note names are assigned so a Note cannot shadow it.
func NotesArchive(notes []domainvault.Note) ([]byte, error) {
	sorted := sortedNotes(notes)
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	used := map[string]int{strings.ToLower(vaultimport.NotesManifestName): 1}
	taken := map[string]bool{strings.ToLower(vaultimport.NotesManifestName): true}
	entries := make([]vaultimport.NotesManifestEntry, 0, len(sorted))
	for _, note := range sorted {
		base := noteArchiveName(note)
		key := strings.ToLower(base)
		used[key]++
		name := base
		if used[key] > 1 {
			name = addNameSuffix(base, used[key])
		}
		// A suffixed name can itself collide with a real title ("A" and
		// "A-2"), and duplicate entry names would make the manifest path
		// ambiguous. Keep counting until the path is actually free.
		for taken[strings.ToLower(name)] {
			used[key]++
			name = addNameSuffix(base, used[key])
		}
		taken[strings.ToLower(name)] = true
		if err := writeZipEntry(archive, name, note.Content); err != nil {
			_ = archive.Close()
			return nil, err
		}
		entries = append(entries, vaultimport.NotesManifestEntry{
			Path: name, Title: note.Title,
			Group: domainvault.NormalizeGroup(note.Group), ID: note.ID,
		})
	}
	manifest, err := marshal(vaultimport.NotesManifest{
		Kind: vaultimport.KindNotes, Version: Version, Entries: entries,
	})
	if err != nil {
		_ = archive.Close()
		return nil, err
	}
	if err := writeZipEntry(archive, vaultimport.NotesManifestName, string(manifest)); err != nil {
		_ = archive.Close()
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeZipEntry(archive *zip.Writer, name, content string) error {
	writer, err := archive.Create(name)
	if err != nil {
		return err
	}
	_, err = writer.Write([]byte(content))
	return err
}

func noteArchiveName(note domainvault.Note) string {
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(domainvault.NormalizeGroup(note.Group), "/") {
		if segment == "" {
			continue
		}
		segments = append(segments, SanitizeName(segment, "group"))
	}
	segments = append(segments, SanitizeName(note.Title, "note")+".md")
	return strings.Join(segments, "/")
}

func addNameSuffix(name string, count int) string {
	dot := strings.LastIndexByte(name, '.')
	suffix := "-" + strconv.Itoa(count)
	if dot < 0 {
		return name + suffix
	}
	return name[:dot] + suffix + name[dot:]
}

// ---------------------------------------------------------------- Keys

type keysDocument struct {
	Kind           string    `json:"kind"`
	Version        int       `json:"version"`
	ExportedAt     string    `json:"exportedAt"`
	AuthorizedKeys string    `json:"authorizedKeys"`
	Keys           []jsonKey `json:"keys"`
}

func exportKeys(d Dataset, opts Options) ([]byte, error) {
	keys := sortedKeys(d.Keys)
	document := keysDocument{
		Kind:       KindKeys,
		Version:    Version,
		ExportedAt: opts.exportedAt(),
		Keys:       make([]jsonKey, 0, len(keys)),
	}
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		entry := jsonKey{Key: key.Key}
		if opts.IncludeSecrets {
			entry.PrivateKey = key.PrivateKey
			entry.Passphrase = key.Passphrase
		}
		document.Keys = append(document.Keys, entry)
		if line := authorizedKeysLine(key.Key); line != "" {
			lines = append(lines, line)
		}
	}
	document.AuthorizedKeys = strings.Join(lines, "\n")
	if len(lines) > 0 {
		document.AuthorizedKeys += "\n"
	}
	return marshal(document)
}

// authorizedKeysLine renders one public key in authorized_keys form. The label
// is the comment, so the line is still attributable when pasted into a server.
func authorizedKeysLine(key domainvault.Key) string {
	publicKey := strings.TrimSpace(key.PublicKey)
	if publicKey == "" {
		return ""
	}
	label := strings.ReplaceAll(strings.TrimSpace(key.Label), "\n", " ")
	if label == "" {
		return publicKey
	}
	return publicKey + " " + label
}

// ---------------------------------------------------------------- Themes

type themesDocument struct {
	Kind        string   `json:"kind"`
	Version     int      `json:"version"`
	ExportedAt  string   `json:"exportedAt"`
	ActiveTheme string   `json:"activeTheme"`
	Themes      []string `json:"themes"`
}

func exportThemes(d Dataset, opts Options) ([]byte, error) {
	active := strings.TrimSpace(d.ActiveTheme)
	if active == "" {
		active = "dark"
	}
	return marshal(themesDocument{
		Kind:        KindThemes,
		Version:     Version,
		ExportedAt:  opts.exportedAt(),
		ActiveTheme: active,
		Themes:      []string{"light", "dark"},
	})
}

// ---------------------------------------------------------------- Logs

type logsDocument struct {
	Kind           string                        `json:"kind"`
	Version        int                           `json:"version"`
	ExportedAt     string                        `json:"exportedAt"`
	ConnectionLogs []domainhistory.ConnectionLog `json:"connectionLogs"`
	History        []domainhistory.Entry         `json:"history"`
}

func exportLogsJSON(d Dataset, opts Options) ([]byte, error) {
	return marshal(logsDocument{
		Kind:           KindLogs,
		Version:        Version,
		ExportedAt:     opts.exportedAt(),
		ConnectionLogs: sortedLogs(d.Logs),
		History:        sortedHistory(d.History),
	})
}

// LogsHeader is the connection-log CSV header.
var logsHeader = []string{
	"Kind", "Status", "Host", "Address", "User", "Session",
	"StartedAt", "EndedAt", "Command", "Error",
}

func exportLogsCSV(d Dataset) []byte {
	var out strings.Builder
	out.WriteString(strings.Join(logsHeader, ","))
	out.WriteString("\r\n")
	for _, log := range sortedLogs(d.Logs) {
		row := []string{
			"connection", string(log.Status), log.HostLabel, log.Hostname,
			log.Username, string(log.SessionID), log.StartedAt.UTC().Format(time.RFC3339),
			formatOptionalTime(log.EndedAt), log.Kind, log.Error,
		}
		writeCSVRow(&out, row)
	}
	for _, entry := range sortedHistory(d.History) {
		row := []string{
			"history", "", entry.HostLabel, "", "",
			string(entry.SessionID), entry.RecordedAt.UTC().Format(time.RFC3339),
			"", entry.Command, entry.Scope,
		}
		writeCSVRow(&out, row)
	}
	return []byte(out.String())
}

func writeCSVRow(out *strings.Builder, row []string) {
	for index, value := range row {
		if index > 0 {
			out.WriteByte(',')
		}
		out.WriteString(vaultimport.EscapeCSVField(value, true))
	}
	out.WriteString("\r\n")
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------- ordering

// sortedByOrder returns a copy ordered by the stored order key and then the
// identity tie-breaker, which keeps every export deterministic.
func sortedByOrder[T any](items []T, order func(T) int64, id func(T) string) []T {
	out := append([]T(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		return lessRecord(order(out[i]), id(out[i]), order(out[j]), id(out[j]))
	})
	return out
}

func sortedHosts(hosts []Host) []Host {
	return sortedByOrder(hosts, func(h Host) int64 { return h.Host.OrderKey },
		func(h Host) string { return string(h.Host.ID) })
}

func sortedKeys(keys []Key) []Key {
	return sortedByOrder(keys, func(k Key) int64 { return k.Key.OrderKey },
		func(k Key) string { return string(k.Key.ID) })
}

func sortedGroups(groups []domainvault.Group) []domainvault.Group {
	return sortedByOrder(groups, func(g domainvault.Group) int64 { return g.OrderKey },
		func(g domainvault.Group) string { return g.Path })
}

func sortedIdentities(items []domainvault.Identity) []domainvault.Identity {
	return sortedByOrder(items, func(v domainvault.Identity) int64 { return v.OrderKey },
		func(v domainvault.Identity) string { return string(v.ID) })
}

func sortedProxies(items []domainvault.ProxyProfile) []domainvault.ProxyProfile {
	return sortedByOrder(items, func(p domainvault.ProxyProfile) int64 { return p.OrderKey },
		func(p domainvault.ProxyProfile) string { return p.ID })
}

func sortedSnippets(items []domainvault.Snippet) []domainvault.Snippet {
	return sortedByOrder(items, func(s domainvault.Snippet) int64 { return s.OrderKey },
		func(s domainvault.Snippet) string { return s.ID })
}

func sortedNotes(items []domainvault.Note) []domainvault.Note {
	return sortedByOrder(items, func(n domainvault.Note) int64 { return n.OrderKey },
		func(n domainvault.Note) string { return n.ID })
}

func sortedLogs(items []domainhistory.ConnectionLog) []domainhistory.ConnectionLog {
	out := append([]domainhistory.ConnectionLog(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func sortedHistory(items []domainhistory.Entry) []domainhistory.Entry {
	out := append([]domainhistory.Entry(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].RecordedAt.Before(out[j].RecordedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func lessRecord(leftOrder int64, leftID string, rightOrder int64, rightID string) bool {
	if leftOrder != rightOrder {
		return leftOrder < rightOrder
	}
	return leftID < rightID
}

func sortedStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func noteGroupPaths(d Dataset) []string {
	paths := make([]string, 0, len(d.NoteGroups))
	for _, group := range d.NoteGroups {
		paths = append(paths, group.Path)
	}
	return paths
}

func knownHostDocuments(items []domainconn.KnownHost) []jsonKnownHost {
	out := make([]jsonKnownHost, 0, len(items))
	sorted := append([]domainconn.KnownHost(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Hostname != sorted[j].Hostname {
			return sorted[i].Hostname < sorted[j].Hostname
		}
		if sorted[i].Port != sorted[j].Port {
			return sorted[i].Port < sorted[j].Port
		}
		return sorted[i].KeyType < sorted[j].KeyType
	})
	for _, host := range sorted {
		out = append(out, jsonKnownHost{
			Hostname:          host.Hostname,
			Port:              host.Port,
			KeyType:           host.KeyType,
			PublicKey:         host.PublicKey,
			Fingerprint:       host.Fingerprint,
			DiscoveredAt:      host.DiscoveredAt,
			LastSeen:          host.LastSeen,
			ConvertedToHostID: host.ConvertedToHostID,
		})
	}
	return out
}
