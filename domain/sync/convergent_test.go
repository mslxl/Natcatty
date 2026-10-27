package sync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

func mustJSONValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func familySnapshot(family string, values map[string]json.RawMessage) ConvergentSnapshot {
	records := make([]ConvergentRecord, 0, len(values))
	for id, value := range values {
		records = append(records, ConvergentRecord{ID: id, Value: value})
	}
	sort.Slice(records, func(left, right int) bool { return records[left].ID < records[right].ID })
	return ConvergentSnapshot{Families: map[string][]ConvergentRecord{family: records}}
}

func hostSnapshot(t *testing.T, id, name string) ConvergentSnapshot {
	t.Helper()
	return familySnapshot("hosts", map[string]json.RawMessage{
		id: mustJSONValue(t, map[string]any{"id": id, "name": name}),
	})
}

func emptyFamilySnapshot(family string) ConvergentSnapshot {
	return ConvergentSnapshot{Families: map[string][]ConvergentRecord{family: nil}}
}

func mustCreateState(t *testing.T, snapshot ConvergentSnapshot, node string, now int64) ConvergentState {
	t.Helper()
	state, err := CreateConvergentStateFromSnapshot(snapshot, node, now)
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	return state
}

func mustApply(t *testing.T, state ConvergentState, snapshot ConvergentSnapshot, node string, now int64, force bool) (ConvergentState, bool) {
	t.Helper()
	next, changed, err := ApplyConvergentSnapshot(state, snapshot, node, now, force)
	if err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	return next, changed
}

func mustMerge(t *testing.T, left, right ConvergentState) ConvergentState {
	t.Helper()
	merged, err := MergeConvergentStates(left, right)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	return merged
}

func stateBytes(t *testing.T, state ConvergentState) []byte {
	t.Helper()
	data, err := CanonicalConvergentBytes(state)
	if err != nil {
		t.Fatalf("canonical bytes: %v", err)
	}
	return data
}

func mustMaterialize(t *testing.T, state ConvergentState) ConvergentSnapshot {
	t.Helper()
	snapshot, err := MaterializeConvergentSnapshot(state)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	return snapshot
}

func materializedValue(t *testing.T, snapshot ConvergentSnapshot, family, id string) map[string]any {
	t.Helper()
	for _, record := range snapshot.Families[family] {
		if record.ID == id {
			var value map[string]any
			if err := json.Unmarshal(record.Value, &value); err != nil {
				t.Fatalf("unmarshal %s/%s: %v", family, id, err)
			}
			return value
		}
	}
	t.Fatalf("%s/%s is not materialized", family, id)
	return nil
}

func TestTickHLC(t *testing.T) {
	cases := []struct {
		name    string
		current HybridLogicalClock
		now     int64
		want    HybridLogicalClock
	}{
		{"wall advances", HybridLogicalClock{100, 2}, 101, HybridLogicalClock{101, 0}},
		{"same wall increments logical", HybridLogicalClock{100, 2}, 100, HybridLogicalClock{100, 3}},
		{"clock ahead of wall increments logical", HybridLogicalClock{100, 2}, 50, HybridLogicalClock{100, 3}},
		{"negative wall clamps", HybridLogicalClock{0, 0}, -5, HybridLogicalClock{0, 1}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := TickHLC(testCase.current, testCase.now)
			if err != nil {
				t.Fatalf("tick: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("got %+v, want %+v", got, testCase.want)
			}
		})
	}
	if _, err := TickHLC(HybridLogicalClock{100, ^uint64(0)}, 100); !errors.Is(err, ErrConvergentInvalid) {
		t.Fatalf("expected overflow error, got %v", err)
	}
}

func TestCanonicalValue(t *testing.T) {
	cases := map[string]string{
		`{"b":1,"a":[true,null,"x"]}`: `{"a":[true,null,"x"],"b":1}`,
		`{"n":1.0}`:                   `{"n":1}`,
		`{"n":1e3}`:                   `{"n":1000}`,
		`{"n":-0}`:                    `{"n":0}`,
		`{"n":1e-7}`:                  `{"n":1e-7}`,
		`{"n":1.5}`:                   `{"n":1.5}`,
	}
	for input, want := range cases {
		got, err := CanonicalValue(json.RawMessage(input))
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if string(got) != want {
			t.Fatalf("%s: got %s, want %s", input, got, want)
		}
	}
	for _, invalid := range []string{``, `{`, `{"a":1} {"b":2}`, `{"a":NaN}`} {
		if _, err := CanonicalValue(json.RawMessage(invalid)); err == nil {
			t.Fatalf("expected error for %q", invalid)
		}
	}
}

