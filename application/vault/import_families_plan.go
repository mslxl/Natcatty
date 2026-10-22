package vault

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

// Family row kinds the UI localizes. They are stable data, not display text.
const (
	FamilyKindGroup          = "group"
	FamilyKindSnippetPackage = "snippet_package"
	FamilyKindSnippet        = "snippet"
	FamilyKindNoteGroup      = "note_group"
	FamilyKindNote           = "note"
	FamilyKindKey            = "key"
	FamilyKindIdentity       = "identity"
	FamilyKindProxyProfile   = "proxy_profile"
	FamilyKindKnownHost      = "known_host"
)

// knownHostUpserter is the optional write half of the Known Host store. The
// read/revoke port stays small; an import needs to record a trust decision.
type knownHostUpserter interface {
	UpsertKnownHost(ctx context.Context, record domainconn.KnownHost) error
}

// collectFamilyPlan maps the parsed non-Host records onto preview rows and
// counts, resolves source Host references to the identities this import will
// create, and adds the document's empty Host Groups to the groups to create.
func (preview *ImportPreview) collectFamilyPlan(missingGroups map[string]bool) {
	if preview.family == nil {
		return
	}
	preview.buildHostIDMap()
	family := preview.family
	counts := ImportFamilyCounts{
		Groups:          len(family.Groups),
		SnippetPackages: len(family.SnippetPackages),
		Snippets:        len(family.Snippets),
		NoteGroups:      len(family.NoteGroups),
		Notes:           len(family.Notes),
		Keys:            len(family.Keys),
		Identities:      len(family.Identities),
		ProxyProfiles:   len(family.ProxyProfiles),
		KnownHosts:      len(family.KnownHosts),
	}
	rows := make([]ImportFamilyRow, 0, counts.Total())
	for _, group := range family.Groups {
		for _, ancestor := range groupAncestorsWithSelf(group) {
			missingGroups[ancestor] = true
		}
		rows = append(rows, ImportFamilyRow{Kind: FamilyKindGroup, Label: group})
	}
	for _, pkg := range family.SnippetPackages {
		rows = append(rows, ImportFamilyRow{Kind: FamilyKindSnippetPackage, Label: pkg})
	}
	for _, snippet := range family.Snippets {
		rows = append(rows, ImportFamilyRow{
			Kind: FamilyKindSnippet, Label: snippet.Label, Detail: snippet.Package,
		})
	}
	for _, group := range family.NoteGroups {
		rows = append(rows, ImportFamilyRow{Kind: FamilyKindNoteGroup, Label: group})
	}
	for _, note := range family.Notes {
		rows = append(rows, ImportFamilyRow{
			Kind: FamilyKindNote, Label: note.Title, Detail: note.Group,
		})
	}
	for _, key := range family.Keys {
		rows = append(rows, ImportFamilyRow{Kind: FamilyKindKey, Label: key.Label})
	}
	for _, identity := range family.Identities {
		rows = append(rows, ImportFamilyRow{
			Kind: FamilyKindIdentity, Label: identity.Label, Detail: identity.Username,
		})
	}
	for _, profile := range family.ProxyProfiles {
		rows = append(rows, ImportFamilyRow{
			Kind: FamilyKindProxyProfile, Label: profile.Label, Detail: profile.Config.Host,
		})
	}
	for _, known := range family.KnownHosts {
		rows = append(rows, ImportFamilyRow{
			Kind: FamilyKindKnownHost, Label: known.Hostname,
			Detail: known.KeyType,
		})
	}
	preview.FamilyCounts = counts
	preview.FamilyRows = rows
}

// buildHostIDMap records where each source Host lands. Snippet explicit
// targets and Host links then reference a real destination Host when the
// target is present, and stay dangling when it is not.
func (preview *ImportPreview) buildHostIDMap() {
	resolvedByKey := make(map[string]model.HostID, len(preview.records))
	for index, record := range preview.records {
		if index >= len(preview.Rows) {
			break
		}
		row := preview.Rows[index]
		var target model.HostID
		var mergeKey string
		if index < len(preview.drafts) {
			mergeKey = hostFromDraft(preview.drafts[index]).MergeKey()
		}
		switch row.Status {
		case ImportStatusNew:
			target = preview.newIDs[index]
		case ImportStatusConflict:
			target = row.Existing
		case ImportStatusDuplicate:
			if mergeKey != "" {
				target = resolvedByKey[mergeKey]
			}
		}
		if target == "" {
			continue
		}
		if mergeKey != "" {
			resolvedByKey[mergeKey] = target
		}
		if record.SourceID != "" {
			preview.hostIDs[record.SourceID] = target
		}
	}
}

