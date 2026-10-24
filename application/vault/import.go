package vault

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

// ImportStore saves a chunk of imported Hosts and Host Groups in one
// transaction. The Vault service writes credentials to the field-secret store
// before calling it and rolls them back when the batch fails, so a database
// failure never leaves a Host advertising a password that was not written or
// a half-imported chunk.
type ImportStore interface {
	SaveImportBatch(ctx context.Context, batch ImportBatch) error
}

// ImportBatch is one atomic import chunk: Host Groups first, then Hosts.
type ImportBatch struct {
	Groups []domainvault.Group
	Hosts  []domainvault.Host
}

// ImportFileReader reads a referenced key file. The app implementation expands
// "~" and resolves the path; the service treats the path as opaque.
type ImportFileReader interface {
	ReadImportFile(path string) ([]byte, error)
}

// WithImportStore gives the Service transactional batch persistence for
// imported Hosts and Host Groups.
func WithImportStore(store ImportStore) Option {
	return func(s *Service) { s.importStore = store }
}

// WithApplyBarrier sets the mandatory work before the first chunk of a bulk
// import, such as a protective local backup. If it fails, no record is
// written. Later chunks of the same apply skip it.
func WithApplyBarrier(fn func(ctx context.Context) error) Option {
	return func(s *Service) { s.applyBarrier = fn }
}

// ApplyBarrierError reports that a bulk apply was aborted before writing
// because its protective action failed. The Vault is unchanged.
type ApplyBarrierError struct{ Cause error }

func (e *ApplyBarrierError) Error() string {
	return "vault: apply barrier failed: " + e.Cause.Error()
}

func (e *ApplyBarrierError) Unwrap() error { return e.Cause }

// WithImportFiles gives the Service the key-file reader used to import
// IdentityFile references from external inventories.
func WithImportFiles(reader ImportFileReader) Option {
	return func(s *Service) { s.importFiles = reader }
}

// ImportStatus classifies one parsed row in the preview.
type ImportStatus string

const (
	// ImportStatusNew is a Host the Vault does not have yet.
	ImportStatusNew ImportStatus = "new"
	// ImportStatusConflict is a Host whose merge key already exists.
	ImportStatusConflict ImportStatus = "conflict"
	// ImportStatusDuplicate repeats an earlier row in the same file.
	ImportStatusDuplicate ImportStatus = "duplicate"
	// ImportStatusInvalid failed validation and cannot be imported.
	ImportStatusInvalid ImportStatus = "invalid"
)

// ImportMerge chooses what happens to a conflicting row. There is no implicit
// overwrite: the user must select it.
type ImportMerge string

const (
	// ImportMergeSkip leaves the existing Host untouched.
	ImportMergeSkip ImportMerge = "skip"
	// ImportMergeOverwrite replaces the existing Host's editable fields.
	ImportMergeOverwrite ImportMerge = "overwrite"
)

// ImportRow is the non-secret preview of one parsed record. Password, key
// material, and key passphrase stay in the plan, never in this struct.
type ImportRow struct {
	Index         int
	SourceRow     int
	Label         string
	Hostname      string
	Username      string
	Port          int
	Protocol      string
	Group         string
	Status        ImportStatus
	Selected      bool
	Existing      model.HostID
	ExistingLabel string
	KeyLabel      string
	HasProxy      bool
	Issues        []vaultimport.Issue
}

// ImportCounts summarizes a preview.
type ImportCounts struct {
	Total      int
	New        int
	Conflicts  int
	Duplicates int
	Invalid    int
	Selected   int
}

// ImportFamilyRow is one non-Host record in the preview. Kind is a stable code
// (snippet, note, key, identity, proxy, known_host, package, note_group,
// group) the UI localizes; Label and Detail are data.
type ImportFamilyRow struct {
	Kind   string
	Label  string
	Detail string
}

// ImportFamilyCounts summarizes the non-Host records a document carries.
type ImportFamilyCounts struct {
	Groups          int
	SnippetPackages int
	Snippets        int
	NoteGroups      int
	Notes           int
	Keys            int
	Identities      int
	ProxyProfiles   int
	KnownHosts      int
}

// Total returns how many family records the preview will write.
func (c ImportFamilyCounts) Total() int {
	return c.Groups + c.SnippetPackages + c.Snippets + c.NoteGroups + c.Notes +
		c.Keys + c.Identities + c.ProxyProfiles + c.KnownHosts
}

