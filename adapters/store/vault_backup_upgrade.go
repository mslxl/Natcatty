package store

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

// upgradeSnapshotV1 rewrites a backup written before Host Groups were keyed by
// ID. Every stored group path becomes the deterministic ID of a group record,
// every Host's group_path becomes its group_id, and a path no group record
// named gets one so no Host loses its membership on restore.
func upgradeSnapshotV1(snapshot domainbackup.Snapshot) (domainbackup.Snapshot, error) {
	tables := make(map[string]domainbackup.Table, len(snapshot.Tables))
	for _, table := range snapshot.Tables {
		tables[table.Name] = table
	}

	var order int64
	byPath := make(map[string]string)
	groups := make([]domainvault.Group, 0, len(tables[domainbackup.TableGroups].Rows))
	for _, row := range tables[domainbackup.TableGroups].Rows {
		path := domainvault.NormalizeGroup(snapshotText(row, "path"))
		if path == "" {
			continue
		}
		var group domainvault.Group
		if data := snapshotText(row, "data"); data != "" {
			if err := json.Unmarshal([]byte(data), &group); err != nil {
				return domainbackup.Snapshot{}, err
			}
		}
		applySnapshotGroupDefaults(&group, row)
		group.ID = domainvault.GroupIDFromPath(path)
		group.Path = path
		group.Name = domainvault.GroupName(path)
		group.ParentID = ""
		if group.OrderKey > order {
			order = group.OrderKey
		}
		groups = append(groups, group)
		byPath[path] = group.ID
	}
	for index := range groups {
		groups[index].ParentID = byPath[domainvault.ParentGroupPath(groups[index].Path)]
	}

	hosts := make([]domainvault.Host, 0, len(tables[domainbackup.TableHosts].Rows))
	for _, row := range tables[domainbackup.TableHosts].Rows {
		var host domainvault.Host
		if data := snapshotText(row, "data"); data != "" {
			if err := json.Unmarshal([]byte(data), &host); err != nil {
				return domainbackup.Snapshot{}, err
			}
		}
		host.ID = model.HostID(snapshotText(row, "id"))
		path := domainvault.NormalizeGroup(snapshotText(row, "group_path"))
		if path == "" {
			path = domainvault.NormalizeGroup(host.Group)
		}
		if path != "" && byPath[path] == "" {
			groups = append(groups, addPathRecords(byPath, path, &order, time.Time{})...)
		}
		host.GroupID = byPath[path]
		host.Group = ""
		hosts = append(hosts, host)
	}

	sort.Slice(groups, func(left, right int) bool {
		if groups[left].OrderKey != groups[right].OrderKey {
			return groups[left].OrderKey < groups[right].OrderKey
		}
		return groups[left].ID < groups[right].ID
	})

	groupRows := make([]domainbackup.Row, 0, len(groups))
	for _, group := range groups {
		stored := group
		stored.Path = ""
		data, err := json.Marshal(stored)
		if err != nil {
			return domainbackup.Snapshot{}, err
		}
		groupRows = append(groupRows, domainbackup.Row{
			"id":                  domainbackup.Text(group.ID),
			"parent_id":           domainbackup.Text(group.ParentID),
			"name":                domainbackup.Text(group.Name),
			"notes":               domainbackup.Text(group.Notes),
			"username":            domainbackup.Text(group.Username),
			"port":                domainbackup.Int(int64(group.Port)),
			"protocol":            domainbackup.Text(string(group.Protocol)),
			"auth_method":         domainbackup.Text(string(group.AuthMethod)),
			"identity_id":         domainbackup.Text(string(group.IdentityID)),
			"key_id":              domainbackup.Text(string(group.KeyID)),
			"use_ssh_agent":       domainbackup.Int(int64(boolInt(group.UseSSHAgent))),
			"requires_mfa":        domainbackup.Int(int64(boolInt(group.RequiresMFA))),
			"legacy_algorithms":   domainbackup.Int(int64(boolInt(group.LegacyAlgorithms))),
			"skip_ecdsa_host_key": domainbackup.Int(int64(boolInt(group.SkipEcdsaHostKey))),
			"proxy_profile_id":    domainbackup.Text(group.ProxyProfileID),
			"order_key":           domainbackup.Int(group.OrderKey),
			"created_at":          domainbackup.Int(group.CreatedAt.UnixMilli()),
			"updated_at":          domainbackup.Int(group.UpdatedAt.UnixMilli()),
			"data":                domainbackup.Text(string(data)),
		})
	}
	hostRows := make([]domainbackup.Row, 0, len(hosts))
	for _, host := range hosts {
		stored := host
		stored.Group = ""
		data, err := json.Marshal(stored)
		if err != nil {
			return domainbackup.Snapshot{}, err
		}
		hostRows = append(hostRows, domainbackup.Row{
			"id":         domainbackup.Text(string(host.ID)),
			"label":      domainbackup.Text(host.Label),
			"hostname":   domainbackup.Text(host.Hostname),
			"port":       domainbackup.Int(int64(host.Port)),
			"username":   domainbackup.Text(host.Username),
			"protocol":   domainbackup.Text(string(host.Protocol)),
			"group_id":   domainbackup.Text(host.GroupID),
			"pinned":     domainbackup.Int(int64(boolInt(host.Pinned))),
			"order_key":  domainbackup.Int(host.OrderKey),
			"created_at": domainbackup.Int(host.CreatedAt.UnixMilli()),
			"updated_at": domainbackup.Int(host.UpdatedAt.UnixMilli()),
			"data":       domainbackup.Text(string(data)),
		})
	}

	upgraded := domainbackup.Snapshot{
		SchemaVersion: 2,
		Tables:        make([]domainbackup.Table, 0, len(snapshot.Tables)),
	}
	for _, table := range snapshot.Tables {
		switch table.Name {
		case domainbackup.TableGroups:
			upgraded.Tables = append(upgraded.Tables, domainbackup.Table{Name: table.Name, Rows: groupRows})
		case domainbackup.TableHosts:
			upgraded.Tables = append(upgraded.Tables, domainbackup.Table{Name: table.Name, Rows: hostRows})
		default:
			upgraded.Tables = append(upgraded.Tables, table)
		}
	}
	return upgraded, nil
}