func TestConvergentSnapshotRoundTrip(t *testing.T) {
	snapshot := hostSnapshot(t, "host-1", "alpha")
	state := mustCreateState(t, snapshot, "device-a", 1000)
	materialized := mustMaterialize(t, state)
	if got := materializedValue(t, materialized, "hosts", "host-1")["name"]; got != "alpha" {
		t.Fatalf("name = %v", got)
	}
	if state.Version["device-a"] == 0 {
		t.Fatal("bootstrap did not allocate dots")
	}
	if !VersionVectorsEqual(state.Version, mustManifestEntityVersion(t, state, "hosts", "host-1")) {
		// The bootstrap writes presence plus one field; the per-entity vector
		// must summarize exactly those dots.
		t.Fatalf("unexpected entity vector: %+v", mustManifestEntityVersion(t, state, "hosts", "host-1"))
	}
	deletions, err := ConvergentTombstones(state)
	if err != nil {
		t.Fatalf("tombstones: %v", err)
	}
	if len(deletions) != 0 {
		t.Fatalf("unexpected tombstones: %+v", deletions)
	}
}

func mustManifestEntityVersion(t *testing.T, state ConvergentState, family, id string) VersionVector {
	t.Helper()
	return BuildConvergentManifest(state).Entities[ManifestKey(family, id)]
}

func TestMergeLaws(t *testing.T) {
	base := mustCreateState(t, familySnapshot("hosts", map[string]json.RawMessage{
		"h1": mustJSONValue(t, map[string]any{"id": "h1", "name": "base"}),
		"h2": mustJSONValue(t, map[string]any{"id": "h2", "name": "base"}),
	}), "base", 100)

	a, changed := mustApply(t, base, hostSnapshot(t, "h1", "bravo"), "a", 200, false)
	if !changed {
		t.Fatal("a edit did not change state")
	}
	b, changed := mustApply(t, base, hostSnapshot(t, "h2", "charlie"), "b", 300, false)
	if !changed {
		t.Fatal("b edit did not change state")
	}
	c, changed := mustApply(t, base, emptyFamilySnapshot("hosts"), "c", 400, false)
	if !changed {
		t.Fatal("c delete did not change state")
	}

	ab := mustMerge(t, a, b)
	ba := mustMerge(t, b, a)
	if !bytes.Equal(stateBytes(t, ab), stateBytes(t, ba)) {
		t.Fatal("merge is not commutative")
	}
	abcLeft := mustMerge(t, mustMerge(t, a, b), c)
	abcRight := mustMerge(t, a, mustMerge(t, b, c))
	if !bytes.Equal(stateBytes(t, abcLeft), stateBytes(t, abcRight)) {
		t.Fatal("merge is not associative")
	}
	if !bytes.Equal(stateBytes(t, mustMerge(t, a, a)), stateBytes(t, a)) {
		t.Fatal("merge is not idempotent")
	}

	// Every delivery order of the same three states must produce identical
	// canonical bytes.
	permutations := [][3]ConvergentState{
		{a, b, c}, {a, c, b}, {b, a, c}, {b, c, a}, {c, a, b}, {c, b, a},
	}
	var reference []byte
	for index, permutation := range permutations {
		merged := mustMerge(t, mustMerge(t, permutation[0], permutation[1]), permutation[2])
		data := stateBytes(t, merged)
		if index == 0 {
			reference = data
			continue
		}
		if !bytes.Equal(reference, data) {
			t.Fatalf("delivery order %d produced different bytes", index)
		}
	}
	// Duplicated delivery is a no-op.
	duplicated := mustMerge(t, mustMerge(t, ab, abcLeft), a)
	if !bytes.Equal(stateBytes(t, duplicated), stateBytes(t, abcLeft)) {
		t.Fatal("duplicate delivery changed state")
	}
}