// ImportPreview is a parsed, validated, conflict-analyzed inventory. Nothing
// is written until CommitImport is called. The plan's credential material is
// in memory only and is never rendered by the UI.
type ImportPreview struct {
	Format         vaultimport.Format
	FileName       string
	Rows           []ImportRow
	Issues         []vaultimport.Issue
	Counts         ImportCounts
	GroupsToCreate []string
	// FamilyRows and FamilyCounts describe the non-Host records a Netcatty
	// document carries. Families are additive and are committed with the first
	// chunk.
	FamilyRows   []ImportFamilyRow
	FamilyCounts ImportFamilyCounts

	records  []vaultimport.Record
	drafts   []domainvault.Draft
	keyFiles map[string]*importKeyFile
	// keyIDs caches Keys created for an IdentityFile path or an inline JSON
	// Key, so a chunked import creates one Key per source rather than one per
	// chunk.
	keyIDs map[string]model.KeyID
	// newIDs are the stable identities of rows the Vault does not have yet.
	// They are assigned during preview so a ProxyJump chain can reference a
	// Host that a later chunk imports, and a retried chunk keeps the same ID.
	newIDs []model.HostID
	// Family plan state, allocated during preview. The maps are reference
	// values, so mappings created in one commit chunk survive into the next
	// call even though ImportPreview is passed by value.
	family          *vaultimport.Family
	hostIDs         map[string]model.HostID
	identityIDs     map[string]model.IdentityID
	proxyProfileIDs map[string]string
	snippetIDs      map[string]string
	noteIDs         map[string]string
	// familyDone marks the family stages a commit finished, so a chunked
	// commit writes each family record once. It is a reference value for the
	// same reason the id maps are.
	familyDone map[string]bool
}

const (
	familyStageKeys    = "keys"
	familyStageLinks   = "links"
	familyStageRecords = "records"
)

type importKeyFile struct {
	label      string
	privateKey string
	passphrase string
}

// PreviewImportOptions configure parsing and detection.
type PreviewImportOptions struct {
	Format         vaultimport.Format
	FileName       string
	MasterPassword string
}