// applySnapshotGroupDefaults backfills fields a v1 row projected but whose JSON
// payload predates them, so the migrated record keeps every value.
func applySnapshotGroupDefaults(group *domainvault.Group, row domainbackup.Row) {
	if group.Notes == "" {
		group.Notes = snapshotText(row, "notes")
	}
	if group.Username == "" {
		group.Username = snapshotText(row, "username")
	}
	if group.Port == 0 {
		group.Port = int(snapshotInt(row, "port"))
	}
	if group.Protocol == "" {
		group.Protocol = domainvault.Protocol(snapshotText(row, "protocol"))
	}
	if group.AuthMethod == "" {
		group.AuthMethod = domainvault.HostAuthMethod(snapshotText(row, "auth_method"))
	}
	if group.IdentityID == "" {
		group.IdentityID = model.IdentityID(snapshotText(row, "identity_id"))
	}
	if group.KeyID == "" {
		group.KeyID = model.KeyID(snapshotText(row, "key_id"))
	}
	if !group.UseSSHAgent {
		group.UseSSHAgent = snapshotInt(row, "use_ssh_agent") != 0
	}
	if !group.RequiresMFA {
		group.RequiresMFA = snapshotInt(row, "requires_mfa") != 0
	}
	if !group.LegacyAlgorithms {
		group.LegacyAlgorithms = snapshotInt(row, "legacy_algorithms") != 0
	}
	if !group.SkipEcdsaHostKey {
		group.SkipEcdsaHostKey = snapshotInt(row, "skip_ecdsa_host_key") != 0
	}
	if group.ProxyProfileID == "" {
		group.ProxyProfileID = snapshotText(row, "proxy_profile_id")
	}
	if group.OrderKey == 0 {
		group.OrderKey = snapshotInt(row, "order_key")
	}
	if group.CreatedAt.IsZero() {
		group.CreatedAt = time.UnixMilli(snapshotInt(row, "created_at")).UTC()
	}
	if group.UpdatedAt.IsZero() {
		group.UpdatedAt = time.UnixMilli(snapshotInt(row, "updated_at")).UTC()
	}
}

func snapshotText(row domainbackup.Row, column string) string {
	value, ok := row[column]
	if !ok || value.Kind != domainbackup.KindText {
		return ""
	}
	return value.Text
}

func snapshotInt(row domainbackup.Row, column string) int64 {
	value, ok := row[column]
	if !ok || value.Kind != domainbackup.KindInt {
		return 0
	}
	return value.Int
}
