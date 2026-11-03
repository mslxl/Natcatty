package sync

// Host Groups are a movable tree inside the v2 CRDT. The CRDT keys a group by
// its stable ID and carries ParentID plus Name as ordinary registers, so a
// rename or move changes two fields instead of deleting one path-keyed record
// and creating another. Path is derived only when a v1-shaped payload is
// materialized, which keeps older readers working and keeps a derived field
// out of the register set.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// groupFamily is the v1 "groups" array with stable-ID identity and the
// path-to-tree normalization hook.
func groupFamily() domainsync.ConvergentFamily {
	return domainsync.ConvergentFamily{
		Name:      "groups",
		KeyFields: []string{"id"},
		Normalize: normalizeGroupRecord,
	}
}

// normalizeGroupRecord turns either payload shape into the canonical CRDT
// record. A path-only record (written before stable IDs) gets the
// deterministic path identity, and its parent gets the same identity its
// parent record would have. A new record is left with its explicit ID and
// tree fields; the derived path is discarded so it can never be a register.
func normalizeGroupRecord(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("group record must be an object: %w", err)
	}
	path, err := stringField(fields, "path")
	if err != nil {
		return nil, err
	}
	id, err := stringField(fields, "id")
	if err != nil {
		return nil, err
	}
	parentID, err := stringField(fields, "parentId")
	if err != nil {
		return nil, err
	}
	name, err := stringField(fields, "name")
	if err != nil {
		return nil, err
	}
	if path != "" {
		path = domainvault.NormalizeGroup(path)
		if id == "" {
			id = domainvault.GroupIDFromPath(path)
		}
		if name == "" {
			name = domainvault.GroupName(path)
		}
		if parentID == "" {
			if parentPath := domainvault.ParentGroupPath(path); parentPath != "" {
				parentID = domainvault.GroupIDFromPath(parentPath)
			}
		}
	}
	// Path is derived from the tree and must never become a register, even
	// when the source record carried an empty one.
	delete(fields, "path")
	if id == "" {
		return nil, fmt.Errorf("group record has neither id nor path")
	}
	if name == "" {
		name = id
	}
	encodedID, _ := json.Marshal(id)
	encodedName, _ := json.Marshal(name)
	fields["id"] = encodedID
	fields["name"] = encodedName
	if parentID == "" {
		delete(fields, "parentId")
	} else {
		encodedParent, _ := json.Marshal(parentID)
		fields["parentId"] = encodedParent
	}
	// Group's non-omitempty fields are always present when a payload is
	// re-marshaled. A record built from an older path-only value must carry
	// the same zero values, otherwise the migrated envelope and the normalized
	// inline payload disagree on the record's field set.
	if _, exists := fields["orderKey"]; !exists {
		fields["orderKey"] = json.RawMessage("0")
	}
	for _, field := range []string{"createdAt", "updatedAt"} {
		if _, exists := fields[field]; !exists {
			fields[field] = json.RawMessage(`"0001-01-01T00:00:00Z"`)
		}
	}
	return json.Marshal(fields)
}

// normalizeHostRecord turns the legacy derived group path into the stable
// GroupID that the tree actually stores. Deriving the ID in both directions
// (payload parsing and state migration) keeps an old envelope and the migrated
// state byte-for-byte comparable.
func normalizeHostRecord(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("host record must be an object: %w", err)
	}
	groupID, err := stringField(fields, "groupId")
	if err != nil {
		return nil, err
	}
	path, err := stringField(fields, "group")
	if err != nil {
		return nil, err
	}
	delete(fields, "group")
	if groupID == "" {
		if path = domainvault.NormalizeGroup(path); path != "" {
			groupID = domainvault.GroupIDFromPath(path)
		}
	}
	if groupID == "" {
		delete(fields, "groupId")
	} else {
		encoded, _ := json.Marshal(groupID)
		fields["groupId"] = encoded
	}
	return json.Marshal(fields)
}

// normalizeSnippetRecord does the same for a Snippet's target group paths.
func normalizeSnippetRecord(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("snippet record must be an object: %w", err)
	}
	var ids []string
	if value, exists := fields["targetGroupIds"]; exists {
		if err := json.Unmarshal(value, &ids); err != nil {
			return nil, fmt.Errorf("snippet targetGroupIds must be an array: %w", err)
		}
	}
	var paths []string
	if value, exists := fields["targetGroups"]; exists {
		if err := json.Unmarshal(value, &paths); err != nil {
			return nil, fmt.Errorf("snippet targetGroups must be an array: %w", err)
		}
	}
	delete(fields, "targetGroups")
	if len(ids) == 0 {
		ids = targetGroupIDsFromPaths(paths)
	}
	if len(ids) == 0 {
		delete(fields, "targetGroupIds")
	} else {
		encoded, _ := json.Marshal(ids)
		fields["targetGroupIds"] = encoded
	}
	return json.Marshal(fields)
}

