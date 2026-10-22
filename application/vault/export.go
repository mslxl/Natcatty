package vault

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mslxl/natcatty/application/vault/export"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// ExportFormat is one portable output shape.
type ExportFormat = vaultexport.Format

// Export formats re-exported for callers that should not import the domain
// package just to name a format.
const (
	ExportJSON          = vaultexport.FormatJSON
	ExportCSV           = vaultexport.FormatCSV
	ExportSnippets      = vaultexport.FormatSnippets
	ExportNotesMarkdown = vaultexport.FormatNotesMarkdown
	ExportNotesHTML     = vaultexport.FormatNotesHTML
	ExportNotesZIP      = vaultexport.FormatNotesZIP
	ExportKeys          = vaultexport.FormatKeys
	ExportThemes        = vaultexport.FormatThemes
	ExportLogsJSON      = vaultexport.FormatLogsJSON
	ExportLogsCSV       = vaultexport.FormatLogsCSV
)

// ExportFormats returns every format in menu order.
func ExportFormats() []ExportFormat { return vaultexport.Formats() }

// HistoryReader is the connection-log and command-history port an export
// consumes. application/history.Service implements it.
type HistoryReader interface {
	ConnectionLogs(ctx context.Context) ([]domainhistory.ConnectionLog, error)
	Entries(ctx context.Context) ([]domainhistory.Entry, error)
}

// WithHistoryReader gives the Service the log and history source used by the
// log export formats.
func WithHistoryReader(reader HistoryReader) Option {
	return func(s *Service) { s.history = reader }
}

// WithThemeSource gives the Service the active appearance theme name the theme
// export writes. Without it the built-in default is written.
func WithThemeSource(source func() string) Option {
	return func(s *Service) { s.themeSource = source }
}

// ExportOptions select one export.
type ExportOptions struct {
	Format ExportFormat
	// Title is the document title a Notes HTML export writes.
	Title string
	// IncludeSecrets writes passwords, key passphrases, and private key
	// material in plaintext. The UI must confirm it before setting it.
	IncludeSecrets bool
	// HostIDs restricts a Host-bearing format to these records. Empty means
	// every Host.
	HostIDs []model.HostID
	// ExportedAt stamps the document. A zero value uses the current time.
	ExportedAt time.Time
}

// Export is a generated document. Nothing here has touched the filesystem.
type Export struct {
	Format   ExportFormat
	Filename string
	Content  []byte
	// UnreadableSecrets counts credentials that could not be decrypted for a
	// secret-bearing export. The document is still produced without them.
	UnreadableSecrets int
}

// ExportVault renders the requested data in opts.Format. The result is
// deterministic for the same Vault and options, except for the exportedAt
// stamp callers can pin.
func (s *Service) ExportVault(ctx context.Context, opts ExportOptions) (Export, error) {
	if !opts.Format.Valid() {
		return Export{}, invalid(fmt.Errorf("vault: unknown export format %q", opts.Format))
	}
	dataset, unreadable, err := s.exportDataset(ctx, opts)
	if err != nil {
		return Export{}, err
	}
	content, err := vaultexport.Export(dataset, opts.Format, vaultexport.Options{
		Title:          opts.Title,
		IncludeSecrets: opts.IncludeSecrets,
		ExportedAt:     opts.ExportedAt,
	})
	if err != nil {
		return Export{}, internal(err)
	}
	return Export{
		Format:            opts.Format,
		Filename:          exportFilename(opts.Format),
		Content:           content,
		UnreadableSecrets: unreadable,
	}, nil
}

