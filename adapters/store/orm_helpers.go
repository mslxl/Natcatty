package store

import (
	"encoding/json"
	"fmt"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type dataSchemaRow struct {
	Data string `gorm:"column:data"`
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func loadDataRows(tx *gorm.DB, model any, order ...string) ([]string, error) {
	query := tx.Model(model).Select("data")
	for _, expression := range order {
		query = query.Order(expression)
	}
	var rows []dataSchemaRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	data := make([]string, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.Data)
	}
	return data, nil
}

func loadDataRow(tx *gorm.DB, model any, condition any) (string, bool, error) {
	var row dataSchemaRow
	err := tx.Model(model).Where(condition).First(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return row.Data, true, nil
}

func upsertModel(tx *gorm.DB, model any, conflict string, updates ...string) error {
	return upsertModelWithConflicts(tx, model, []string{conflict}, updates...)
}

func upsertModelWithConflicts(tx *gorm.DB, model any, conflicts []string, updates ...string) error {
	columns := make([]clause.Column, 0, len(conflicts))
	for _, conflict := range conflicts {
		columns = append(columns, clause.Column{Name: conflict})
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   columns,
		DoUpdates: clause.AssignmentColumns(updates),
	}).Create(model).Error
}

func deleteModel(tx *gorm.DB, model any, condition any) (int64, error) {
	result := tx.Where(condition).Delete(model)
	return result.RowsAffected, result.Error
}

func deleteAll(tx *gorm.DB, model any) error {
	return tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(model).Error
}

func marshalData(value any, label string) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("store: encode %s: %w", label, err)
	}
	return string(data), nil
}

func decodeData(data, label string, value any) error {
	if err := json.Unmarshal([]byte(data), value); err != nil {
		return fmt.Errorf("store: decode %s: %w", label, err)
	}
	return nil
}