// remapIdentityID returns the imported Identity for a source identity id. A
// reference to a target that was not part of the document stays as exported.
func (preview *ImportPreview) remapIdentityID(source model.IdentityID) model.IdentityID {
	if source == "" {
		return ""
	}
	if id, ok := preview.identityIDs[string(source)]; ok {
		return id
	}
	return source
}

// remapProxyProfileID returns the imported Proxy Profile for a source id.
func (preview *ImportPreview) remapProxyProfileID(source string) string {
	if source == "" {
		return ""
	}
	if id, ok := preview.proxyProfileIDs[source]; ok {
		return id
	}
	return source
}

func remapKeyID(ids map[string]model.KeyID, cacheKey string, fallback model.KeyID) model.KeyID {
	if cacheKey == "" {
		return fallback
	}
	if id, ok := ids[cacheKey]; ok && id != "" {
		return id
	}
	return fallback
}

// commitFamilyKeys restores standalone SSH Keys. Keys that only had metadata
// in the document (a reference with no exported material) are skipped: a Key
// with no material cannot authenticate, and the Host link stays dangling.
func (s *Service) commitFamilyKeys(ctx context.Context, preview ImportPreview,
	ids map[string]model.KeyID, result *ImportCommitResult) error {

	if preview.family == nil || preview.familyDone[familyStageKeys] {
		return nil
	}
	if s.keys == nil {
		result.FamilySkipped += len(preview.family.Keys)
		preview.familyDone[familyStageKeys] = true
		return nil
	}
	for _, key := range preview.family.Keys {
		cacheKey := ""
		if key.ID != "" {
			cacheKey = "id:" + key.ID
			if id, ok := preview.keyIDs[cacheKey]; ok {
				ids[cacheKey] = id
				continue
			}
			if s.keys != nil {
				existing, err := s.keys.GetKey(ctx, model.KeyID(key.ID))
				switch {
				case err == nil:
					preview.keyIDs[cacheKey] = existing.ID
					ids[cacheKey] = existing.ID
					continue
				case !errors.Is(err, ErrNotFound):
					return internal(err)
				}
			}
		}
		if strings.TrimSpace(key.PrivateKey) == "" {
			// A metadata-only Key has no material to restore. Count it so the
			// import result does not claim a complete restore.
			result.FamilySkipped++
			continue
		}
		created, err := s.CreateKey(ctx, domainvault.KeyDraft{
			Label:          key.Label,
			PrivateKey:     key.PrivateKey,
			Passphrase:     key.Passphrase,
			SavePassphrase: key.Passphrase != "",
			Certificate:    key.Certificate,
			PublicKey:      key.PublicKey,
			Type:           domainvault.KeyType(key.Type),
			Source:         domainvault.KeySource(key.Source),
		})
		if err != nil {
			return err
		}
		if cacheKey != "" {
			preview.keyIDs[cacheKey] = created.ID
			ids[cacheKey] = created.ID
		}
	}
	preview.familyDone[familyStageKeys] = true
	return nil
}

// commitFamilyLinks creates the Keys, Identities, and Proxy Profiles Hosts
// reference. Keys are created by commitImportKeys; this step maps the rest.
func (s *Service) commitFamilyLinks(ctx context.Context, preview ImportPreview,
	result *ImportCommitResult) error {
	if preview.family == nil || preview.familyDone[familyStageLinks] {
		return nil
	}
	for _, identity := range preview.family.Identities {
		if identity.ID != "" {
			if _, done := preview.identityIDs[identity.ID]; done {
				continue
			}
			if s.identities != nil {
				existing, err := s.identities.GetIdentity(ctx, model.IdentityID(identity.ID))
				switch {
				case err == nil:
					preview.identityIDs[identity.ID] = existing.ID
					continue
				case !errors.Is(err, ErrNotFound):
					return internal(err)
				}
			}
		}
		if s.identities == nil {
			result.FamilySkipped++
			continue
		}
		created, err := s.CreateIdentity(ctx, domainvault.IdentityDraft{
			Label:      identity.Label,
			Username:   identity.Username,
			AuthMethod: domainvault.IdentityAuthMethod(identity.AuthMethod),
			KeyID:      remapKeyID(preview.keyIDs, "id:"+identity.KeyID, model.KeyID(identity.KeyID)),
			Password:   identity.Password,
		})
		if err != nil {
			return err
		}
		if identity.ID != "" {
			preview.identityIDs[identity.ID] = created.ID
		}
	}
	for _, profile := range preview.family.ProxyProfiles {
		if profile.ID != "" {
			if _, done := preview.proxyProfileIDs[profile.ID]; done {
				continue
			}
			if s.proxies != nil {
				existing, err := s.proxies.GetProxyProfile(ctx, profile.ID)
				switch {
				case err == nil:
					preview.proxyProfileIDs[profile.ID] = existing.ID
					continue
				case !errors.Is(err, ErrNotFound):
					return internal(err)
				}
			}
		}
		if s.proxies == nil {
			result.FamilySkipped++
			continue
		}
		created, err := s.CreateProxyProfile(ctx, domainvault.ProxyProfileDraft{
			Label: profile.Label,
			Config: domainvault.ProxyConfig{
				Type:        domainvault.ProxyType(profile.Config.Type),
				Host:        profile.Config.Host,
				Port:        profile.Config.Port,
				Command:     profile.Config.Command,
				Username:    profile.Config.Username,
				Password:    profile.Password,
				HasPassword: profile.Config.HasPassword,
			},
		})
		if err != nil {
			return err
		}
		if profile.ID != "" {
			preview.proxyProfileIDs[profile.ID] = created.ID
		}
	}
	preview.familyDone[familyStageLinks] = true
	return nil
}