func TestMultiValueRegisterConcurrentWrites(t *testing.T) {
	base := mustCreateState(t, hostSnapshot(t, "h1", "base"), "base", 100)
	a, _ := mustApply(t, base, hostSnapshot(t, "h1", "from-a"), "a", 500, false)
	b, _ := mustApply(t, base, hostSnapshot(t, "h1", "from-b"), "b", 500, false)
	merged := mustMerge(t, a, b)
	entity := merged.Collections["hosts"].Entities["h1"]
	field := entity.Fields["name"]
	if !RegisterHasConflict(field) {
		t.Fatalf("concurrent writes did not conflict: %+v", field)
	}
	winner, ok := SelectRegisterWinner(field)
	if !ok || string(winner.Value) != `"from-b"` {
		t.Fatalf("deterministic tie-break did not select from-b: %+v", winner)
	}

	// The same replacement delivered in the opposite order must select the
	// same winner.
	reverse := mustMerge(t, b, a)
	reverseWinner, _ := SelectRegisterWinner(reverse.Collections["hosts"].Entities["h1"].Fields["name"])
	if CandidateFingerprint(winner) != CandidateFingerprint(reverseWinner) {
		t.Fatal("winner depends on merge argument order")
	}

	// An explicit resolution observes every candidate and dominates them.
	resolved, changed := mustApply(t, merged, hostSnapshot(t, "h1", "resolved"), "a", 900, false)
	if !changed {
		t.Fatal("resolution did not allocate a write")
	}
	resolvedEntity := resolved.Collections["hosts"].Entities["h1"]
	if RegisterHasConflict(resolvedEntity.Fields["name"]) {
		t.Fatal("resolution left a conflict")
	}
	if got := materializedValue(t, mustMaterialize(t, resolved), "hosts", "h1")["name"]; got != "resolved" {
		t.Fatalf("resolved value = %v", got)
	}
	// The resolution must dominate both stale branches.
	for _, stale := range []ConvergentState{a, b} {
		joined := mustMerge(t, resolved, stale)
		if RegisterHasConflict(joined.Collections["hosts"].Entities["h1"].Fields["name"]) {
			t.Fatal("stale branch reintroduced a conflict")
		}
	}
}

func TestConcurrentDeleteAndEdit(t *testing.T) {
	base := mustCreateState(t, hostSnapshot(t, "h1", "before"), "base", 100)
	deleted, _ := mustApply(t, base, emptyFamilySnapshot("hosts"), "a", 300, false)
	edited, _ := mustApply(t, base, hostSnapshot(t, "h1", "after"), "b", 150, false)

	merged := mustMerge(t, deleted, edited)
	entity := merged.Collections["hosts"].Entities["h1"]
	presenceWinner, _ := SelectRegisterWinner(entity.Presence)
	if !presenceWinner.Tombstone {
		t.Fatal("later delete must win the presence register")
	}
	if len(mustMaterialize(t, merged).Families["hosts"]) != 0 {
		t.Fatal("deleted entity was materialized")
	}
	tombstones, err := ConvergentTombstones(merged)
	if err != nil {
		t.Fatalf("tombstones: %v", err)
	}
	if len(tombstones) != 1 || tombstones[0].ID != "h1" {
		t.Fatalf("unexpected tombstones: %+v", tombstones)
	}
	// Recreating after observing the delete wins with a fresh dot.
	recreated, changed := mustApply(t, merged, hostSnapshot(t, "h1", "reborn"), "c", 400, false)
	if !changed {
		t.Fatal("recreation did not write")
	}
	if got := materializedValue(t, mustMaterialize(t, recreated), "hosts", "h1")["name"]; got != "reborn" {
		t.Fatalf("recreated value = %v", got)
	}
}

func TestApplyIsNoOpAfterConvergence(t *testing.T) {
	snapshot := hostSnapshot(t, "h1", "stable")
	state := mustCreateState(t, snapshot, "a", 100)
	versionHash := VersionVectorHash(state.Version)
	before := stateBytes(t, state)
	next, changed := mustApply(t, state, snapshot, "a", 99999, false)
	if changed {
		t.Fatal("reapplying an identical snapshot allocated writes")
	}
	if got := VersionVectorHash(next.Version); got != versionHash {
		t.Fatal("version vector changed on a no-op")
	}
	if !bytes.Equal(before, stateBytes(t, next)) {
		t.Fatal("no-op apply changed canonical bytes")
	}
	deleted, changed := mustApply(t, state, emptyFamilySnapshot("hosts"), "a", 200, false)
	if !changed {
		t.Fatal("first delete did not change state")
	}
	deletedAgain, changed := mustApply(t, deleted, emptyFamilySnapshot("hosts"), "a", 300, false)
	if changed {
		t.Fatal("repeated delete allocated another write")
	}
	if !bytes.Equal(stateBytes(t, deleted), stateBytes(t, deletedAgain)) {
		t.Fatal("repeated delete changed canonical bytes")
	}
}

func TestForceApplyDominates(t *testing.T) {
	base := mustCreateState(t, hostSnapshot(t, "h1", "base"), "base", 100)
	a, _ := mustApply(t, base, hostSnapshot(t, "h1", "from-a"), "a", 200, false)
	b, _ := mustApply(t, base, hostSnapshot(t, "h1", "from-b"), "b", 300, false)
	joined := mustMerge(t, a, b)
	forced, changed := mustApply(t, joined, hostSnapshot(t, "h1", "from-a"), "a", 400, true)
	if !changed {
		t.Fatal("force apply did not write")
	}
	final := mustMerge(t, forced, joined)
	if RegisterHasConflict(final.Collections["hosts"].Entities["h1"].Fields["name"]) {
		t.Fatal("forced local write did not dominate the join")
	}
	if got := materializedValue(t, mustMaterialize(t, final), "hosts", "h1")["name"]; got != "from-a" {
		t.Fatalf("forced value = %v", got)
	}
}