// PreviewImport parses text, validates every row, and reports conflicts
// against the current Vault. It never writes.
func (s *Service) PreviewImport(ctx context.Context, text string, opts PreviewImportOptions) (ImportPreview, error) {
	format := opts.Format
	if format == "" {
		format = vaultimport.DetectNamed(text, opts.FileName)
	}
	if !format.Valid() {
		return ImportPreview{}, invalid(fmt.Errorf("vault: unsupported import format %q", format))
	}

	parsed := vaultimport.Parse(format, text, vaultimport.Options{
		FileName: opts.FileName, MasterPassword: opts.MasterPassword,
	})
	preview := ImportPreview{
		Format:          parsed.Format,
		FileName:        opts.FileName,
		Issues:          append([]vaultimport.Issue(nil), parsed.Issues...),
		records:         parsed.Records,
		drafts:          make([]domainvault.Draft, len(parsed.Records)),
		keyFiles:        make(map[string]*importKeyFile),
		keyIDs:          make(map[string]model.KeyID),
		newIDs:          make([]model.HostID, len(parsed.Records)),
		family:          parsed.Family,
		hostIDs:         make(map[string]model.HostID),
		identityIDs:     make(map[string]model.IdentityID),
		proxyProfileIDs: make(map[string]string),
		snippetIDs:      make(map[string]string),
		noteIDs:         make(map[string]string),
		familyDone:      make(map[string]bool),
	}

	hosts, err := s.repo.List(ctx)
	if err != nil {
		return ImportPreview{}, internal(err)
	}
	existing := make(map[string]domainvault.Host, len(hosts))
	for _, host := range hosts {
		existing[host.MergeKey()] = host
	}
	groups, err := s.importExistingGroups(ctx)
	if err != nil {
		return ImportPreview{}, err
	}
	existingGroups := make(map[string]bool, len(groups))
	for _, group := range groups {
		existingGroups[group.Path] = true
	}

	seen := make(map[string]int, len(parsed.Records))
	missingGroups := make(map[string]bool)
	keyCache := make(map[string]*importKeyFile)

	for index, record := range parsed.Records {
		row := ImportRow{
			Index:     index,
			SourceRow: record.Row,
			Label:     record.Label,
			Hostname:  record.Hostname,
			Username:  record.Username,
			Port:      record.Port,
			Protocol:  record.Protocol,
			Group:     record.Group,
			HasProxy:  record.Proxy != nil,
			Selected:  true,
		}
		draft := draftFromRecord(record)
		if record.X11Forwarding && !record.X11WarningAcknowledged {
			// Imported configuration cannot accept a security warning on the
			// user's behalf, so forwarding is not enabled silently.
			preview.Issues = append(preview.Issues, vaultimport.Issue{
				Severity: vaultimport.SeverityWarning,
				Code:     vaultimport.IssueX11NotImported,
				Row:      record.Row,
			})
			row.Issues = append(row.Issues, preview.Issues[len(preview.Issues)-1])
		}
		if record.KeyPath != "" || record.KeyMaterial != "" {
			key, keyIssue := s.importKeyFile(record, keyCache)
			if keyIssue != nil {
				preview.Issues = append(preview.Issues, *keyIssue)
				row.Issues = append(row.Issues, *keyIssue)
			}
			if key != nil {
				row.KeyLabel = key.label
				preview.keyFiles[recordKey(record)] = key
			}
		}

		normalized, err := domainvault.NormalizeDraft(draft)
		if err != nil {
			row.Status = ImportStatusInvalid
			row.Selected = false
			item := validationIssue(err, record.Row)
			preview.Issues = append(preview.Issues, item)
			row.Issues = append(row.Issues, item)
			preview.Rows = append(preview.Rows, row)
			preview.Counts.Total++
			preview.Counts.Invalid++
			continue
		}
		preview.drafts[index] = normalized

		key := hostFromDraft(normalized).MergeKey()
		if first, duplicate := seen[key]; duplicate {
			row.Status = ImportStatusDuplicate
			row.Selected = false
			row.Existing = preview.Rows[first].Existing
			item := vaultimport.Issue{
				Severity: vaultimport.SeverityWarning,
				Code:     vaultimport.IssueDuplicateInFile,
				Row:      record.Row,
				Value:    normalized.Hostname,
			}
			preview.Issues = appendUniqueImportIssue(preview.Issues, item)
			row.Issues = append(row.Issues, item)
			preview.Counts.Total++
			preview.Counts.Duplicates++
			preview.Rows = append(preview.Rows, row)
			continue
		}
		seen[key] = index

		if host, found := existing[key]; found {
			row.Status = ImportStatusConflict
			row.Existing = host.ID
			row.ExistingLabel = host.Label
			preview.Counts.Conflicts++
		} else {
			row.Status = ImportStatusNew
			preview.Counts.New++
			preview.newIDs[index] = s.newID()
		}
		preview.Counts.Total++
		preview.Counts.Selected++
		for _, ancestor := range groupAncestorsWithSelf(normalized.Group) {
			if !existingGroups[ancestor] {
				missingGroups[ancestor] = true
			}
		}
		preview.Rows = append(preview.Rows, row)
	}

	// The preview rows must align with parsed records for commit indexing.
	// Invalid and duplicate rows are already appended in order above.
	if len(preview.Rows) != len(parsed.Records) {
		preview.Rows = alignImportRows(preview.Rows, len(parsed.Records))
	}

	// A parser may reject a whole entry without producing a Record, for
	// example an invalid CSV hostname or an unsupported RDP session. Those
	// rows still belong in the summary: the issue report names their source
	// row, and the counts must add up to every data row in the file.
	recordRows := make(map[int]bool, len(parsed.Records))
	for _, record := range parsed.Records {
		recordRows[record.Row] = true
	}
	droppedRows := make(map[int]bool)
	for _, item := range parsed.Issues {
		if item.Row > 0 && !recordRows[item.Row] && rejectedImportRow(item.Code) {
			droppedRows[item.Row] = true
		}
	}
	preview.Counts.Total += len(droppedRows)
	preview.Counts.Invalid += len(droppedRows)

	preview.collectFamilyPlan(missingGroups)

	preview.GroupsToCreate = sortedGroupPaths(missingGroups)
	return preview, nil
}

// alignImportRows keeps row indexes dense if a parser ever returns a record
// the loop could not describe. It is a defensive invariant, not a repair path
// for user input.
func alignImportRows(rows []ImportRow, total int) []ImportRow {
	aligned := make([]ImportRow, total)
	copy(aligned, rows)
	return aligned
}