// commitFamilyRecords restores Snippets, Notes, and Known Hosts. It runs once,
// in the chunk that starts at offset zero; the plan's maps make a retry
// idempotent.
func (s *Service) commitFamilyRecords(ctx context.Context, preview ImportPreview,
	result *ImportCommitResult) error {

	if preview.family == nil || preview.familyDone[familyStageRecords] {
		return nil
	}
	family := preview.family
	if err := s.commitFamilySnippetPackages(ctx, family, result); err != nil {
		return err
	}
	if err := s.commitFamilySnippets(ctx, preview, family, result); err != nil {
		return err
	}
	if err := s.commitFamilyNotes(ctx, preview, family, result); err != nil {
		return err
	}
	if err := s.commitFamilyKnownHosts(ctx, preview, family, result); err != nil {
		return err
	}
	preview.familyDone[familyStageRecords] = true
	return nil
}

func (s *Service) commitFamilySnippetPackages(ctx context.Context, family *vaultimport.Family,
	result *ImportCommitResult) error {

	if len(family.SnippetPackages) == 0 {
		return nil
	}
	if s.snippets == nil {
		result.FamilySkipped += len(family.SnippetPackages)
		return nil
	}
	paths := append([]string(nil), family.SnippetPackages...)
	sort.Strings(paths)
	for _, path := range paths {
		if _, err := s.CreateSnippetPackage(ctx,
			domainvault.SnippetPackageDraft{Path: path}); err != nil {
			if typed, ok := err.(*Error); ok && typed.Kind == KindConflict {
				continue
			}
			return err
		}
		result.FamilyCreated++
	}
	return nil
}