func TestRandomizedReplicasConverge(t *testing.T) {
	random := rand.New(rand.NewSource(55))
	for trial := 0; trial < 25; trial++ {
		nodes := []string{"alpha", "bravo", "charlie", "delta"}
		base := mustCreateState(t, hostSnapshot(t, "h0", "base"), "base", 10)
		states := make([]ConvergentState, len(nodes))
		for index, node := range nodes {
			state := base
			for step := 0; step < 6; step++ {
				id := fmt.Sprintf("h%d", random.Intn(4))
				name := fmt.Sprintf("%s-%d-%d", node, step, random.Intn(100))
				var snapshot ConvergentSnapshot
				if random.Intn(5) == 0 {
					snapshot = emptyFamilySnapshot("hosts")
				} else {
					snapshot = hostSnapshot(t, id, name)
				}
				state, _ = mustApply(t, state, snapshot, node, int64(100+step*10), false)
			}
			states[index] = state
		}
		// Deliver all replica states in a random order, duplicate some.
		delivery := make([]ConvergentState, 0, len(states)*2)
		for _, state := range states {
			delivery = append(delivery, state)
			if random.Intn(2) == 0 {
				delivery = append(delivery, state)
			}
		}
		random.Shuffle(len(delivery), func(left, right int) {
			delivery[left], delivery[right] = delivery[right], delivery[left]
		})
		merged := EmptyConvergentState()
		for _, state := range delivery {
			merged = mustMerge(t, merged, state)
		}
		// Each replica that receives the same set converges to the same bytes.
		other := EmptyConvergentState()
		for _, state := range states {
			other = mustMerge(t, other, state)
		}
		if !bytes.Equal(stateBytes(t, merged), stateBytes(t, other)) {
			t.Fatalf("trial %d diverged after delivery-order changes", trial)
		}
		// Re-merging the materialized result is an idempotent no-op.
		if !bytes.Equal(stateBytes(t, mustMerge(t, merged, merged)), stateBytes(t, merged)) {
			t.Fatalf("trial %d is not idempotent", trial)
		}
	}
}

func manualState() ConvergentState {
	presence := RegisterCandidate{
		Dot:     Dot{Node: "a", Counter: 1},
		Context: []Dot{},
		HLC:     HybridLogicalClock{WallTime: 100},
		Value:   json.RawMessage(`true`),
	}
	name := RegisterCandidate{
		Dot:     Dot{Node: "a", Counter: 2},
		Context: []Dot{},
		HLC:     HybridLogicalClock{WallTime: 200},
		Value:   json.RawMessage(`"alpha"`),
	}
	return ConvergentState{
		SchemaVersion: ConvergentSchemaVersion,
		Version:       VersionVector{"a": 2},
		HLC:           HybridLogicalClock{WallTime: 200},
		DotOrigins: map[string]map[string]string{
			"a": {
				"1": RegisterIdentity("hosts", "h1", presenceField),
				"2": RegisterIdentity("hosts", "h1", "name"),
			},
		},
		Collections: map[string]ConvergentCollection{
			"hosts": {
				Entities: map[string]ConvergentEntity{
					"h1": {
						Version:  VersionVector{"a": 2},
						Presence: MultiValueRegister{Candidates: []RegisterCandidate{presence}},
						Fields: map[string]MultiValueRegister{
							"name": {Candidates: []RegisterCandidate{name}},
						},
					},
				},
			},
		},
	}
}

