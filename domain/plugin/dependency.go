package plugin

import (
	"fmt"
	"sort"
	"strings"
)

// Dependency is one package-level dependency: a plugin id and a semver range.
// Dependencies are resolved at install and update time; they are distinct from
// runtime service injection.
type Dependency struct {
	ID      string `json:"id" toml:"id"`
	Version string `json:"version" toml:"version"`
}

// Validate checks the id shape and the range grammar.
func (d Dependency) Validate() error {
	if err := validatePluginID(d.ID); err != nil {
		return invalid("dependencies.id", "%v", err)
	}
	if !ValidRange(d.Version) {
		return invalid("dependencies.version", "%q is not a supported semver range", d.Version)
	}
	return nil
}

// ResolvedDependency is one dependency selected by resolution.
type ResolvedDependency struct {
	ID      string
	Version string
}

// ResolveDependencies resolves the transitive package dependencies of root
// against the available version lists. It picks the highest version that
// satisfies every collected constraint for an id, rejects cycles and
// conflicting constraints, and returns the dependencies sorted by id. The root
// itself is not included in the result.
func ResolveDependencies(root string, graph map[string][]Dependency,
	available map[string][]string) ([]ResolvedDependency, error) {
	constraints := make(map[string][]Dependency)
	visiting := make(map[string]bool)
	visited := make(map[string]bool)

	var walk func(string) error
	walk = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("plugin dependency: cycle through %s", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		deps := append([]Dependency(nil), graph[id]...)
		sort.SliceStable(deps, func(i, j int) bool { return deps[i].ID < deps[j].ID })
		for _, dep := range deps {
			if dep.ID == root {
				return fmt.Errorf("plugin dependency: cycle through %s", root)
			}
			if err := dep.Validate(); err != nil {
				return err
			}
			constraints[dep.ID] = append(constraints[dep.ID], dep)
			if err := walk(dep.ID); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(constraints))
	for id := range constraints {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	resolved := make([]ResolvedDependency, 0, len(ids))
	for _, id := range ids {
		version, err := satisfyAll(id, constraints[id], available[id])
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, ResolvedDependency{ID: id, Version: version})
	}
	return resolved, nil
}

// satisfyAll returns the highest available version that satisfies every
// constraint for id. It fails with a conflict when no version qualifies.
func satisfyAll(id string, constraints []Dependency, versions []string) (string, error) {
	var best Semver
	bestRaw := ""
	found := false
	for _, raw := range versions {
		version, ok := ParseSemver(raw)
		if !ok {
			continue
		}
		matched := true
		for _, constraint := range constraints {
			if !RangeContains(constraint.Version, version) {
				matched = false
				break
			}
		}
		if matched && (!found || version.Compare(best) > 0) {
			best = version
			bestRaw = raw
			found = true
		}
	}
	if !found {
		ranges := make([]string, 0, len(constraints))
		for _, constraint := range constraints {
			ranges = append(ranges, constraint.ID+" "+constraint.Version)
		}
		if len(constraints) > 1 {
			return "", fmt.Errorf("plugin dependency: conflict for %s: no version satisfies %s",
				id, strings.Join(ranges, ", "))
		}
		return "", fmt.Errorf("plugin dependency: no available version of %s satisfies %s",
			id, strings.Join(ranges, ", "))
	}
	return bestRaw, nil
}