func decodeDataList[T any](data []string, label string) ([]T, error) {
	values := make([]T, 0, len(data))
	for _, item := range data {
		var value T
		if err := decodeData(item, label, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func hostModel(host domainvault.Host) (hostSchema, error) {
	if err := host.Validate(); err != nil {
		return hostSchema{}, fmt.Errorf("store: save Host: %w", err)
	}
	data, err := marshalData(host, "Host")
	if err != nil {
		return hostSchema{}, err
	}
	return hostSchema{
		ID: string(host.ID), Label: host.Label, Hostname: host.Hostname,
		Port: int64(host.Port), Username: host.Username, Protocol: string(host.Protocol),
		GroupPath: host.Group, Pinned: int64(boolInt(host.Pinned)), OrderKey: host.OrderKey,
		CreatedAt: host.CreatedAt.UnixMilli(), UpdatedAt: host.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func groupModel(group domainvault.Group) (hostGroupSchema, error) {
	if group.Path == "" {
		return hostGroupSchema{}, fmt.Errorf("store: Host Group has no path")
	}
	data, err := marshalData(group, "Host Group")
	if err != nil {
		return hostGroupSchema{}, err
	}
	return hostGroupSchema{
		Path: group.Path, Notes: group.Notes, Username: group.Username, Port: int64(group.Port),
		Protocol: string(group.Protocol), AuthMethod: string(group.AuthMethod),
		IdentityID: string(group.IdentityID), KeyID: string(group.KeyID),
		UseSSHAgent: int64(boolInt(group.UseSSHAgent)), RequiresMFA: int64(boolInt(group.RequiresMFA)),
		LegacyAlgorithms: int64(boolInt(group.LegacyAlgorithms)),
		SkipECDSAHostKey: int64(boolInt(group.SkipEcdsaHostKey)),
		ProxyProfileID:   group.ProxyProfileID, OrderKey: group.OrderKey,
		CreatedAt: group.CreatedAt.UnixMilli(), UpdatedAt: group.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func keyModel(key domainvault.Key) (sshKeySchema, error) {
	if key.ID == "" || key.Label == "" {
		return sshKeySchema{}, fmt.Errorf("store: save SSH Key: missing identity")
	}
	data, err := marshalData(key, "SSH Key")
	if err != nil {
		return sshKeySchema{}, err
	}
	return sshKeySchema{
		ID: string(key.ID), Label: key.Label, Type: string(key.Type), Source: string(key.Source),
		OrderKey: key.OrderKey, CreatedAt: key.CreatedAt.UnixMilli(), UpdatedAt: key.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func identityModel(identity domainvault.Identity) (identitySchema, error) {
	if identity.ID == "" || identity.Label == "" {
		return identitySchema{}, fmt.Errorf("store: save Identity: missing identity")
	}
	data, err := marshalData(identity, "Identity")
	if err != nil {
		return identitySchema{}, err
	}
	return identitySchema{
		ID: string(identity.ID), Label: identity.Label, Username: identity.Username,
		AuthMethod: string(identity.AuthMethod), KeyID: string(identity.KeyID),
		OrderKey: identity.OrderKey, CreatedAt: identity.CreatedAt.UnixMilli(),
		UpdatedAt: identity.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func forwardingRuleModel(rule domainforward.Rule) (forwardingRuleSchema, error) {
	if rule.ID == "" {
		return forwardingRuleSchema{}, fmt.Errorf("store: save Port Forwarding Rule: empty id")
	}
	if err := rule.Validate(); err != nil {
		return forwardingRuleSchema{}, fmt.Errorf("store: save Port Forwarding Rule: %w", err)
	}
	data, err := marshalData(rule, "Port Forwarding Rule")
	if err != nil {
		return forwardingRuleSchema{}, err
	}
	return forwardingRuleSchema{
		ID: string(rule.ID), Label: rule.Label, Type: string(rule.Type), HostID: string(rule.HostID),
		BindAddress: rule.BindAddress, LocalPort: int64(rule.LocalPort), OrderKey: rule.OrderKey,
		CreatedAt: rule.CreatedAt.UnixMilli(), UpdatedAt: rule.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func proxyProfileModel(profile domainvault.ProxyProfile) (proxyProfileSchema, error) {
	if profile.ID == "" {
		return proxyProfileSchema{}, fmt.Errorf("store: Proxy Profile has no id")
	}
	data, err := marshalData(profile, "Proxy Profile")
	if err != nil {
		return proxyProfileSchema{}, err
	}
	return proxyProfileSchema{
		ID: profile.ID, Label: profile.Label, Type: string(profile.Config.Type), Host: profile.Config.Host,
		Port: int64(profile.Config.Port), OrderKey: profile.OrderKey,
		CreatedAt: profile.CreatedAt.UnixMilli(), UpdatedAt: profile.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func snippetModel(snippet domainvault.Snippet) (snippetSchema, error) {
	if snippet.ID == "" {
		return snippetSchema{}, fmt.Errorf("store: Snippet has no id")
	}
	data, err := marshalData(snippet, "Snippet")
	if err != nil {
		return snippetSchema{}, err
	}
	return snippetSchema{
		ID: snippet.ID, Label: snippet.Label, PackagePath: snippet.Package, OrderKey: snippet.OrderKey,
		CreatedAt: snippet.CreatedAt.UnixMilli(), UpdatedAt: snippet.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func snippetPackageModel(pkg domainvault.SnippetPackage) (snippetPackageSchema, error) {
	if pkg.Path == "" {
		return snippetPackageSchema{}, fmt.Errorf("store: Snippet package has no path")
	}
	data, err := marshalData(pkg, "Snippet package")
	if err != nil {
		return snippetPackageSchema{}, err
	}
	return snippetPackageSchema{
		Path: pkg.Path, OrderKey: pkg.OrderKey, CreatedAt: pkg.CreatedAt.UnixMilli(),
		UpdatedAt: pkg.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func noteModel(note domainvault.Note) (noteSchema, error) {
	if note.ID == "" {
		return noteSchema{}, fmt.Errorf("store: Vault Note has no id")
	}
	data, err := marshalData(note, "Vault Note")
	if err != nil {
		return noteSchema{}, err
	}
	return noteSchema{
		ID: note.ID, Title: note.Title, GroupPath: note.Group, OrderKey: note.OrderKey,
		CreatedAt: note.CreatedAt.UnixMilli(), UpdatedAt: note.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func noteGroupModel(group domainvault.NoteGroup) (noteGroupSchema, error) {
	if group.Path == "" {
		return noteGroupSchema{}, fmt.Errorf("store: Note Group has no path")
	}
	data, err := marshalData(group, "Note Group")
	if err != nil {
		return noteGroupSchema{}, err
	}
	return noteGroupSchema{
		Path: group.Path, OrderKey: group.OrderKey, CreatedAt: group.CreatedAt.UnixMilli(),
		UpdatedAt: group.UpdatedAt.UnixMilli(), Data: data,
	}, nil
}

func knownHostModel(record domainconn.KnownHost) (knownHostSchema, error) {
	data, err := marshalData(record, "Known Host")
	if err != nil {
		return knownHostSchema{}, err
	}
	return knownHostSchema{
		ID: record.ID, Hostname: record.Hostname, Port: int64(record.Port), KeyType: record.KeyType,
		Fingerprint: record.Fingerprint, PublicKey: record.PublicKey,
		DiscoveredAt: record.DiscoveredAt.UnixMilli(), LastSeen: record.LastSeen.UnixMilli(),
		ConvertedToHostID: record.ConvertedToHostID, OrderKey: record.Order, Data: data,
	}, nil
}

func connectionLogModel(log domainhistory.ConnectionLog) (connectionLogSchema, error) {
	if log.ID == "" {
		return connectionLogSchema{}, fmt.Errorf("store: connection log has no id")
	}
	data, err := marshalData(log, "connection log")
	if err != nil {
		return connectionLogSchema{}, err
	}
	return connectionLogSchema{
		ID: log.ID, SessionID: string(log.SessionID), HostID: string(log.HostID), HostLabel: log.HostLabel,
		Hostname: log.Hostname, Username: log.Username, Kind: log.Kind, Status: string(log.Status),
		Error: log.Error, StartedAt: log.StartedAt.UnixMilli(), EndedAt: log.EndedAt.UnixMilli(), Data: data,
	}, nil
}

func historyEntryModel(entry domainhistory.Entry) (historyEntrySchema, error) {
	if entry.ID == "" {
		return historyEntrySchema{}, fmt.Errorf("store: history entry has no id")
	}
	data, err := marshalData(entry, "history entry")
	if err != nil {
		return historyEntrySchema{}, err
	}
	return historyEntrySchema{
		ID: entry.ID, SessionID: string(entry.SessionID), HostID: string(entry.HostID),
		HostLabel: entry.HostLabel, Scope: entry.Scope, Command: entry.Command, Source: entry.Source,
		RecordedAt: entry.RecordedAt.UnixMilli(), Data: data,
	}, nil
}