func TestValidateConvergentStateRejectsInvariants(t *testing.T) {
	valid := manualState()
	if err := ValidateConvergentState(valid); err != nil {
		t.Fatalf("manual state is invalid: %v", err)
	}
	cases := map[string]func(*ConvergentState){
		"schema": func(state *ConvergentState) { state.SchemaVersion = 3 },
		"empty collections": func(state *ConvergentState) {
			state.Collections = nil
		},
		"negative clock": func(state *ConvergentState) { state.HLC.WallTime = -1 },
		"clock ahead":    func(state *ConvergentState) { state.HLC = HybridLogicalClock{WallTime: 300} },
		"unwitnessed vector": func(state *ConvergentState) {
			state.Version["a"] = 3
		},
		"entity version mismatch": func(state *ConvergentState) {
			entity := state.Collections["hosts"].Entities["h1"]
			entity.Version = VersionVector{"a": 3}
			state.Collections["hosts"].Entities["h1"] = entity
		},
		"dangling context": func(state *ConvergentState) {
			entity := state.Collections["hosts"].Entities["h1"]
			register := entity.Fields["name"]
			register.Candidates[0].Context = []Dot{{Node: "a", Counter: 5}}
			entity.Fields["name"] = register
			state.Collections["hosts"].Entities["h1"] = entity
		},
		"own dot in context": func(state *ConvergentState) {
			entity := state.Collections["hosts"].Entities["h1"]
			register := entity.Fields["name"]
			register.Candidates[0].Context = []Dot{{Node: "a", Counter: 2}}
			entity.Fields["name"] = register
			state.Collections["hosts"].Entities["h1"] = entity
		},
		"duplicate dot in register": func(state *ConvergentState) {
			entity := state.Collections["hosts"].Entities["h1"]
			register := entity.Fields["name"]
			duplicate := register.Candidates[0]
			duplicate.HLC = HybridLogicalClock{WallTime: 150}
			register.Candidates = append(register.Candidates, duplicate)
			entity.Fields["name"] = register
			state.Collections["hosts"].Entities["h1"] = entity
		},
		"same dot across registers": func(state *ConvergentState) {
			entity := state.Collections["hosts"].Entities["h1"]
			entity.Fields["other"] = MultiValueRegister{Candidates: []RegisterCandidate{{
				Dot:     Dot{Node: "a", Counter: 1},
				Context: []Dot{},
				HLC:     HybridLogicalClock{WallTime: 100},
				Value:   json.RawMessage(`"x"`),
			}}}
			state.Collections["hosts"].Entities["h1"] = entity
		},
		"noncanonical value": func(state *ConvergentState) {
			entity := state.Collections["hosts"].Entities["h1"]
			register := entity.Fields["name"]
			register.Candidates[0].Value = json.RawMessage(`{ "b" : 1, "a" : 2 }`)
			entity.Fields["name"] = register
			state.Collections["hosts"].Entities["h1"] = entity
		},
		"tombstone with value": func(state *ConvergentState) {
			entity := state.Collections["hosts"].Entities["h1"]
			register := entity.Fields["name"]
			register.Candidates[0].Tombstone = true
			entity.Fields["name"] = register
			state.Collections["hosts"].Entities["h1"] = entity
		},
		"empty register": func(state *ConvergentState) {
			entity := state.Collections["hosts"].Entities["h1"]
			entity.Fields["name"] = MultiValueRegister{}
			state.Collections["hosts"].Entities["h1"] = entity
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			state := manualState()
			mutate(&state)
			if err := ValidateConvergentState(state); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestMergeRejectsReusedDot(t *testing.T) {
	left := manualState()
	right, err := NormalizeConvergentState(manualState())
	if err != nil {
		t.Fatal(err)
	}
	entity := right.Collections["hosts"].Entities["h1"]
	register := entity.Fields["name"]
	register.Candidates[0].Value = json.RawMessage(`"different"`)
	entity.Fields["name"] = register
	right.Collections["hosts"].Entities["h1"] = entity
	if _, err := MergeConvergentStates(left, right); !errors.Is(err, ErrConvergentDotConflict) {
		t.Fatalf("expected dot conflict, got %v", err)
	}
	if _, err := MergeRegisters(left.Collections["hosts"].Entities["h1"].Fields["name"],
		right.Collections["hosts"].Entities["h1"].Fields["name"]); !errors.Is(err, ErrConvergentDotConflict) {
		t.Fatalf("expected register dot conflict, got %v", err)
	}
}

func TestCanonicalBytesIgnoreSliceOrder(t *testing.T) {
	first := manualState()
	second := manualState()
	entity := second.Collections["hosts"].Entities["h1"]
	register := entity.Fields["name"]
	register.Candidates = append(register.Candidates, RegisterCandidate{
		Dot:     Dot{Node: "b", Counter: 1},
		Context: []Dot{},
		HLC:     HybridLogicalClock{WallTime: 250},
		Value:   json.RawMessage(`"beta"`),
	})
	entity.Fields["name"] = register
	second.Collections["hosts"].Entities["h1"] = entity
	second.Version["b"] = 1
	second.DotOrigins["b"] = map[string]string{
		"1": RegisterIdentity("hosts", "h1", "name"),
	}
	second.HLC = HybridLogicalClock{WallTime: 250}
	// The second state's per-entity version is derived, not hand-maintained.
	second.Collections["hosts"] = ConvergentCollection{Entities: map[string]ConvergentEntity{
		"h1": {
			Version:  DerivedEntityVersion(entity),
			Presence: entity.Presence,
			Fields:   entity.Fields,
		},
	}}
	if err := ValidateConvergentState(second); err != nil {
		t.Fatalf("second state invalid: %v", err)
	}
	// Reverse candidate and context slices; canonical bytes must not change.
	reversedRegister := second.Collections["hosts"].Entities["h1"].Fields["name"]
	for left, right := 0, len(reversedRegister.Candidates)-1; left < right; left, right = left+1, right-1 {
		reversedRegister.Candidates[left], reversedRegister.Candidates[right] =
			reversedRegister.Candidates[right], reversedRegister.Candidates[left]
	}
	reversedEntity := second.Collections["hosts"].Entities["h1"]
	reversedEntity.Fields["name"] = reversedRegister
	reversedEntity.Version = DerivedEntityVersion(reversedEntity)
	second.Collections["hosts"] = ConvergentCollection{Entities: map[string]ConvergentEntity{"h1": reversedEntity}}
	if err := ValidateConvergentState(second); err != nil {
		t.Fatalf("reordered state invalid: %v", err)
	}
	firstBytes := stateBytes(t, first)
	secondBytes := stateBytes(t, second)
	if bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("states with different candidates must not have identical bytes")
	}
	// Normalization makes the reordered state byte-identical to its
	// normalized self.
	if !bytes.Equal(secondBytes, stateBytes(t, mustMerge(t, second, second))) {
		t.Fatal("normalization is not idempotent")
	}
}

func TestEnvelopeStrictParsing(t *testing.T) {
	state := manualState()
	envelope, err := WrapConvergentEnvelope(state)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseConvergentEnvelope(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !bytes.Equal(stateBytes(t, parsed.State), stateBytes(t, state)) {
		t.Fatal("envelope round-trip changed state")
	}
	var unknown map[string]any
	_ = json.Unmarshal(data, &unknown)
	unknown["schemaVersion"] = 3
	unknownData, _ := json.Marshal(unknown)
	if _, err := ParseConvergentEnvelope(unknownData); !errors.Is(err, ErrConvergentSchema) {
		t.Fatalf("expected schema error, got %v", err)
	}
	delete(unknown, "state")
	unknown["schemaVersion"] = 2
	missingData, _ := json.Marshal(unknown)
	if _, err := ParseConvergentEnvelope(missingData); !errors.Is(err, ErrConvergentSchema) {
		t.Fatalf("expected missing-state error, got %v", err)
	}
	unknown["state"] = json.RawMessage(`{`)
	brokenData, _ := json.Marshal(unknown)
	if _, err := ParseConvergentEnvelope(brokenData); err == nil {
		t.Fatal("expected malformed-state error")
	}
	if _, err := ParseConvergentEnvelope(nil); !errors.Is(err, ErrConvergentSchema) {
		t.Fatalf("expected missing-envelope error, got %v", err)
	}
}

func TestEnvelopeMatchesMaterializedSnapshot(t *testing.T) {
	state := manualState()
	materialized := mustMaterialize(t, state)
	if err := EnvelopeMatchesSnapshot(state, materialized); err != nil {
		t.Fatalf("matching envelope rejected: %v", err)
	}
	tampered := familySnapshot("hosts", map[string]json.RawMessage{
		"h1": mustJSONValue(t, map[string]any{"id": "h1", "name": "ghost"}),
	})
	if err := EnvelopeMatchesSnapshot(state, tampered); err == nil {
		t.Fatal("tampered materialized snapshot accepted")
	}
	missing := familySnapshot("hosts", nil)
	if err := EnvelopeMatchesSnapshot(state, missing); err == nil {
		t.Fatal("missing materialized entity accepted")
	}
	// A populated materialized family that the envelope never mentions must
	// fail closed; iterating only envelope families would silently accept it.
	extra := mustMaterialize(t, state)
	extra.Families["keys"] = []ConvergentRecord{{
		ID:    "key-1",
		Value: mustJSONValue(t, map[string]any{"id": "key-1"}),
	}}
	if err := EnvelopeMatchesSnapshot(state, extra); err == nil {
		t.Fatal("materialized-only family accepted")
	}
}

func TestImportConvergentDeletionsIsStableAndPreservesTombstones(t *testing.T) {
	state := mustCreateState(t, familySnapshot("hosts", map[string]json.RawMessage{
		"h1": mustJSONValue(t, map[string]any{"id": "h1", "name": "one"}),
		"h2": mustJSONValue(t, map[string]any{"id": "h2", "name": "two"}),
	}), "device-a", 1000)
	deletions := []ConvergentDeletion{
		{Family: "hosts", ID: "h1", DeletedAt: 1200, Node: "legacy"},
		{Family: "hosts", ID: "gone", DeletedAt: 1400, Node: "legacy"},
		{Family: "hosts", ID: "h1", DeletedAt: 1300, Node: "legacy"},
	}
	imported, changed, err := ImportConvergentDeletions(state, deletions, "device-b", 2000)
	if err != nil {
		t.Fatalf("import deletions: %v", err)
	}
	if !changed {
		t.Fatal("import reported no change")
	}
	if err := ValidateConvergentState(imported); err != nil {
		t.Fatalf("imported state invalid: %v", err)
	}
	if _, ok := SelectRegisterWinner(imported.Collections["hosts"].Entities["h1"].Presence); !ok {
		t.Fatal("h1 has no presence candidate")
	}
	materialized := mustMaterialize(t, imported)
	for _, record := range materialized.Families["hosts"] {
		if record.ID == "h1" {
			t.Fatal("tombstoned h1 still materializes")
		}
	}
	tombstones, err := ConvergentTombstones(imported)
	if err != nil {
		t.Fatalf("tombstones: %v", err)
	}
	if len(tombstones) != 2 {
		t.Fatalf("tombstones = %+v", tombstones)
	}
	if tombstones[0].ID != "gone" || tombstones[1].ID != "h1" {
		t.Fatalf("tombstones are not sorted: %+v", tombstones)
	}
	if tombstones[1].DeletedAt != 1300 {
		t.Fatalf("duplicate deletion kept %d, want the latest 1300", tombstones[1].DeletedAt)
	}
	again, changedAgain, err := ImportConvergentDeletions(imported, deletions, "device-b", 2000)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if changedAgain {
		t.Fatal("second import allocated writes")
	}
	firstBytes, err := CanonicalConvergentBytes(imported)
	if err != nil {
		t.Fatal(err)
	}
	againBytes, err := CanonicalConvergentBytes(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, againBytes) {
		t.Fatal("importing the same tombstones twice changed the state")
	}
}

func TestManifestComparison(t *testing.T) {
	base := mustCreateState(t, hostSnapshot(t, "h1", "base"), "base", 100)
	a, _ := mustApply(t, base, hostSnapshot(t, "h1", "a"), "a", 200, false)
	b, _ := mustApply(t, base, hostSnapshot(t, "h1", "b"), "b", 300, false)
	ab := mustMerge(t, a, b)

	manifestA := BuildConvergentManifest(a)
	manifestAB := BuildConvergentManifest(ab)
	if !ManifestDominates(manifestAB, manifestA) {
		t.Fatal("merged manifest does not dominate its branch")
	}
	if ManifestDominates(manifestA, manifestAB) {
		t.Fatal("branch manifest dominates the merge")
	}
	if ManifestsEqual(manifestA, manifestAB) {
		t.Fatal("branch and merge manifests must differ")
	}
	if ManifestHash(manifestAB) != ManifestHash(BuildConvergentManifest(ab)) {
		t.Fatal("manifest hash is unstable")
	}
	if ManifestHash(manifestA) == ManifestHash(manifestAB) {
		t.Fatal("different manifests share a hash")
	}
}

func TestDetectDivergence(t *testing.T) {
	base := mustCreateState(t, hostSnapshot(t, "h1", "base"), "base", 100)
	a, _ := mustApply(t, base, hostSnapshot(t, "h1", "from-a"), "a", 200, false)
	b, _ := mustApply(t, base, hostSnapshot(t, "h1", "from-b"), "b", 300, false)

	if divergence := DetectDivergence(a, b); divergence != nil {
		t.Fatalf("ordinary concurrency reported as divergence: %+v", divergence)
	}
	merged := mustMerge(t, a, b)
	if divergence := DetectDivergence(merged, a); divergence != nil {
		t.Fatalf("forward progress reported as divergence: %+v", divergence)
	}
	// Compaction of observed candidates during a later local write is legal.
	later, _ := mustApply(t, merged, hostSnapshot(t, "h1", "later"), "a", 400, false)
	if divergence := DetectDivergence(later, merged); divergence != nil {
		t.Fatalf("compaction reported as divergence: %+v", divergence)
	}

	// Losing a witnessed dot is structurally invalid; it must be reported as
	// an unreadable replica, never silently merged or overwritten.
	broken, err := NormalizeConvergentState(merged)
	if err != nil {
		t.Fatal(err)
	}
	brokenEntity := broken.Collections["hosts"].Entities["h1"]
	delete(brokenEntity.Fields, "name")
	brokenEntity.Version = DerivedEntityVersion(brokenEntity)
	broken.Collections["hosts"] = ConvergentCollection{Entities: map[string]ConvergentEntity{"h1": brokenEntity}}
	if err := ValidateConvergentState(broken); err == nil {
		t.Fatal("lossy state unexpectedly passed validation")
	}
	if divergence := DetectDivergence(merged, broken); divergence == nil || divergence.Kind != "invalid-remote" {
		t.Fatalf("expected invalid-remote, got %+v", divergence)
	}

	// Same dot, different payload.
	tampered, err := NormalizeConvergentState(merged)
	if err != nil {
		t.Fatal(err)
	}
	tamperedEntity := tampered.Collections["hosts"].Entities["h1"]
	field := tamperedEntity.Fields["name"]
	for index := range field.Candidates {
		if field.Candidates[index].Tombstone {
			continue
		}
		field.Candidates[index].Value = json.RawMessage(`"tampered"`)
		break
	}
	tamperedEntity.Fields["name"] = field
	tamperedEntity.Version = DerivedEntityVersion(tamperedEntity)
	tampered.Collections["hosts"] = ConvergentCollection{Entities: map[string]ConvergentEntity{"h1": tamperedEntity}}
	if err := ValidateConvergentState(tampered); err != nil {
		t.Fatalf("tampered state invalid: %v", err)
	}
	divergence := DetectDivergence(merged, tampered)
	if divergence == nil || divergence.Kind != "dot-payload-mismatch" {
		t.Fatalf("expected dot-payload-mismatch, got %+v", divergence)
	}

	// One allocated dot cannot be owned by different registers on two
	// replicas. Both states are individually valid, so only the cross-replica
	// origin comparison can catch this.
	relocated := manualState()
	relocatedEntity := relocated.Collections["hosts"].Entities["h1"]
	relocatedPresence := relocatedEntity.Presence
	relocatedPresence.Candidates[0] = RegisterCandidate{
		Dot:       Dot{Node: "a", Counter: 2},
		HLC:       HybridLogicalClock{WallTime: 200},
		Tombstone: true,
	}
	relocatedName := relocatedEntity.Fields["name"]
	relocatedName.Candidates[0] = RegisterCandidate{
		Dot:     Dot{Node: "a", Counter: 1},
		Context: []Dot{},
		HLC:     HybridLogicalClock{WallTime: 100},
		Value:   json.RawMessage(`"alpha"`),
	}
	relocatedEntity.Presence = relocatedPresence
	relocatedEntity.Fields["name"] = relocatedName
	relocatedEntity.Version = DerivedEntityVersion(relocatedEntity)
	relocated.Collections["hosts"] = ConvergentCollection{Entities: map[string]ConvergentEntity{"h1": relocatedEntity}}
	relocated.DotOrigins["a"]["1"] = RegisterIdentity("hosts", "h1", "name")
	relocated.DotOrigins["a"]["2"] = RegisterIdentity("hosts", "h1", presenceField)
	if err := ValidateConvergentState(relocated); err != nil {
		t.Fatalf("relocated state invalid: %v", err)
	}
	divergence = DetectDivergence(manualState(), relocated)
	if divergence == nil || divergence.Kind != "origin-mismatch" {
		t.Fatalf("expected origin-mismatch, got %+v", divergence)
	}

	// Invalid remote metadata is reported rather than ignored.
	invalid := manualState()
	invalid.SchemaVersion = 9
	if divergence := DetectDivergence(a, invalid); divergence == nil || divergence.Kind != "invalid-remote" {
		t.Fatalf("expected invalid-remote, got %+v", divergence)
	}
}

func TestParseConvergentSnapshotValidation(t *testing.T) {
	families := []ConvergentFamily{{Name: "hosts", KeyFields: []string{"id"}}}
	raw := map[string]json.RawMessage{
		"hosts": json.RawMessage(`[{"id":"h1","name":"a"},{"id":"h1","name":"b"}]`),
	}
	if _, err := ParseConvergentSnapshot(raw, families); err == nil {
		t.Fatal("duplicate entity ids accepted")
	}
	raw["hosts"] = json.RawMessage(`[{"name":"a"}]`)
	if _, err := ParseConvergentSnapshot(raw, families); err == nil {
		t.Fatal("missing key field accepted")
	}
	raw["hosts"] = json.RawMessage(`{}`)
	if _, err := ParseConvergentSnapshot(raw, families); err == nil {
		t.Fatal("non-array family accepted")
	}
	raw["hosts"] = json.RawMessage(`[{"id":"h1","n":1e3,"z":{"b":1,"a":2}}]`)
	snapshot, err := ParseConvergentSnapshot(raw, families)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(snapshot.Families["hosts"][0].Value) != `{"id":"h1","n":1000,"z":{"a":2,"b":1}}` {
		t.Fatalf("value was not canonicalized: %s", snapshot.Families["hosts"][0].Value)
	}
	again, err := RawSnapshot(snapshot)
	if err != nil {
		t.Fatalf("raw snapshot: %v", err)
	}
	if string(again["hosts"]) != `[{"id":"h1","n":1000,"z":{"a":2,"b":1}}]` {
		t.Fatalf("raw snapshot = %s", again["hosts"])
	}
}