// targetGroupIDsFromPaths maps legacy target paths to stable IDs in order,
// dropping empty paths and duplicates.
func targetGroupIDsFromPaths(paths []string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, path := range paths {
		path = domainvault.NormalizeGroup(path)
		if path == "" {
			continue
		}
		id := domainvault.GroupIDFromPath(path)
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// synthesizeLegacyGroupAncestors adds the implicit ancestor records a
// pre-stable-ID payload implies. Migration synthesizes the same nodes for the
// old CRDT state; doing it here too keeps the inline payload and the migrated
// envelope in agreement when an old replica is joined.
func synthesizeLegacyGroupAncestors(raw map[string]json.RawMessage) error {
	var records []map[string]json.RawMessage
	if value, exists := raw["groups"]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if err := json.Unmarshal(value, &records); err != nil {
			return fmt.Errorf("groups must be an array: %w", err)
		}
	}
	known := make(map[string]bool, len(records))
	needed := map[string]bool{}
	legacy := false
	addPath := func(path string) {
		path = domainvault.NormalizeGroup(path)
		if path == "" {
			return
		}
		legacy = true
		for _, ancestor := range domainvault.GroupAncestors(path) {
			needed[ancestor] = true
		}
	}
	for _, fields := range records {
		path, err := stringField(fields, "path")
		if err != nil {
			return err
		}
		id, err := stringField(fields, "id")
		if err != nil {
			return err
		}
		if path == "" {
			continue
		}
		known[domainvault.NormalizeGroup(path)] = true
		if id == "" {
			addPath(path)
		}
	}
	if value, exists := raw["hosts"]; exists {
		var hosts []map[string]json.RawMessage
		if err := json.Unmarshal(value, &hosts); err != nil {
			return fmt.Errorf("hosts must be an array: %w", err)
		}
		for _, fields := range hosts {
			groupID, err := stringField(fields, "groupId")
			if err != nil {
				return err
			}
			if groupID != "" {
				continue
			}
			path, err := stringField(fields, "group")
			if err != nil {
				return err
			}
			addPath(path)
		}
	}
	if value, exists := raw["snippets"]; exists {
		var snippets []map[string]json.RawMessage
		if err := json.Unmarshal(value, &snippets); err != nil {
			return fmt.Errorf("snippets must be an array: %w", err)
		}
		for _, fields := range snippets {
			var ids []string
			if value, exists := fields["targetGroupIds"]; exists {
				if err := json.Unmarshal(value, &ids); err != nil {
					return fmt.Errorf("snippet targetGroupIds must be an array: %w", err)
				}
			}
			if len(ids) > 0 {
				continue
			}
			var paths []string
			if value, exists := fields["targetGroups"]; exists {
				if err := json.Unmarshal(value, &paths); err != nil {
					return fmt.Errorf("snippet targetGroups must be an array: %w", err)
				}
			}
			for _, path := range paths {
				addPath(path)
			}
		}
	}
	if !legacy {
		return nil
	}
	added := 0
	for path := range needed {
		if known[path] {
			continue
		}
		encodedPath, _ := json.Marshal(path)
		records = append(records, map[string]json.RawMessage{"path": encodedPath})
		added++
	}
	if added == 0 {
		return nil
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return err
	}
	raw["groups"] = encoded
	return nil
}

func stringField(fields map[string]json.RawMessage, name string) (string, error) {
	raw, exists := fields[name]
	if !exists {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("group field %q is not a string: %w", name, err)
	}
	return value, nil
}

// migrateGroupFamily upgrades an in-memory replica whose "groups" collection
// is still keyed by path. The migration node and clock are derived from the
// old state, so two devices that hold the same old state produce byte-for-byte
// the same migrated causal state instead of two unrelated rewrites.
func migrateGroupFamily(state domainsync.ConvergentState) (domainsync.ConvergentState, bool, error) {
	snapshot, err := domainsync.MaterializeConvergentSnapshot(state)
	if err != nil {
		return state, false, err
	}
	records := snapshot.Families["groups"]
	needsRekey := false
	for _, record := range records {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(record.Value, &fields); err != nil {
			return state, false, err
		}
		id, err := stringField(fields, "id")
		if err != nil {
			return state, false, err
		}
		if id == "" || id != record.ID {
			needsRekey = true
			break
		}
	}
	needsHostStrip := snapshotHasField(snapshot.Families["hosts"], "group")
	needsSnippetStrip := snapshotHasField(snapshot.Families["snippets"], "targetGroups")
	if !needsRekey && !needsHostStrip && !needsSnippetStrip {
		return state, false, nil
	}

	// The migration node and clock derive from the old state so every replica
	// holding the same old state performs the same rewrite.
	hash, err := domainsync.ConvergentStateHash(state)
	if err != nil {
		return state, false, err
	}
	sum := sha256.Sum256([]byte("natcatty-group-tree-v1\x00" + hash))
	node := "group-tree-" + hex.EncodeToString(sum[:12])

	next := state
	groupRecords := records
	if needsRekey || (len(records) == 0 && (needsHostStrip || needsSnippetStrip)) {
		normalized, err := normalizeGroupFamily(records, snapshot)
		if err != nil {
			return state, false, err
		}
		// An old state can carry Hosts in an implicit group with no group record
		// at all. Rebuilding assigns the synthesized ancestor records their
		// stable IDs; when there is nothing to add, leave the family untouched.
		if needsRekey || len(normalized) > 0 {
			next, _, err = domainsync.RebuildConvergentFamily(next, "groups", normalized, node, next.HLC.WallTime)
			if err != nil {
				return state, false, err
			}
			groupRecords = normalized
		}
	}
	paths, err := groupPathsForMigration(groupRecords)
	if err != nil {
		return state, false, err
	}

	// A Host's group path and a Snippet's target paths are now derived from
	// stable IDs. Old entities still carry them as ordinary registers. The
	// stable reference is written first, then the derived register is
	// tombstoned; the tombstone preserves entity identity and every old dot.
	if needsHostStrip {
		updates := map[string]map[string]json.RawMessage{}
		for _, record := range snapshot.Families["hosts"] {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(record.Value, &fields); err != nil {
				return state, false, err
			}
			groupID, err := stringField(fields, "groupId")
			if err != nil {
				return state, false, err
			}
			if groupID != "" {
				continue
			}
			path, err := stringField(fields, "group")
			if err != nil {
				return state, false, err
			}
			if groupID = groupIDForPath(paths, path); groupID == "" {
				continue
			}
			encoded, _ := json.Marshal(groupID)
			updates[record.ID] = map[string]json.RawMessage{"groupId": encoded}
		}
		if len(updates) > 0 {
			next, _, err = domainsync.SetConvergentEntityFields(next, "hosts", updates, node, next.HLC.WallTime)
			if err != nil {
				return state, false, err
			}
		}
		next, _, err = domainsync.TombstoneConvergentFields(next, "hosts", []string{"group"}, node, next.HLC.WallTime)
		if err != nil {
			return state, false, err
		}
	}
	if needsSnippetStrip {
		updates := map[string]map[string]json.RawMessage{}
		for _, record := range snapshot.Families["snippets"] {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(record.Value, &fields); err != nil {
				return state, false, err
			}
			var ids []string
			if value, exists := fields["targetGroupIds"]; exists {
				if err := json.Unmarshal(value, &ids); err != nil {
					return state, false, err
				}
			}
			if len(ids) > 0 {
				continue
			}
			var legacy []string
			if value, exists := fields["targetGroups"]; exists {
				if err := json.Unmarshal(value, &legacy); err != nil {
					return state, false, err
				}
			}
			ids = targetGroupIDsForPaths(paths, legacy)
			if len(ids) == 0 {
				continue
			}
			encoded, _ := json.Marshal(ids)
			updates[record.ID] = map[string]json.RawMessage{"targetGroupIds": encoded}
		}
		if len(updates) > 0 {
			next, _, err = domainsync.SetConvergentEntityFields(next, "snippets", updates, node, next.HLC.WallTime)
			if err != nil {
				return state, false, err
			}
		}
		next, _, err = domainsync.TombstoneConvergentFields(next, "snippets", []string{"targetGroups"}, node, next.HLC.WallTime)
		if err != nil {
			return state, false, err
		}
	}
	return next, true, nil
}

// snapshotHasField reports whether any materialized record carries a field,
// even with an empty value. Old payload records marshaled the field
// unconditionally, so presence is the signal that it is still a register.
func snapshotHasField(records []domainsync.ConvergentRecord, field string) bool {
	for _, record := range records {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(record.Value, &fields); err != nil {
			continue
		}
		if _, exists := fields[field]; exists {
			return true
		}
	}
	return false
}

// normalizeGroupFamily converts every path-keyed record to a stable-ID record
// and adds the implicit ancestor nodes a Host or Snippet path implies.
func normalizeGroupFamily(records []domainsync.ConvergentRecord, snapshot domainsync.ConvergentSnapshot) ([]domainsync.ConvergentRecord, error) {
	byID := make(map[string]json.RawMessage, len(records))
	for _, record := range records {
		normalized, err := normalizeGroupRecord(record.Value)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(normalized, &fields); err != nil {
			return nil, err
		}
		id, err := stringField(fields, "id")
		if err != nil {
			return nil, err
		}
		if id == "" {
			return nil, fmt.Errorf("group record has no id after normalization")
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("group record %q is duplicated", id)
		}
		byID[id] = normalized
	}

	paths, err := groupPathsFromRecords(byID)
	if err != nil {
		return nil, err
	}
	pathIDs := make(map[string]string, len(paths))
	for id, path := range paths {
		pathIDs[path] = id
	}

	needed := map[string]bool{}
	addPath := func(path string) {
		for _, ancestor := range domainvault.GroupAncestors(path) {
			needed[ancestor] = true
		}
	}
	for _, path := range paths {
		addPath(path)
	}
	for _, record := range snapshot.Families["hosts"] {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(record.Value, &fields); err != nil {
			continue
		}
		path, _ := stringField(fields, "group")
		addPath(path)
	}
	for _, record := range snapshot.Families["snippets"] {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(record.Value, &fields); err != nil {
			continue
		}
		var targets []string
		if raw, exists := fields["targetGroups"]; exists {
			_ = json.Unmarshal(raw, &targets)
		}
		for _, path := range targets {
			addPath(path)
		}
	}
	missing := make([]string, 0, len(needed))
	for path := range needed {
		if _, exists := pathIDs[path]; exists {
			continue
		}
		missing = append(missing, path)
	}
	sort.Strings(missing)
	for _, path := range missing {
		id := domainvault.GroupIDFromPath(path)
		if _, exists := byID[id]; exists {
			continue
		}
		fields := map[string]json.RawMessage{}
		encodedID, _ := json.Marshal(id)
		encodedName, _ := json.Marshal(domainvault.GroupName(path))
		fields["id"] = encodedID
		fields["name"] = encodedName
		if parentPath := domainvault.ParentGroupPath(path); parentPath != "" {
			parentID := pathIDs[parentPath]
			if parentID == "" {
				parentID = domainvault.GroupIDFromPath(parentPath)
			}
			encodedParent, _ := json.Marshal(parentID)
			fields["parentId"] = encodedParent
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		// Normalize once more so the synthesized record has the same field set
		// as a record derived from a payload with a path.
		normalized, err := normalizeGroupRecord(encoded)
		if err != nil {
			return nil, err
		}
		byID[id] = normalized
		pathIDs[path] = id
	}

	out := make([]domainsync.ConvergentRecord, 0, len(byID))
	for id, value := range byID {
		out = append(out, domainsync.ConvergentRecord{ID: id, Value: value})
	}
	sort.Slice(out, func(left, right int) bool { return out[left].ID < out[right].ID })
	return out, nil
}

// groupPathsFromRecords resolves the tree paths of canonical group records.
func groupPathsFromRecords(records map[string]json.RawMessage) (map[string]string, error) {
	nodes := make([]domainvault.GroupTreeNode, 0, len(records))
	for id, raw := range records {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		parentID, err := stringField(fields, "parentId")
		if err != nil {
			return nil, err
		}
		name, err := stringField(fields, "name")
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, domainvault.GroupTreeNode{
			ID: id, ParentID: parentID, Name: name, OrderKey: intField(fields, "orderKey"),
		})
	}
	return domainvault.ResolveGroupTree(nodes), nil
}

func intField(fields map[string]json.RawMessage, name string) int64 {
	raw, exists := fields[name]
	if !exists {
		return 0
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0
	}
	return value
}

// groupRecordFields decodes one canonical group record.
func groupRecordFields(record domainsync.ConvergentRecord) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record.Value, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// decorateGroupPayload adds the derived path to each group record and
// rewrites Host and Snippet references from their stable IDs. Only derived
// fields and fields already present in the state are touched: adding a new
// register field here would make the inline payload disagree with the
// envelope during verification.
func decorateGroupPayload(raw map[string]json.RawMessage, snapshot domainsync.ConvergentSnapshot) error {
	paths, err := groupRecordPathMap(snapshot)
	if err != nil {
		return err
	}

	groups := make([]json.RawMessage, 0, len(snapshot.Families["groups"]))
	for _, record := range snapshot.Families["groups"] {
		fields, err := groupRecordFields(record)
		if err != nil {
			return err
		}
		path, ok := paths[record.ID]
		if !ok {
			path = ""
		}
		encodedPath, _ := json.Marshal(path)
		fields["path"] = encodedPath
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		groups = append(groups, encoded)
	}
	encodedGroups, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	raw["groups"] = encodedGroups

	if rawHosts, exists := raw["hosts"]; exists {
		var hosts []json.RawMessage
		if err := json.Unmarshal(rawHosts, &hosts); err != nil {
			return err
		}
		changed := false
		for index, hostRaw := range hosts {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(hostRaw, &fields); err != nil {
				return err
			}
			groupID, err := stringField(fields, "groupId")
			if err != nil {
				return err
			}
			if groupID == "" {
				continue
			}
			path, ok := paths[groupID]
			if !ok {
				continue
			}
			encodedPath, _ := json.Marshal(path)
			fields["group"] = encodedPath
			encoded, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			hosts[index] = encoded
			changed = true
		}
		if changed {
			encoded, err := json.Marshal(hosts)
			if err != nil {
				return err
			}
			raw["hosts"] = encoded
		}
	}

	if rawSnippets, exists := raw["snippets"]; exists {
		var snippets []json.RawMessage
		if err := json.Unmarshal(rawSnippets, &snippets); err != nil {
			return err
		}
		changed := false
		for index, snippetRaw := range snippets {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(snippetRaw, &fields); err != nil {
				return err
			}
			var ids []string
			if value, exists := fields["targetGroupIds"]; exists {
				if err := json.Unmarshal(value, &ids); err != nil {
					return err
				}
			}
			if len(ids) == 0 {
				continue
			}
			targets := make([]string, 0, len(ids))
			allFound := true
			for _, id := range ids {
				path, ok := paths[id]
				if !ok {
					allFound = false
					break
				}
				targets = append(targets, path)
			}
			if !allFound {
				continue
			}
			encodedTargets, _ := json.Marshal(targets)
			fields["targetGroups"] = encodedTargets
			encoded, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			snippets[index] = encoded
			changed = true
		}
		if changed {
			encoded, err := json.Marshal(snippets)
			if err != nil {
				return err
			}
			raw["snippets"] = encoded
		}
	}
	return nil
}

func groupRecordPathMap(snapshot domainsync.ConvergentSnapshot) (map[string]string, error) {
	records := snapshot.Families["groups"]
	nodes := make([]domainvault.GroupTreeNode, 0, len(records))
	for _, record := range records {
		fields, err := groupRecordFields(record)
		if err != nil {
			return nil, err
		}
		parentID, err := stringField(fields, "parentId")
		if err != nil {
			return nil, err
		}
		name, err := stringField(fields, "name")
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, domainvault.GroupTreeNode{
			ID: record.ID, ParentID: parentID, Name: name, OrderKey: intField(fields, "orderKey"),
		})
	}
	return domainvault.ResolveGroupTree(nodes), nil
}

