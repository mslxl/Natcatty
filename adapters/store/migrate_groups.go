package store

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// legacyGroupRow is the path-keyed Host Group row this build used before the
// group tree became ID-keyed. It exists only for the one-time upgrade.
type legacyGroupRow struct {
	Path     string `gorm:"column:path"`
	GroupID  string `gorm:"column:id"`
	ParentID string `gorm:"column:parent_id"`
	Name     string `gorm:"column:name"`
	Data     string `gorm:"column:data"`
}

// legacyHostRow is the Host row with its former group_path projection.
type legacyHostRow struct {
	ID        string `gorm:"column:id"`
	GroupPath string `gorm:"column:group_path"`
	Data      string `gorm:"column:data"`
}

// migrateGroupIdentity upgrades a database written before Host Groups were
// identified by ID. Hosts keep their membership: each stored path becomes the
// stable ID of the group at that path, and a path with no record becomes an
// explicit group so no Host silently falls back to the Vault root.
//
// The upgrade runs before AutoMigrate because the group table's primary key
// changes: GORM can add columns but cannot re-key an existing table.
func (s *Store) migrateGroupIdentity(ctx context.Context) error {
	return s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if err := migrateGroupsToIDs(tx); err != nil {
			return err
		}
		return migrateHostsToGroupIDs(tx)
	})
}

func migrateGroupsToIDs(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&hostGroupSchema{}) || !tx.Migrator().HasColumn(&hostGroupSchema{}, "path") {
		return nil
	}
	var rows []legacyGroupRow
	if err := tx.Table("host_groups").Find(&rows).Error; err != nil {
		return err
	}

	now := time.Now()
	order := int64(0)
	groups := make([]domainvault.Group, 0, len(rows)+4)
	byPath := make(map[string]string, len(rows))
	usedIDs := make(map[string]string, len(rows))
	for _, row := range rows {
		path := domainvault.NormalizeGroup(row.Path)
		if path == "" {
			continue
		}
		var group domainvault.Group
		if row.Data != "" {
			_ = json.Unmarshal([]byte(row.Data), &group)
		}
		id := row.GroupID
		if id == "" || (usedIDs[id] != "" && usedIDs[id] != path) {
			id = domainvault.GroupIDFromPath(path)
		}
		usedIDs[id] = path
		group.ID = id
		group.Path = path
		if row.Name != "" {
			group.Name = row.Name
		}
		if group.Name == "" {
			group.Name = domainvault.GroupName(path)
		}
		if group.ParentID == "" {
			group.ParentID = row.ParentID
		}
		if group.CreatedAt.IsZero() {
			group.CreatedAt = now
		}
		if group.UpdatedAt.IsZero() {
			group.UpdatedAt = now
		}
		if group.OrderKey == 0 {
			order += 1000
			group.OrderKey = order
		} else if group.OrderKey > order {
			order = group.OrderKey
		}
		groups = append(groups, group)
		byPath[path] = id
	}
	// A parent link written before IDs existed is the ancestor's path.
	for index := range groups {
		if groups[index].ParentID != "" {
			continue
		}
		if parentPath := domainvault.ParentGroupPath(groups[index].Path); parentPath != "" {
			groups[index].ParentID = byPath[parentPath]
		}
	}
	created := addMissingAncestors(byPath, &order, now)
	groups = append(groups, created...)

	if err := tx.Migrator().DropTable(&hostGroupSchema{}); err != nil {
		return err
	}
	if err := tx.AutoMigrate(&hostGroupSchema{}); err != nil {
		return err
	}
	for _, group := range groups {
		model, err := groupModel(group)
		if err != nil {
			return err
		}
		if err := tx.Table("host_groups").Create(&model).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateHostsToGroupIDs(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&hostSchema{}) || !tx.Migrator().HasColumn(&hostSchema{}, "group_path") {
		return nil
	}
	var rows []legacyHostRow
	if err := tx.Table("hosts").Find(&rows).Error; err != nil {
		return err
	}
	groups, err := migrationGroups(tx)
	if err != nil {
		return err
	}
	byPath := make(map[string]string, len(groups))
	byID := make(map[string]bool, len(groups))
	for _, group := range groups {
		byPath[group.Path] = group.ID
		byID[group.ID] = true
	}
	now := time.Now()
	order := nextGroupOrder(groups)

	hosts := make([]domainvault.Host, 0, len(rows))
	for _, row := range rows {
		var host domainvault.Host
		if row.Data != "" {
			if err := json.Unmarshal([]byte(row.Data), &host); err != nil {
				return err
			}
		}
		host.ID = model.HostID(row.ID)
		path := domainvault.NormalizeGroup(row.GroupPath)
		if path == "" {
			path = domainvault.NormalizeGroup(host.Group)
		}
		if path != "" && byPath[path] == "" {
			// A Host that referenced a path no record ever named keeps its
			// membership: the path becomes an explicit group.
			created := addPathRecords(byPath, path, &order, now)
			groups = append(groups, created...)
			for _, group := range created {
				byID[group.ID] = true
			}
		}
		groupID := host.GroupID
		if groupID == "" || !byID[groupID] {
			groupID = byPath[path]
		}
		host.GroupID = groupID
		host.Group = ""
		hosts = append(hosts, host)
	}

	if err := tx.Migrator().DropTable(&hostSchema{}); err != nil {
		return err
	}
	if err := tx.AutoMigrate(&hostSchema{}); err != nil {
		return err
	}
	for _, host := range hosts {
		model, err := hostModel(host)
		if err != nil {
			return err
		}
		if err := tx.Table("hosts").Create(&model).Error; err != nil {
			return err
		}
	}
	for _, group := range groups {
		if byPath[group.Path] == "" {
			continue
		}
		model, err := groupModel(group)
		if err != nil {
			return err
		}
		if err := tx.Table("host_groups").Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).Create(&model).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrationGroups reads the already-upgraded group tree.
func migrationGroups(tx *gorm.DB) ([]domainvault.Group, error) {
	var rows []dataSchemaRow
	if err := tx.Table("host_groups").Select("data").Order("order_key ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	data := make([]string, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.Data)
	}
	return decodeGroupRows(data)
}

// addPathRecords creates the records a path implies, root first.
func addPathRecords(byPath map[string]string, path string, order *int64, now time.Time) []domainvault.Group {
	var created []domainvault.Group
	for _, ancestor := range domainvault.GroupAncestors(path) {
		if byPath[ancestor] != "" {
			continue
		}
		parentID := ""
		if parentPath := domainvault.ParentGroupPath(ancestor); parentPath != "" {
			parentID = byPath[parentPath]
		}
		*order += 1000
		created = append(created, domainvault.Group{
			ID: domainvault.GroupIDFromPath(ancestor), ParentID: parentID,
			Name: domainvault.GroupName(ancestor), Path: ancestor,
			OrderKey: *order, CreatedAt: now, UpdatedAt: now,
		})
		byPath[ancestor] = domainvault.GroupIDFromPath(ancestor)
	}
	return created
}

// addMissingAncestors fills the implicit levels of every known path.
func addMissingAncestors(byPath map[string]string, order *int64, now time.Time) []domainvault.Group {
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(left, right int) bool {
		return len(paths[left]) < len(paths[right])
	})
	var created []domainvault.Group
	for _, path := range paths {
		created = append(created, addPathRecords(byPath, path, order, now)...)
	}
	return created
}

func nextGroupOrder(groups []domainvault.Group) int64 {
	var max int64
	for _, group := range groups {
		if group.OrderKey > max {
			max = group.OrderKey
		}
	}
	return max
}