func (s *Service) exportDataset(ctx context.Context, opts ExportOptions) (vaultexport.Dataset, int, error) {
	dataset := vaultexport.Dataset{ActiveTheme: s.activeTheme()}
	unreadable := 0

	hosts, err := s.repo.List(ctx)
	if err != nil {
		return dataset, 0, internal(err)
	}
	selected := make(map[model.HostID]bool, len(opts.HostIDs))
	for _, id := range opts.HostIDs {
		selected[id] = true
	}
	keys, err := s.exportKeys(ctx, opts.IncludeSecrets)
	if err != nil {
		return dataset, 0, err
	}
	dataset.Keys = keys
	keyExportByID := make(map[model.KeyID]vaultexport.Key, len(keys))
	for _, key := range keys {
		keyExportByID[key.Key.ID] = key
	}
	for _, host := range hosts {
		if len(selected) > 0 && !selected[host.ID] {
			continue
		}
		entry := vaultexport.Host{Host: host}
		if key, ok := keyExportByID[host.KeyID]; ok {
			entry.KeyPath = key.Key.FilePath
			entry.Passphrase = key.Passphrase
		}
		if opts.IncludeSecrets && s.secrets != nil {
			if host.HasPassword {
				value, readErr := s.Secret(ctx, host.ID, FieldPassword)
				if readErr != nil {
					unreadable++
				} else {
					entry.Password = value
				}
			}
			if host.Proxy != nil && host.Proxy.HasPassword {
				value, readErr := s.Secret(ctx, host.ID, FieldProxyPassword)
				if readErr != nil {
					unreadable++
				} else {
					entry.ProxyPassword = value
				}
			}
		}
		dataset.Hosts = append(dataset.Hosts, entry)
	}

	if s.groups != nil {
		groups, err := s.groups.ListGroups(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.Groups = groups
	}
	if s.identities != nil {
		identities, err := s.identities.ListIdentities(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.Identities = identities
	}
	if s.proxies != nil {
		proxies, err := s.proxies.ListProxyProfiles(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.ProxyProfiles = proxies
	}
	if s.knownHosts != nil {
		known, err := s.knownHosts.ListKnownHosts(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.KnownHosts = known
	}
	if s.snippets != nil {
		snippets, err := s.snippets.ListSnippets(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.Snippets = snippets
		packages, err := s.snippets.ListSnippetPackages(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.SnippetPackages = packages
	}
	if s.notes != nil {
		notes, err := s.notes.ListNotes(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.Notes = notes
		groups, err := s.notes.ListNoteGroups(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.NoteGroups = groups
	}
	if s.history != nil {
		logs, err := s.history.ConnectionLogs(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		entries, err := s.history.Entries(ctx)
		if err != nil {
			return dataset, 0, internal(err)
		}
		dataset.Logs = logs
		dataset.History = entries
	}
	return dataset, unreadable, nil
}

func (s *Service) exportKeys(ctx context.Context, includeSecrets bool) ([]vaultexport.Key, error) {
	if s.keys == nil {
		return nil, nil
	}
	keys, err := s.keys.ListKeys(ctx)
	if err != nil {
		return nil, internal(err)
	}
	out := make([]vaultexport.Key, 0, len(keys))
	for _, key := range keys {
		entry := vaultexport.Key{Key: key}
		if includeSecrets && s.secrets != nil {
			if privateKey, passphrase, materialErr := s.KeyMaterial(ctx, key.ID); materialErr == nil {
				entry.PrivateKey = privateKey
				entry.Passphrase = passphrase
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *Service) activeTheme() string {
	if s.themeSource == nil {
		return "dark"
	}
	return strings.TrimSpace(s.themeSource())
}

func exportFilename(format ExportFormat) string {
	date := time.Now().UTC().Format("2006-01-02")
	switch format {
	case ExportJSON:
		return "netcatty-vault-" + date + ".json"
	case ExportCSV:
		return "hosts_export_" + date + ".csv"
	case ExportSnippets:
		return "netcatty-snippets-" + date + ".json"
	case ExportNotesMarkdown:
		return "netcatty-notes-" + date + ".md"
	case ExportNotesHTML:
		return "netcatty-notes-" + date + ".html"
	case ExportNotesZIP:
		return "netcatty-notes-" + date + ".zip"
	case ExportKeys:
		return "netcatty-keys-" + date + ".json"
	case ExportThemes:
		return "netcatty-themes-" + date + ".json"
	case ExportLogsJSON:
		return "netcatty-logs-" + date + ".json"
	case ExportLogsCSV:
		return "netcatty-logs-" + date + ".csv"
	default:
		return "netcatty-export-" + date + ".json"
	}
}