func (s *Service) importExistingGroups(ctx context.Context) ([]domainvault.Group, error) {
	if s.groups == nil {
		return nil, nil
	}
	groups, err := s.groups.ListGroups(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return groups, nil
}

// importKeyFile reads and inspects a referenced IdentityFile once per path.
// A key that cannot be read is a warning: the Host itself is still valid and
// the user can attach a Key later.
func (s *Service) importKeyFile(record vaultimport.Record,
	cache map[string]*importKeyFile) (*importKeyFile, *vaultimport.Issue) {

	cacheKey := recordKey(record)
	if cached, ok := cache[cacheKey]; ok {
		return cached, nil
	}
	issue := func(code vaultimport.IssueCode) *vaultimport.Issue {
		return &vaultimport.Issue{
			Severity: vaultimport.SeverityWarning, Code: code, Row: record.Row,
			Field: "keypath", Value: record.KeyPath,
		}
	}
	if s.inspector == nil || s.keys == nil || s.secrets == nil {
		return nil, issue(vaultimport.IssueKeyUnreadable)
	}
	data := record.KeyMaterial
	if data == "" {
		if s.importFiles == nil {
			return nil, issue(vaultimport.IssueKeyUnreadable)
		}
		read, err := s.importFiles.ReadImportFile(record.KeyPath)
		if err != nil || len(read) == 0 {
			return nil, issue(vaultimport.IssueKeyUnreadable)
		}
		data = string(read)
	}
	inspection, err := s.inspector.Inspect(data, record.Passphrase, "")
	if err != nil {
		return nil, issue(vaultimport.IssueKeyUnreadable)
	}
	if inspection.Encrypted && record.Passphrase == "" {
		return nil, issue(vaultimport.IssuePassphraseConflict)
	}
	label := strings.TrimSpace(record.KeyLabelHint)
	if label == "" {
		label = keyFileLabel(record.KeyPath)
	}
	key := &importKeyFile{
		label:      label,
		privateKey: data,
		passphrase: record.Passphrase,
	}
	cache[cacheKey] = key
	return key, nil
}

// recordKey identifies the Key one record references. An inline JSON Key is
// shared by its exported identity; a CSV Key by its file path.
func recordKey(record vaultimport.Record) string {
	if record.KeyRef != "" {
		return "id:" + record.KeyRef
	}
	return record.KeyPath
}

func keyFileLabel(keyPath string) string {
	cleaned := strings.TrimSpace(keyPath)
	base := path.Base(strings.ReplaceAll(cleaned, `\`, "/"))
	base = strings.TrimSuffix(base, path.Ext(base))
	if base == "" || base == "." || base == "/" {
		// The Key label is user data, not display copy: fall back to the
		// referenced path when it has no usable base name.
		return cleaned
	}
	return base
}

// ImportCommitOptions select rows and the conflict policy. Offset and Limit
// make the commit chunked and resumable: Next is the row offset to pass as
// Offset on the next call.
type ImportCommitOptions struct {
	Selected []bool
	Merge    ImportMerge
	Offset   int
	Limit    int
}

// ImportCommitResult reports one chunk. Done is false when Limit ended the
// chunk before the last row; the caller resumes at Next.
type ImportCommitResult struct {
	Next          int
	Done          bool
	Imported      []model.HostID
	Overwritten   []model.HostID
	Skipped       int
	GroupsCreated []string
	// FamilyCreated and FamilySkipped count the non-Host records a Netcatty
	// document restored. A family is written once, in the chunk that starts at
	// offset zero.
	FamilyCreated int
	FamilySkipped int
}

// CommitImport writes one chunk of the preview. Rows that are invalid,
// duplicates, unselected, or a skipped conflict are counted and never written.
// New Hosts and needed Host Groups are persisted in one transaction; imported
// credentials always pass through the field-secret store.
func (s *Service) CommitImport(ctx context.Context, preview ImportPreview,
	opts ImportCommitOptions) (ImportCommitResult, error) {

	if (len(preview.records) == 0 && preview.family == nil) ||
		len(preview.records) != len(preview.Rows) {
		return ImportCommitResult{}, invalid(fmt.Errorf("vault: import plan is empty"))
	}
	merge := opts.Merge
	if merge == "" {
		merge = ImportMergeSkip
	}
	if merge != ImportMergeSkip && merge != ImportMergeOverwrite {
		return ImportCommitResult{}, invalid(fmt.Errorf("vault: unknown merge policy %q", merge))
	}
	selected := func(index int) bool {
		if len(opts.Selected) == 0 {
			return preview.Rows[index].Selected
		}
		if index < 0 || index >= len(opts.Selected) {
			return false
		}
		return opts.Selected[index]
	}

	start := opts.Offset
	if start < 0 {
		start = 0
	}
	if start > len(preview.Rows) {
		start = len(preview.Rows)
	}
	end := len(preview.Rows)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	// A bulk apply starts with its protective barrier. A resumed chunk is
	// already past it, so it must not take another backup or be blocked after
	// the first chunk committed.
	if start == 0 && s.applyBarrier != nil {
		if err := s.applyBarrier(ctx); err != nil {
			return ImportCommitResult{Next: start}, &ApplyBarrierError{Cause: err}
		}
	}

	result := ImportCommitResult{Next: start}

	// Keys are created before any Host so a Host never references material
	// that could not be stored. CreateKey encrypts the material; an
	// unreferenced Key left by a later failure is harmless.
	keyIDs, err := s.commitImportKeys(ctx, preview, selected, merge, start, end, &result)
	if err != nil {
		return result, err
	}
	// Identities and Proxy Profiles are linked from Hosts, so they exist before
	// the Host batch. The plan's maps make a retried chunk idempotent.
	if err := s.commitFamilyLinks(ctx, preview, &result); err != nil {
		return result, err
	}

	hosts, err := s.repo.List(ctx)
	if err != nil {
		return result, internal(err)
	}
	// ProxyJump references resolve against the preview's stable new IDs, so a
	// forwarding chain works no matter which chunk its jump Host is in.
	chains := preview.jumpChains(selected, hosts)

	for index := start; index < end; index++ {
		row := preview.Rows[index]
		switch row.Status {
		case ImportStatusInvalid, ImportStatusDuplicate:
			result.Skipped++
			continue
		}
		if !selected(index) {
			continue
		}
		switch row.Status {
		case ImportStatusConflict:
			if merge == ImportMergeSkip {
				result.Skipped++
				continue
			}
			draft, ok := preview.commitDraft(index)
			if !ok {
				result.Skipped++
				continue
			}
			draft.KeyID = remapKeyID(keyIDs, recordKey(preview.records[index]), draft.KeyID)
			draft.IdentityID = preview.remapIdentityID(draft.IdentityID)
			draft.ProxyProfileID = preview.remapProxyProfileID(draft.ProxyProfileID)
			draft.HostChain = chains[index]
			updated, err := s.Update(ctx, row.Existing, draft)
			if err != nil {
				result.Next = index
				return result, err
			}
			result.Overwritten = append(result.Overwritten, updated.ID)
		case ImportStatusNew:
			// Batched below.
		default:
			result.Skipped++
		}
	}

	existingGroups, err := s.importExistingGroups(ctx)
	if err != nil {
		return result, err
	}
	groupByPath := make(map[string]bool, len(existingGroups))
	for _, group := range existingGroups {
		groupByPath[group.Path] = true
	}

	now := s.now()
	order := nextOrder(hosts)
	type pendingHost struct {
		host  domainvault.Host
		draft domainvault.Draft
	}
	var pending []pendingHost
	// A family-only document (for example a Vault JSON whose Hosts are all
	// skipped) still restores its empty Host Groups. Host-draft groups keep
	// their chunk-local timing, and the existing-group check keeps a retried
	// chunk idempotent.
	missingGroups := make(map[string]bool)
	if preview.family != nil {
		for _, group := range preview.family.Groups {
			for _, ancestor := range groupAncestorsWithSelf(group) {
				if !groupByPath[ancestor] {
					missingGroups[ancestor] = true
				}
			}
		}
	}
	for index := start; index < end; index++ {
		if !selected(index) {
			continue
		}
		row := preview.Rows[index]
		if row.Status != ImportStatusNew && row.Status != ImportStatusConflict {
			continue
		}
		if row.Status == ImportStatusConflict && merge == ImportMergeSkip {
			continue
		}
		draft, ok := preview.commitDraft(index)
		if !ok {
			continue
		}
		draft.KeyID = remapKeyID(keyIDs, recordKey(preview.records[index]), draft.KeyID)
		draft.IdentityID = preview.remapIdentityID(draft.IdentityID)
		draft.ProxyProfileID = preview.remapProxyProfileID(draft.ProxyProfileID)
		draft.HostChain = chains[index]
		for _, ancestor := range groupAncestorsWithSelf(draft.Group) {
			if !groupByPath[ancestor] {
				missingGroups[ancestor] = true
			}
		}
		if row.Status != ImportStatusNew {
			continue
		}
		if draft.Protocol == "" {
			draft.Protocol = domainvault.ProtocolSSH
		}
		host := hostFromDraft(draft)
		host.ID = preview.newIDs[index]
		if host.ID == "" {
			// Defensive: a plan without a stable ID is still importable, but
			// its jump references cannot be resolved reliably.
			host.ID = s.newID()
		}
		host.OrderKey = order
		host.CreatedAt = now
		host.UpdatedAt = now
		host.HasPassword = draft.Password != "" && s.SecretMode() != SecretModeSession
		if host.Proxy != nil {
			host.Proxy.HasPassword = draft.Proxy != nil &&
				draft.Proxy.Password != "" && s.SecretMode() != SecretModeSession
		}
		order += orderStep
		pending = append(pending, pendingHost{host: host, draft: draft})
	}

	batch := ImportBatch{}
	groupOrder := nextGroupOrder(existingGroups)
	for _, groupPath := range sortedGroupPaths(missingGroups) {
		batch.Groups = append(batch.Groups, domainvault.Group{
			Path: groupPath, OrderKey: groupOrder, CreatedAt: now, UpdatedAt: now,
		})
		groupOrder += orderStep
	}
	for _, item := range pending {
		batch.Hosts = append(batch.Hosts, item.host)
	}

	// Credentials are written first. Any failure from here through the batch
	// transaction removes exactly what this call wrote.
	var written []SecretRef
	rollback := func() {
		if s.secrets == nil {
			return
		}
		for _, ref := range written {
			_ = s.secrets.Delete(ctx, ref)
		}
	}
	for _, item := range pending {
		needsPassword := item.draft.Password != ""
		needsProxy := item.draft.Proxy != nil && item.draft.Proxy.Password != ""
		if (needsPassword || needsProxy) && s.secrets == nil {
			return result, unsupported(ErrSecretsUnavailable)
		}
	}
	for _, item := range pending {
		if item.draft.Password != "" {
			ref := HostSecretRef(item.host.ID, FieldPassword)
			if err := s.secrets.Set(ctx, ref, item.draft.Password); err != nil {
				rollback()
				return result, secretError(err)
			}
			written = append(written, ref)
		}
		if item.draft.Proxy != nil && item.draft.Proxy.Password != "" {
			ref := HostSecretRef(item.host.ID, FieldProxyPassword)
			if err := s.secrets.Set(ctx, ref, item.draft.Proxy.Password); err != nil {
				rollback()
				return result, secretError(err)
			}
			written = append(written, ref)
		}
	}

	if len(batch.Hosts) > 0 || len(batch.Groups) > 0 {
		if s.importStore == nil {
			rollback()
			return result, unsupported(ErrImportStoreUnavailable)
		}
		if err := s.importStore.SaveImportBatch(ctx, batch); err != nil {
			rollback()
			return result, internal(err)
		}
	}
	for _, host := range batch.Hosts {
		result.Imported = append(result.Imported, host.ID)
	}
	for _, group := range batch.Groups {
		result.GroupsCreated = append(result.GroupsCreated, group.Path)
	}
	if err := s.commitFamilyRecords(ctx, preview, &result); err != nil {
		return result, err
	}
	result.Next = end
	result.Done = end >= len(preview.Rows)
	return result, nil
}

// ErrImportStoreUnavailable reports a Service built without batch import
// persistence. Import never falls back to non-transactional writes.
var ErrImportStoreUnavailable = errors.New("vault: import storage is unavailable")

func (s *Service) commitImportKeys(ctx context.Context, preview ImportPreview,
	selected func(int) bool, merge ImportMerge, start, end int,
	result *ImportCommitResult) (map[string]model.KeyID, error) {

	ids := make(map[string]model.KeyID)
	// Family Keys are restored even when no Host references them, so an
	// Identity's Key link survives a round trip.
	if err := s.commitFamilyKeys(ctx, preview, ids, result); err != nil {
		return nil, err
	}
	if len(preview.keyFiles) == 0 {
		return ids, nil
	}
	used := make(map[string]string)
	for index := start; index < end; index++ {
		if !selected(index) {
			continue
		}
		status := preview.Rows[index].Status
		if status == ImportStatusConflict && merge == ImportMergeSkip {
			continue
		}
		if status != ImportStatusNew && status != ImportStatusConflict {
			continue
		}
		record := preview.records[index]
		if record.KeyPath != "" || record.KeyMaterial != "" {
			used[recordKey(record)] = record.KeyPath
		}
	}
	for cacheKey, keyPath := range used {
		if id, found := preview.keyIDs[cacheKey]; found {
			ids[cacheKey] = id
			continue
		}
		keyFile, ok := preview.keyFiles[cacheKey]
		if !ok {
			continue
		}
		key, err := s.CreateKey(ctx, domainvault.KeyDraft{
			Label:          keyFile.label,
			PrivateKey:     keyFile.privateKey,
			Passphrase:     keyFile.passphrase,
			SavePassphrase: keyFile.passphrase != "",
			Source:         domainvault.KeySourceImported,
			FilePath:       keyPath,
		})
		if err != nil {
			return nil, err
		}
		preview.keyIDs[cacheKey] = key.ID
		ids[cacheKey] = key.ID
	}
	return ids, nil
}

// commitDraft returns the normalized draft for one row. Credential plaintext
// stays in this plan and is never exposed through ImportRow.
func (preview *ImportPreview) commitDraft(index int) (domainvault.Draft, bool) {
	if index < 0 || index >= len(preview.drafts) {
		return domainvault.Draft{}, false
	}
	return preview.drafts[index], true
}

func draftFromRecord(record vaultimport.Record) domainvault.Draft {
	draft := domainvault.Draft{
		Label:            record.Label,
		Hostname:         record.Hostname,
		Port:             record.Port,
		Username:         record.Username,
		Protocol:         domainvault.Protocol(record.Protocol),
		Group:            record.Group,
		Tags:             append([]string(nil), record.Tags...),
		Notes:            record.Notes,
		Pinned:           record.Pinned,
		IdentityID:       model.IdentityID(record.IdentityID),
		KeyID:            model.KeyID(record.KeyID),
		AuthMethod:       domainvault.HostAuthMethod(record.AuthMethod),
		UseSSHAgent:      record.UseSSHAgent,
		RequiresMFA:      record.RequiresMFA,
		Password:         record.Password,
		ProxyProfileID:   record.ProxyProfileID,
		LegacyAlgorithms: record.LegacyAlgorithms,
		SkipEcdsaHostKey: record.SkipEcdsaHostKey,
	}
	if record.Protocol == "" {
		draft.Protocol = domainvault.ProtocolSSH
	}
	if record.X11Forwarding && record.X11WarningAcknowledged {
		// A Netcatty export records an acknowledgement the user already made.
		// External inventories never carry one, so they stay off with a
		// warning instead of enabling forwarding silently.
		draft.X11Forwarding = true
		draft.X11WarningAcknowledged = true
	}
	if record.Proxy != nil {
		draft.Proxy = &domainvault.ProxyConfig{
			Type:     domainvault.ProxyType(record.Proxy.Type),
			Host:     record.Proxy.Host,
			Port:     record.Proxy.Port,
			Command:  record.Proxy.Command,
			Username: record.Proxy.Username,
			Password: record.Proxy.Password,
		}
	}
	if record.Algorithms != nil {
		draft.Algorithms = &domainvault.AlgorithmOverrides{
			KeyExchanges: append([]string(nil), record.Algorithms.KeyExchanges...),
			Ciphers:      append([]string(nil), record.Algorithms.Ciphers...),
			MACs:         append([]string(nil), record.Algorithms.MACs...),
			HostKeys:     append([]string(nil), record.Algorithms.HostKeys...),
		}
	}
	return draft
}

// jumpChains resolves every parsed ProxyJump reference to a Host identity.
// Each row's new ID was assigned during preview, so a chain may name a Host in
// a later commit chunk. References to rows that will not be written, and a
// row's self-reference, are dropped rather than saved as a dangling chain.
func (preview *ImportPreview) jumpChains(selected func(int) bool,
	existing []domainvault.Host) map[int]*domainvault.HostChain {

	existingByKey := make(map[string]model.HostID, len(existing))
	for _, host := range existing {
		existingByKey[host.MergeKey()] = host.ID
	}
	newIndexByKey := make(map[string]int, len(preview.Rows))
	newIDByKey := make(map[string]model.HostID, len(preview.Rows))
	for index, row := range preview.Rows {
		if row.Status != ImportStatusNew || index >= len(preview.drafts) {
			continue
		}
		key := hostFromDraft(preview.drafts[index]).MergeKey()
		if _, found := newIndexByKey[key]; !found {
			newIndexByKey[key] = index
		}
		newIDByKey[key] = preview.newIDs[index]
	}

	exists := func(index int) bool {
		if index < 0 || index >= len(preview.Rows) {
			return false
		}
		row := preview.Rows[index]
		switch row.Status {
		case ImportStatusConflict:
			return row.Existing != ""
		case ImportStatusNew:
			return selected(index)
		case ImportStatusDuplicate:
			if row.Existing != "" {
				return true
			}
			if index >= len(preview.drafts) {
				return false
			}
			key := hostFromDraft(preview.drafts[index]).MergeKey()
			if _, found := existingByKey[key]; found {
				return true
			}
			target, found := newIndexByKey[key]
			return found && selected(target)
		default:
			return false
		}
	}

	resolve := func(index int) (model.HostID, bool) {
		if index < 0 || index >= len(preview.Rows) {
			return "", false
		}
		row := preview.Rows[index]
		switch row.Status {
		case ImportStatusNew:
			return preview.newIDs[index], preview.newIDs[index] != ""
		case ImportStatusConflict:
			return row.Existing, row.Existing != ""
		case ImportStatusDuplicate:
			if row.Existing != "" {
				return row.Existing, true
			}
			if index >= len(preview.drafts) {
				return "", false
			}
			id, found := newIDByKey[hostFromDraft(preview.drafts[index]).MergeKey()]
			return id, found && id != ""
		default:
			return "", false
		}
	}

	chains := make(map[int]*domainvault.HostChain)
	for index, record := range preview.records {
		if len(record.JumpRefs) == 0 || index >= len(preview.Rows) {
			continue
		}
		var ids []model.HostID
		seen := make(map[model.HostID]bool, len(record.JumpRefs))
		for _, ref := range record.JumpRefs {
			id, ok := resolve(ref)
			if !ok || !exists(ref) || seen[id] || id == preview.newIDs[index] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			chains[index] = &domainvault.HostChain{HostIDs: ids}
		}
	}
	return chains
}

func hostFromDraft(draft domainvault.Draft) domainvault.Host {
	return domainvault.Host{
		Label:                  draft.Label,
		Hostname:               draft.Hostname,
		Port:                   draft.Port,
		Username:               draft.Username,
		Protocol:               draft.Protocol,
		Group:                  draft.Group,
		Tags:                   append([]string(nil), draft.Tags...),
		Notes:                  draft.Notes,
		Pinned:                 draft.Pinned,
		IdentityID:             draft.IdentityID,
		KeyID:                  draft.KeyID,
		AuthMethod:             draft.AuthMethod,
		UseSSHAgent:            draft.UseSSHAgent,
		RequiresMFA:            draft.RequiresMFA,
		X11Forwarding:          draft.X11Forwarding,
		X11WarningAcknowledged: draft.X11WarningAcknowledged,
		HostChain:              domainvault.CloneHostChain(draft.HostChain),
		Proxy:                  domainvault.CloneProxy(draft.Proxy),
		ProxyProfileID:         draft.ProxyProfileID,
		LegacyAlgorithms:       draft.LegacyAlgorithms,
		SkipEcdsaHostKey:       draft.SkipEcdsaHostKey,
		Algorithms:             domainvault.CloneAlgorithms(draft.Algorithms),
		SFTPFileProtocol:       draft.SFTPFileProtocol,
		SFTPSudo:               draft.SFTPSudo,
	}
}

func validationIssue(err error, row int) vaultimport.Issue {
	var validation *domainvault.ValidationError
	if errors.As(err, &validation) {
		code := vaultimport.IssueInvalidField
		switch {
		case validation.Field == domainvault.FieldHostname && validation.Rule == domainvault.RuleRequired:
			code = vaultimport.IssueMissingHostname
		case validation.Field == domainvault.FieldProtocol:
			code = vaultimport.IssueUnsupportedProtocol
		case validation.Field == domainvault.FieldPort:
			code = vaultimport.IssueInvalidPort
		}
		return vaultimport.Issue{
			Severity: vaultimport.SeverityWarning, Code: code, Row: row,
			Field: string(validation.Field), Detail: string(validation.Rule),
		}
	}
	return vaultimport.Issue{
		Severity: vaultimport.SeverityWarning, Code: vaultimport.IssueInvalidField,
		Row: row, Detail: err.Error(),
	}
}

// groupAncestorsWithSelf returns "" for the Vault root, then every parent and
// the group itself for a nested path.
func groupAncestorsWithSelf(group string) []string {
	group = domainvault.NormalizeGroup(group)
	if group == "" {
		return nil
	}
	segments := strings.Split(group, "/")
	out := make([]string, 0, len(segments))
	for index := range segments {
		out = append(out, strings.Join(segments[:index+1], "/"))
	}
	return out
}

func sortedGroupPaths(groups map[string]bool) []string {
	paths := make([]string, 0, len(groups))
	for group := range groups {
		paths = append(paths, group)
	}
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return paths[i] < paths[j]
	})
	return paths
}

// rejectedImportRow reports parse issues that discard a whole entry instead
// of annotating a Record the preview still lists.
func rejectedImportRow(code vaultimport.IssueCode) bool {
	switch code {
	case vaultimport.IssueInvalidHostname, vaultimport.IssueUnsupportedProtocol,
		vaultimport.IssueMissingHostname, vaultimport.IssueUnsupportedEntry:
		return true
	default:
		return false
	}
}

// appendUniqueImportIssue avoids reporting the same parser-level issue twice
// when both the parser and the preview classification notice it.
func appendUniqueImportIssue(issues []vaultimport.Issue, item vaultimport.Issue) []vaultimport.Issue {
	for _, existing := range issues {
		if existing.Code == item.Code && existing.Row == item.Row &&
			existing.Field == item.Field {
			return issues
		}
	}
	return append(issues, item)
}