func (s *Service) commitFamilySnippets(ctx context.Context, preview ImportPreview,
	family *vaultimport.Family, result *ImportCommitResult) error {

	if len(family.Snippets) == 0 {
		return nil
	}
	if s.snippets == nil {
		result.FamilySkipped += len(family.Snippets)
		return nil
	}
	existing, err := s.snippets.ListSnippets(ctx)
	if err != nil {
		return internal(err)
	}
	order := nextSnippetOrder(existing)
	for _, item := range family.Snippets {
		if item.ID != "" {
			if _, done := preview.snippetIDs[item.ID]; done {
				continue
			}
			if found, err := s.snippets.GetSnippet(ctx, item.ID); err == nil {
				preview.snippetIDs[item.ID] = found.ID
				result.FamilySkipped++
				continue
			} else if !errors.Is(err, ErrNotFound) {
				return internal(err)
			}
		}
		normalized, err := domainvault.NormalizeSnippetDraft(domainvault.SnippetDraft{
			Label:            item.Label,
			Command:          item.Command,
			Package:          item.Package,
			Tags:             item.Tags,
			Targets:          preview.remapSnippetTargets(item.Targets),
			TargetGroups:     item.TargetGroups,
			TargetsAllHosts:  item.TargetsAllHosts,
			NoAutoRun:        item.NoAutoRun,
			MultiLineRunMode: domainvault.SnippetMultiLineRunMode(item.MultiLineMode),
		})
		if err != nil {
			result.FamilySkipped++
			continue
		}
		now := s.now()
		snippet := domainvault.Snippet{
			ID:               s.newSnippetID(),
			Label:            normalized.Label,
			Command:          normalized.Command,
			Package:          normalized.Package,
			Tags:             normalized.Tags,
			Targets:          normalized.Targets,
			TargetGroups:     normalized.TargetGroups,
			TargetsAllHosts:  normalized.TargetsAllHosts,
			NoAutoRun:        normalized.NoAutoRun,
			MultiLineRunMode: normalized.MultiLineRunMode,
			VariableValues:   cloneVariableValues(item.VariableValues),
			OrderKey:         order,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := s.snippets.SaveSnippet(ctx, snippet); err != nil {
			return internal(err)
		}
		order += orderStep
		if item.ID != "" {
			preview.snippetIDs[item.ID] = snippet.ID
		}
		result.FamilyCreated++
	}
	return nil
}

// remapSnippetTargets maps exported Host ids onto the Hosts this import
// creates. A target that was not part of the document stays as a dangling
// reference rather than disappearing.
func (preview *ImportPreview) remapSnippetTargets(targets []string) []model.HostID {
	out := make([]model.HostID, 0, len(targets))
	for _, target := range targets {
		if mapped, ok := preview.hostIDs[target]; ok {
			out = append(out, mapped)
			continue
		}
		out = append(out, model.HostID(target))
	}
	return out
}

func (s *Service) commitFamilyNotes(ctx context.Context, preview ImportPreview,
	family *vaultimport.Family, result *ImportCommitResult) error {

	if len(family.NoteGroups) > 0 {
		if s.notes == nil {
			result.FamilySkipped += len(family.NoteGroups)
		} else {
			groups := append([]string(nil), family.NoteGroups...)
			sort.Strings(groups)
			for _, group := range groups {
				if _, err := s.CreateNoteGroup(ctx,
					domainvault.NoteGroupDraft{Path: group}); err != nil {
					if typed, ok := err.(*Error); ok && typed.Kind == KindConflict {
						continue
					}
					return err
				}
				result.FamilyCreated++
			}
		}
	}
	if len(family.Notes) == 0 {
		return nil
	}
	if s.notes == nil {
		result.FamilySkipped += len(family.Notes)
		return nil
	}
	existing, err := s.notes.ListNotes(ctx)
	if err != nil {
		return internal(err)
	}
	order := nextNoteOrder(existing)
	for _, item := range family.Notes {
		if item.ID != "" {
			if _, done := preview.noteIDs[item.ID]; done {
				continue
			}
			if found, err := s.notes.GetNote(ctx, item.ID); err == nil {
				preview.noteIDs[item.ID] = found.ID
				result.FamilySkipped++
				continue
			} else if !errors.Is(err, ErrNotFound) {
				return internal(err)
			}
		}
		normalized, err := domainvault.NormalizeNoteDraft(domainvault.NoteDraft{
			Title: item.Title, Content: item.Content, Group: item.Group,
		})
		if err != nil {
			result.FamilySkipped++
			continue
		}
		now := s.now()
		note := domainvault.Note{
			ID:        s.newNoteID(),
			Title:     normalized.Title,
			Content:   normalized.Content,
			Group:     normalized.Group,
			OrderKey:  order,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.notes.SaveNote(ctx, note); err != nil {
			return internal(err)
		}
		order += orderStep
		if item.ID != "" {
			preview.noteIDs[item.ID] = note.ID
		}
		result.FamilyCreated++
	}
	return nil
}

func (s *Service) commitFamilyKnownHosts(ctx context.Context, preview ImportPreview,
	family *vaultimport.Family, result *ImportCommitResult) error {

	if len(family.KnownHosts) == 0 {
		return nil
	}
	saver, ok := s.knownHosts.(knownHostUpserter)
	if !ok {
		result.FamilySkipped += len(family.KnownHosts)
		return nil
	}
	for _, known := range family.KnownHosts {
		if strings.TrimSpace(known.Hostname) == "" {
			result.FamilySkipped++
			continue
		}
		port := known.Port
		if port == 0 {
			port = 22
		}
		convertedTo := known.ConvertedToHostID
		if id, ok := preview.hostIDs[convertedTo]; ok {
			convertedTo = string(id)
		}
		record := domainconn.KnownHost{
			ID:                newULID(),
			Hostname:          known.Hostname,
			Port:              port,
			KeyType:           known.KeyType,
			PublicKey:         known.PublicKey,
			Fingerprint:       known.Fingerprint,
			DiscoveredAt:      known.DiscoveredAt,
			LastSeen:          known.LastSeen,
			ConvertedToHostID: convertedTo,
		}
		if err := saver.UpsertKnownHost(ctx, record); err != nil {
			return fmt.Errorf("vault: restore Known Host: %w", err)
		}
		result.FamilyCreated++
	}
	return nil
}