// legacyGroupDeletionID translates a v1 group tombstone, which used the path
// as identity, into the stable ID the v2 groups family is keyed by.
func legacyGroupDeletionID(id string, paths map[string]string) string {
	if id == "" {
		return ""
	}
	for groupID, path := range paths {
		if id == groupID {
			return groupID
		}
		if path == domainvault.NormalizeGroup(id) {
			return groupID
		}
	}
	return domainvault.GroupIDFromPath(id)
}

// groupIDForPath returns the ID a materialized path belongs to, falling back
// to the deterministic legacy identity when no group record carries the path.
func groupIDForPath(paths map[string]string, path string) string {
	path = domainvault.NormalizeGroup(path)
	if path == "" {
		return ""
	}
	for id, candidate := range paths {
		if candidate == path {
			return id
		}
	}
	return domainvault.GroupIDFromPath(path)
}

// groupPathsForMigration resolves paths from canonical group records.
func groupPathsForMigration(records []domainsync.ConvergentRecord) (map[string]string, error) {
	byID := make(map[string]json.RawMessage, len(records))
	for _, record := range records {
		byID[record.ID] = record.Value
	}
	return groupPathsFromRecords(byID)
}

// targetGroupIDsForPaths maps legacy paths through the live tree and falls
// back to the deterministic path identity, preserving order and dropping
// duplicates.
func targetGroupIDsForPaths(paths map[string]string, legacy []string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, path := range legacy {
		id := groupIDForPath(paths, path)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}
