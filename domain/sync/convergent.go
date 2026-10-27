package sync

// Convergent sync v2: a state-based CRDT for the cloud Vault.
//
// The model follows the original Electron convergent core at 73d15da7
// (docs/designs/convergent-sync-crdt.md and convergent-sync-protocol-v2.md):
//
//   - a dotted version vector allocates globally unique dots per device;
//   - every entity also carries a per-entity version vector summary;
//   - each mutable value is an observed-remove multi-value register whose
//     candidates carry the exact dot set the writer had observed;
//   - deletion is a tombstone candidate, retained indefinitely;
//   - a hybrid logical clock supplies a user-facing order without defining
//     causality; the deterministic winner order is value over tombstone, then
//     HLC, then node id, then counter;
//   - merge is commutative, associative, and idempotent; canonical bytes are
//     independent of delivery order or duplication.
//
// The register context is an exact dot set rather than a compact version
// vector because one device's global counters interleave writes to different
// registers. This package imports only the standard library (ADR-0001).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ConvergentSchemaVersion is the only v2 schema this build reads or writes.
const ConvergentSchemaVersion = 2

// ConvergentEncodingMaterializedWinner is the envelope encoding name. The
// payload keeps a complete materialized v1 snapshot beside the envelope so an
// older client can still read hosts, keys, snippets, and settings.
const ConvergentEncodingMaterializedWinner = "materialized-winner-v1"

// Errors callers map onto typed sync problems.
var (
	// ErrConvergentSchema reports an unknown, newer, or malformed schema.
	ErrConvergentSchema = errors.New("sync: unsupported convergent schema")
	// ErrConvergentInvalid reports a state that violates a dot, context,
	// origin, vector, HLC, or JSON invariant.
	ErrConvergentInvalid = errors.New("sync: invalid convergent state")
	// ErrConvergentDotConflict reports one dot reused for different payloads.
	ErrConvergentDotConflict = errors.New("sync: a convergent dot was reused for different data")
)

// VersionVector maps a device (node) id to the highest counter this replica has
// observed from that device.
type VersionVector map[string]uint64

// HybridLogicalClock is the user-facing ordering hint. It never defines
// causality; dots and register contexts do.
type HybridLogicalClock struct {
	WallTime int64  `json:"wallTime"`
	Logical  uint64 `json:"logical"`
}

// Dot is one globally unique write: a node id and a monotonically increasing
// counter owned by that node.
type Dot struct {
	Node    string `json:"node"`
	Counter uint64 `json:"counter"`
}

// RegisterCandidate is one concurrent value or tombstone in a multi-value
// register. Context records the exact dots this writer had observed in the
// register before writing.
type RegisterCandidate struct {
	Dot       Dot                `json:"dot"`
	Context   []Dot              `json:"context"`
	HLC       HybridLogicalClock `json:"hlc"`
	Tombstone bool               `json:"tombstone,omitempty"`
	Value     json.RawMessage    `json:"value,omitempty"`
}

// MultiValueRegister retains every concurrent candidate. A register with one
// candidate is the ordinary single-value case.
type MultiValueRegister struct {
	Candidates []RegisterCandidate `json:"candidates"`
}

// ConvergentEntity is one entity's causal state. Version is the per-entity
// version vector summary; the exact register contexts remain authoritative.
type ConvergentEntity struct {
	Version  VersionVector                 `json:"version"`
	Presence MultiValueRegister            `json:"presence"`
	Fields   map[string]MultiValueRegister `json:"fields"`
}

// ConvergentCollection groups entities of one payload family.
type ConvergentCollection struct {
	Entities map[string]ConvergentEntity `json:"entities"`
}

// ConvergentState is the pure CRDT state. It contains no provider,
// persistence, or UI concerns. DotOrigins is the permanent dot-origin index:
// it maps every allocated device counter to the register that owns it, so a
// forged candidate context cannot borrow dots from another register.
type ConvergentState struct {
	SchemaVersion int                             `json:"schemaVersion"`
	Version       VersionVector                   `json:"version"`
	HLC           HybridLogicalClock              `json:"hlc"`
	DotOrigins    map[string]map[string]string    `json:"dotOrigins"`
	Collections   map[string]ConvergentCollection `json:"collections"`
}

// ConvergentEnvelope is stored inside the encrypted payload under
// "convergentSync". Plaintext metadata only advertises syncSchemaVersion: 2.
type ConvergentEnvelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Encoding      string          `json:"encoding"`
	State         ConvergentState `json:"state"`
}

// ConvergentFamily describes one payload array and the fields that identify a
// record inside it.
type ConvergentFamily struct {
	Name      string
	KeyFields []string
}

// ConvergentRecord is one materialized record.
type ConvergentRecord struct {
	ID    string          `json:"id"`
	Value json.RawMessage `json:"value"`
}

// ConvergentSnapshot is the materialized v1 view of the CRDT families. It is
// the bridge between the domain package and the application's typed Payload.
type ConvergentSnapshot struct {
	Families map[string][]ConvergentRecord `json:"families"`
}

// ConvergentDeletion is a materialized entity tombstone for the v1
// syncMeta.deletions field, so an older client sees the same deletion.
type ConvergentDeletion struct {
	Family    string `json:"family"`
	ID        string `json:"id"`
	DeletedAt int64  `json:"deletedAt"`
	Node      string `json:"node"`
}

// ConvergentDivergence reports a genuine inconsistency between two replicas,
// as opposed to ordinary concurrency. Kind is a stable protocol identifier the
// UI localizes.
type ConvergentDivergence struct {
	Kind         string        `json:"kind"`
	Family       string        `json:"family,omitempty"`
	EntityID     string        `json:"entityId,omitempty"`
	Dot          string        `json:"dot,omitempty"`
	LocalHash    string        `json:"localHash,omitempty"`
	RemoteHash   string        `json:"remoteHash,omitempty"`
	LocalVector  VersionVector `json:"localVector,omitempty"`
	RemoteVector VersionVector `json:"remoteVector,omitempty"`
	Detail       string        `json:"detail,omitempty"`
}

// Error lets a divergence be returned as a typed error.
func (d *ConvergentDivergence) Error() string {
	if d == nil {
		return "sync: replicas diverged"
	}
	where := d.Family
	if d.EntityID != "" {
		where += "/" + d.EntityID
	}
	if where == "" {
		where = "state"
	}
	return fmt.Sprintf("sync: replicas diverged (%s at %s): %s", d.Kind, where, d.Detail)
}

// EmptyConvergentState returns the valid v2 zero state.
func EmptyConvergentState() ConvergentState {
	return ConvergentState{
		SchemaVersion: ConvergentSchemaVersion,
		Version:       VersionVector{},
		HLC:           HybridLogicalClock{},
		DotOrigins:    map[string]map[string]string{},
		Collections:   map[string]ConvergentCollection{},
	}
}

// RegisterIdentity is the stable address of one register inside the state.
func RegisterIdentity(family, id, field string) string {
	return family + "\x00" + id + "\x00" + field
}

const presenceField = "<presence>"

// ---------------------------------------------------------------------------
// Version vectors, dots, and clocks
// ---------------------------------------------------------------------------

// CompareDots orders dots by node then counter, independent of locale.
func CompareDots(left, right Dot) int {
	if left.Node < right.Node {
		return -1
	}
	if left.Node > right.Node {
		return 1
	}
	switch {
	case left.Counter < right.Counter:
		return -1
	case left.Counter > right.Counter:
		return 1
	}
	return 0
}

// DotKey is the stable identity of a dot.
func DotKey(dot Dot) string {
	return dot.Node + ":" + strconv.FormatUint(dot.Counter, 10)
}

// MergeVersionVectors joins two vectors by pointwise maximum.
func MergeVersionVectors(left, right VersionVector) VersionVector {
	merged := make(VersionVector, len(left)+len(right))
	for node, counter := range left {
		if counter > 0 {
			merged[node] = counter
		}
	}
	for node, counter := range right {
		if counter > merged[node] {
			merged[node] = counter
		}
	}
	return merged
}

// VersionVectorDominates reports whether every counter in expected is covered
// by candidate. Extra counters are allowed: they are a remote superset.
func VersionVectorDominates(candidate, expected VersionVector) bool {
	for node, counter := range expected {
		if candidate[node] < counter {
			return false
		}
	}
	return true
}

// VersionVectorsEqual reports mutual dominance.
func VersionVectorsEqual(left, right VersionVector) bool {
	return VersionVectorDominates(left, right) && VersionVectorDominates(right, left)
}

// VersionVectorHash is a stable hash of a vector.
func VersionVectorHash(vector VersionVector) string {
	data, _ := json.Marshal(vector)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CompareHLC orders hybrid logical clocks.
func CompareHLC(left, right HybridLogicalClock) int {
	switch {
	case left.WallTime < right.WallTime:
		return -1
	case left.WallTime > right.WallTime:
		return 1
	case left.Logical < right.Logical:
		return -1
	case left.Logical > right.Logical:
		return 1
	}
	return 0
}

// MaxHLC returns the later clock.
func MaxHLC(left, right HybridLogicalClock) HybridLogicalClock {
	if CompareHLC(left, right) >= 0 {
		return left
	}
	return right
}

// TickHLC advances the clock for one local write. now is a wall-clock sample
// supplied by the caller; the domain layer never reads the system clock, so
// replaying a state, node, batch, and timestamp produces identical bytes.
func TickHLC(current HybridLogicalClock, now int64) (HybridLogicalClock, error) {
	if now < 0 {
		now = 0
	}
	if now > current.WallTime {
		return HybridLogicalClock{WallTime: now, Logical: 0}, nil
	}
	if current.Logical == math.MaxUint64 {
		return HybridLogicalClock{}, fmt.Errorf("%w: hybrid logical clock counter exhausted", ErrConvergentInvalid)
	}
	return HybridLogicalClock{WallTime: current.WallTime, Logical: current.Logical + 1}, nil
}

// ---------------------------------------------------------------------------
// Canonical JSON
// ---------------------------------------------------------------------------

// CanonicalValue normalizes a JSON value the way the original's
// JSON.parse/JSON.stringify round-trip did: object keys are sorted and numbers
// use a shortest round-trip representation. Two replicas that hold the same
// logical value therefore hold the same bytes.
func CanonicalValue(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty JSON value", ErrConvergentInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConvergentInvalid, err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("%w: trailing JSON value", ErrConvergentInvalid)
	}
	var buffer bytes.Buffer
	if err := encodeCanonicalJSON(&buffer, value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func encodeCanonicalJSON(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		buffer.Write(encoded)
	case json.Number:
		number, err := canonicalNumber(string(typed))
		if err != nil {
			return err
		}
		buffer.WriteString(number)
	case []any:
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := encodeCanonicalJSON(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return err
			}
			buffer.Write(encodedKey)
			buffer.WriteByte(':')
			if err := encodeCanonicalJSON(buffer, typed[key]); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	default:
		return fmt.Errorf("%w: unsupported JSON value %T", ErrConvergentInvalid, value)
	}
	return nil
}

func canonicalNumber(literal string) (string, error) {
	number, err := strconv.ParseFloat(literal, 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
		return "", fmt.Errorf("%w: invalid JSON number %q", ErrConvergentInvalid, literal)
	}
	if number == 0 {
		return "0", nil
	}
	absolute := math.Abs(number)
	if absolute >= 1e-6 && absolute < 1e21 {
		return strconv.FormatFloat(number, 'f', -1, 64), nil
	}
	formatted := strconv.FormatFloat(number, 'e', -1, 64)
	// Go writes e-07; JavaScript writes e-7. Normalize the exponent's sign and
	// leading zeros for cross-client identity.
	if index := strings.IndexByte(formatted, 'e'); index >= 0 {
		mantissa := formatted[:index]
		exponent := formatted[index+1:]
		sign := "+"
		if strings.HasPrefix(exponent, "-") {
			sign = "-"
			exponent = exponent[1:]
		} else {
			exponent = strings.TrimPrefix(exponent, "+")
		}
		exponent = strings.TrimLeft(exponent, "0")
		if exponent == "" {
			exponent = "0"
		}
		formatted = mantissa + "e" + sign + exponent
	}
	return formatted, nil
}

// ---------------------------------------------------------------------------
// Registers
// ---------------------------------------------------------------------------

// IsTombstone reports whether a candidate is a deletion.
func (c RegisterCandidate) IsTombstone() bool { return c.Tombstone }

func cloneJSONValue(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func cloneDot(dot Dot) Dot { return Dot{Node: dot.Node, Counter: dot.Counter} }

func cloneCandidate(candidate RegisterCandidate) RegisterCandidate {
	clone := RegisterCandidate{
		Dot:       cloneDot(candidate.Dot),
		Context:   make([]Dot, len(candidate.Context)),
		HLC:       candidate.HLC,
		Tombstone: candidate.Tombstone,
		Value:     cloneJSONValue(candidate.Value),
	}
	for index, dot := range candidate.Context {
		clone.Context[index] = cloneDot(dot)
	}
	return clone
}

// SortDots sorts a dot set in total order.
func SortDots(dots []Dot) {
	sort.Slice(dots, func(left, right int) bool {
		return CompareDots(dots[left], dots[right]) < 0
	})
}

// RegisterContext returns every dot the register has observed: candidate dots
// and each candidate's exact context, sorted and deduplicated.
func RegisterContext(register MultiValueRegister) []Dot {
	seen := map[string]Dot{}
	for _, candidate := range register.Candidates {
		seen[DotKey(candidate.Dot)] = candidate.Dot
		for _, dot := range candidate.Context {
			seen[DotKey(dot)] = dot
		}
	}
	dots := make([]Dot, 0, len(seen))
	for _, dot := range seen {
		dots = append(dots, cloneDot(dot))
	}
	SortDots(dots)
	return dots
}

func containsDot(dots []Dot, dot Dot) bool {
	key := DotKey(dot)
	for _, candidate := range dots {
		if DotKey(candidate) == key {
			return true
		}
	}
	return false
}

func canonicalContext(context []Dot) string {
	dots := make([]Dot, len(context))
	for index, dot := range context {
		dots[index] = cloneDot(dot)
	}
	SortDots(dots)
	data, _ := json.Marshal(dots)
	return string(data)
}

// CandidateFingerprint is the identity of one candidate independent of slice
// ordering.
func CandidateFingerprint(candidate RegisterCandidate) string {
	value := ""
	if !candidate.Tombstone {
		value = string(candidate.Value)
	}
	return string(mustJSON(struct {
		Dot     Dot                `json:"dot"`
		Context string             `json:"context"`
		HLC     HybridLogicalClock `json:"hlc"`
		Tomb    bool               `json:"tombstone"`
		Value   string             `json:"value"`
	}{candidate.Dot, canonicalContext(candidate.Context), candidate.HLC, candidate.Tombstone, value}))
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// CompareRegisterCandidates is the deterministic total order used to choose a
// materialized winner: a value sorts after a tombstone, then HLC wall time and
// logical counter, then node id, then counter.
func CompareRegisterCandidates(left, right RegisterCandidate) int {
	if left.Tombstone != right.Tombstone {
		if left.Tombstone {
			return -1
		}
		return 1
	}
	if order := CompareHLC(left.HLC, right.HLC); order != 0 {
		return order
	}
	if left.Dot.Node < right.Dot.Node {
		return -1
	}
	if left.Dot.Node > right.Dot.Node {
		return 1
	}
	switch {
	case left.Dot.Counter < right.Dot.Counter:
		return -1
	case left.Dot.Counter > right.Dot.Counter:
		return 1
	}
	return 0
}

// SelectRegisterWinner returns the materialized candidate, if any.
func SelectRegisterWinner(register MultiValueRegister) (RegisterCandidate, bool) {
	if len(register.Candidates) == 0 {
		return RegisterCandidate{}, false
	}
	winner := register.Candidates[0]
	for _, candidate := range register.Candidates[1:] {
		if CompareRegisterCandidates(candidate, winner) > 0 {
			winner = candidate
		}
	}
	return cloneCandidate(winner), true
}

// RegisterHasConflict reports whether more than one distinct value is retained.
func RegisterHasConflict(register MultiValueRegister) bool {
	if len(register.Candidates) < 2 {
		return false
	}
	values := map[string]bool{}
	for _, candidate := range register.Candidates {
		key := "<tombstone>"
		if !candidate.Tombstone {
			key = string(candidate.Value)
		}
		values[key] = true
	}
	return len(values) > 1
}

// MergeRegisters joins two observed-remove multi-value registers.
//
// Rules, from the original:
//  1. candidates present on both sides are kept (their payloads must match);
//  2. a left-only candidate is kept when the right register has not observed
//     its dot;
//  3. a right-only candidate is kept when the left register has not observed
//     its dot;
//  4. candidates causally dominated by another surviving candidate are removed.
//
// The result is sorted by dot, so it is independent of argument order.
func MergeRegisters(left, right MultiValueRegister) (MultiValueRegister, error) {
	leftContext := RegisterContext(left)
	rightContext := RegisterContext(right)
	leftByDot := map[string]RegisterCandidate{}
	rightByDot := map[string]RegisterCandidate{}
	keys := map[string]bool{}
	for _, candidate := range left.Candidates {
		key := DotKey(candidate.Dot)
		if _, exists := leftByDot[key]; exists {
			return MultiValueRegister{}, fmt.Errorf("%w: duplicate dot %s in one register", ErrConvergentInvalid, key)
		}
		leftByDot[key] = candidate
		keys[key] = true
	}
	for _, candidate := range right.Candidates {
		key := DotKey(candidate.Dot)
		if _, exists := rightByDot[key]; exists {
			return MultiValueRegister{}, fmt.Errorf("%w: duplicate dot %s in one register", ErrConvergentInvalid, key)
		}
		rightByDot[key] = candidate
		keys[key] = true
	}

	sortedKeys := make([]string, 0, len(keys))
	for key := range keys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)

	candidates := make([]RegisterCandidate, 0, len(sortedKeys))
	for _, key := range sortedKeys {
		leftCandidate, inLeft := leftByDot[key]
		rightCandidate, inRight := rightByDot[key]
		switch {
		case inLeft && inRight:
			if CandidateFingerprint(leftCandidate) != CandidateFingerprint(rightCandidate) {
				return MultiValueRegister{}, fmt.Errorf("%w: dot %s", ErrConvergentDotConflict, key)
			}
			candidates = append(candidates, cloneCandidate(leftCandidate))
		case inLeft:
			if !containsDot(rightContext, leftCandidate.Dot) {
				candidates = append(candidates, cloneCandidate(leftCandidate))
			}
		case inRight:
			if !containsDot(leftContext, rightCandidate.Dot) {
				candidates = append(candidates, cloneCandidate(rightCandidate))
			}
		}
	}

	maximal := candidates[:0]
	for index, candidate := range candidates {
		dominated := false
		for otherIndex, other := range candidates {
			if index == otherIndex {
				continue
			}
			if containsDot(other.Context, candidate.Dot) {
				dominated = true
				break
			}
		}
		if !dominated {
			maximal = append(maximal, candidate)
		}
	}
	if len(maximal) == 0 {
		return MultiValueRegister{}, nil
	}
	sort.Slice(maximal, func(left, right int) bool {
		return CompareDots(maximal[left].Dot, maximal[right].Dot) < 0
	})
	return MultiValueRegister{Candidates: maximal}, nil
}

// ---------------------------------------------------------------------------
// Validation and canonical serialization
// ---------------------------------------------------------------------------

func validateVersionVector(vector VersionVector, label string) error {
	for node, counter := range vector {
		if node == "" {
			return fmt.Errorf("%w: %s has an empty node id", ErrConvergentInvalid, label)
		}
		if counter == 0 {
			return fmt.Errorf("%w: %s[%s] must be positive", ErrConvergentInvalid, label, node)
		}
	}
	return nil
}

func validateCandidate(candidate RegisterCandidate, vector VersionVector, stateClock HybridLogicalClock, label string) error {
	if candidate.Dot.Node == "" {
		return fmt.Errorf("%w: %s has an empty node id", ErrConvergentInvalid, label)
	}
	if candidate.Dot.Counter == 0 {
		return fmt.Errorf("%w: %s has a zero counter", ErrConvergentInvalid, label)
	}
	if vector[candidate.Dot.Node] < candidate.Dot.Counter {
		return fmt.Errorf("%w: %s dot is not covered by the state vector", ErrConvergentInvalid, label)
	}
	if CompareHLC(candidate.HLC, stateClock) > 0 {
		return fmt.Errorf("%w: %s clock exceeds the state clock", ErrConvergentInvalid, label)
	}
	contextKeys := map[string]bool{}
	for _, dot := range candidate.Context {
		if dot.Node == "" || dot.Counter == 0 {
			return fmt.Errorf("%w: %s context has an invalid dot", ErrConvergentInvalid, label)
		}
		if vector[dot.Node] < dot.Counter {
			return fmt.Errorf("%w: %s context dot is not covered by the state vector", ErrConvergentInvalid, label)
		}
		key := DotKey(dot)
		if contextKeys[key] {
			return fmt.Errorf("%w: %s context repeats dot %s", ErrConvergentInvalid, label, key)
		}
		contextKeys[key] = true
		if key == DotKey(candidate.Dot) {
			return fmt.Errorf("%w: %s context contains its own dot", ErrConvergentInvalid, label)
		}
		if dot.Node == candidate.Dot.Node && dot.Counter >= candidate.Dot.Counter {
			return fmt.Errorf("%w: %s context must precede its own device dot", ErrConvergentInvalid, label)
		}
	}
	if candidate.Tombstone {
		if len(candidate.Value) != 0 {
			return fmt.Errorf("%w: %s tombstone carries a value", ErrConvergentInvalid, label)
		}
		return nil
	}
	if len(candidate.Value) == 0 {
		return fmt.Errorf("%w: %s candidate has no value", ErrConvergentInvalid, label)
	}
	canonical, err := CanonicalValue(candidate.Value)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if !bytes.Equal(canonical, candidate.Value) {
		return fmt.Errorf("%w: %s value is not canonical JSON", ErrConvergentInvalid, label)
	}
	return nil
}

func validateRegister(register MultiValueRegister, vector VersionVector, clock HybridLogicalClock, label string) error {
	if len(register.Candidates) == 0 {
		return fmt.Errorf("%w: %s has no candidates", ErrConvergentInvalid, label)
	}
	seen := map[string]bool{}
	for index, candidate := range register.Candidates {
		key := DotKey(candidate.Dot)
		if seen[key] {
			return fmt.Errorf("%w: %s repeats dot %s", ErrConvergentInvalid, label, key)
		}
		seen[key] = true
		if err := validateCandidate(candidate, vector, clock, fmt.Sprintf("%s.candidates[%d]", label, index)); err != nil {
			return err
		}
	}
	// A retained candidate cannot be referenced by another candidate's
	// context: the observed-remove rules would have dropped it.
	for _, candidate := range register.Candidates {
		for _, dot := range candidate.Context {
			if seen[DotKey(dot)] {
				return fmt.Errorf("%w: %s context references retained candidate dot %s",
					ErrConvergentInvalid, label, DotKey(dot))
			}
		}
	}
	return nil
}

// DerivedEntityVersion computes the per-entity version vector from the retained
// candidate dots and contexts.
func DerivedEntityVersion(entity ConvergentEntity) VersionVector {
	vector := VersionVector{}
	observe := func(dot Dot) {
		if dot.Counter > vector[dot.Node] {
			vector[dot.Node] = dot.Counter
		}
	}
	registers := []MultiValueRegister{entity.Presence}
	for _, register := range entity.Fields {
		registers = append(registers, register)
	}
	for _, register := range registers {
		for _, candidate := range register.Candidates {
			observe(candidate.Dot)
			for _, dot := range candidate.Context {
				observe(dot)
			}
		}
	}
	return vector
}

// ValidateConvergentState checks every dot, context, origin, vector, HLC, and
// JSON invariant and fails closed.
func ValidateConvergentState(state ConvergentState) error {
	if state.SchemaVersion != ConvergentSchemaVersion {
		return fmt.Errorf("%w: schema %d", ErrConvergentSchema, state.SchemaVersion)
	}
	if state.HLC.WallTime < 0 {
		return fmt.Errorf("%w: negative state clock", ErrConvergentInvalid)
	}
	if err := validateVersionVector(state.Version, "version"); err != nil {
		return err
	}
	if state.Collections == nil {
		return fmt.Errorf("%w: collections must not be nil", ErrConvergentInvalid)
	}
	if state.DotOrigins == nil {
		return fmt.Errorf("%w: dotOrigins must not be nil", ErrConvergentInvalid)
	}
	for node, counters := range state.DotOrigins {
		if node == "" {
			return fmt.Errorf("%w: dotOrigins has an empty node id", ErrConvergentInvalid)
		}
		if len(counters) == 0 {
			return fmt.Errorf("%w: dotOrigins.%s is empty", ErrConvergentInvalid, node)
		}
		for counterText, identity := range counters {
			counter, err := strconv.ParseUint(counterText, 10, 64)
			if err != nil || counter == 0 {
				return fmt.Errorf("%w: dotOrigins.%s has invalid counter %q", ErrConvergentInvalid, node, counterText)
			}
			if state.Version[node] < counter {
				return fmt.Errorf("%w: dotOrigins.%s:%d is not covered by the vector", ErrConvergentInvalid, node, counter)
			}
			if identity == "" {
				return fmt.Errorf("%w: dotOrigins.%s:%d has no register identity", ErrConvergentInvalid, node, counter)
			}
		}
	}
	for node, counter := range state.Version {
		for value := uint64(1); value <= counter; value++ {
			if state.DotOrigins[node][strconv.FormatUint(value, 10)] == "" {
				return fmt.Errorf("%w: vector.%s=%d has no dot origin", ErrConvergentInvalid, node, counter)
			}
		}
	}

	dotOwners := map[string]string{}
	witnessed := map[string]map[uint64]bool{}
	maxClock := HybridLogicalClock{}
	witness := func(dot Dot) {
		if witnessed[dot.Node] == nil {
			witnessed[dot.Node] = map[uint64]bool{}
		}
		witnessed[dot.Node][dot.Counter] = true
	}
	checkOrigin := func(registerID string, dot Dot) error {
		origin := state.DotOrigins[dot.Node][strconv.FormatUint(dot.Counter, 10)]
		if origin == "" {
			return fmt.Errorf("%w: dot %s has no origin", ErrConvergentInvalid, DotKey(dot))
		}
		if origin != registerID {
			return fmt.Errorf("%w: dot %s belongs to %s, not %s",
				ErrConvergentInvalid, DotKey(dot), origin, registerID)
		}
		return nil
	}

	familyNames := make([]string, 0, len(state.Collections))
	for family := range state.Collections {
		familyNames = append(familyNames, family)
	}
	sort.Strings(familyNames)
	for _, family := range familyNames {
		if family == "" {
			return fmt.Errorf("%w: empty collection name", ErrConvergentInvalid)
		}
		collection := state.Collections[family]
		if collection.Entities == nil {
			return fmt.Errorf("%w: collection %s has nil entities", ErrConvergentInvalid, family)
		}
		entityIDs := make([]string, 0, len(collection.Entities))
		for id := range collection.Entities {
			entityIDs = append(entityIDs, id)
		}
		sort.Strings(entityIDs)
		for _, id := range entityIDs {
			if id == "" {
				return fmt.Errorf("%w: %s has an empty entity id", ErrConvergentInvalid, family)
			}
			entity := collection.Entities[id]
			label := family + "/" + id
			if err := validateRegister(entity.Presence, state.Version, state.HLC, label+".presence"); err != nil {
				return err
			}
			if entity.Fields == nil {
				return fmt.Errorf("%w: %s has nil fields", ErrConvergentInvalid, label)
			}
			fieldNames := make([]string, 0, len(entity.Fields))
			for field := range entity.Fields {
				fieldNames = append(fieldNames, field)
			}
			sort.Strings(fieldNames)
			for _, field := range fieldNames {
				if field == "" {
					return fmt.Errorf("%w: %s has an empty field name", ErrConvergentInvalid, label)
				}
				register := entity.Fields[field]
				if err := validateRegister(register, state.Version, state.HLC, label+"."+field); err != nil {
					return err
				}
				for _, candidate := range register.Candidates {
					registerID := RegisterIdentity(family, id, field)
					if err := checkOrigin(registerID, candidate.Dot); err != nil {
						return err
					}
					for _, dot := range candidate.Context {
						if err := checkOrigin(registerID, dot); err != nil {
							return err
						}
					}
					if CompareHLC(candidate.HLC, maxClock) > 0 {
						maxClock = candidate.HLC
					}
					ownerLabel := label + "." + field
					if previous, exists := dotOwners[DotKey(candidate.Dot)]; exists {
						return fmt.Errorf("%w: dot %s is claimed by %s and %s",
							ErrConvergentInvalid, DotKey(candidate.Dot), previous, ownerLabel)
					}
					dotOwners[DotKey(candidate.Dot)] = ownerLabel
					witness(candidate.Dot)
					for _, dot := range candidate.Context {
						witness(dot)
					}
				}
			}
			presenceID := RegisterIdentity(family, id, presenceField)
			for _, candidate := range entity.Presence.Candidates {
				if err := checkOrigin(presenceID, candidate.Dot); err != nil {
					return err
				}
				for _, dot := range candidate.Context {
					if err := checkOrigin(presenceID, dot); err != nil {
						return err
					}
				}
				if CompareHLC(candidate.HLC, maxClock) > 0 {
					maxClock = candidate.HLC
				}
				if previous, exists := dotOwners[DotKey(candidate.Dot)]; exists {
					return fmt.Errorf("%w: dot %s is claimed by %s and %s.presence",
						ErrConvergentInvalid, DotKey(candidate.Dot), previous, label)
				}
				dotOwners[DotKey(candidate.Dot)] = label + ".presence"
				witness(candidate.Dot)
				for _, dot := range candidate.Context {
					witness(dot)
				}
			}
			derived := DerivedEntityVersion(entity)
			if !VersionVectorsEqual(derived, entity.Version) {
				return fmt.Errorf("%w: %s per-entity version vector does not match its registers",
					ErrConvergentInvalid, label)
			}
		}
	}

	for node, counter := range state.Version {
		counters := witnessed[node]
		for value := uint64(1); value <= counter; value++ {
			if !counters[value] {
				return fmt.Errorf("%w: vector.%s=%d is not witnessed by a retained dot or context",
					ErrConvergentInvalid, node, counter)
			}
		}
	}
	if CompareHLC(state.HLC, maxClock) != 0 {
		return fmt.Errorf("%w: state clock does not match the latest retained write", ErrConvergentInvalid)
	}
	return nil
}

// NormalizeConvergentState returns a deep copy with canonical values and
// sorted candidate and context slices.
func NormalizeConvergentState(state ConvergentState) (ConvergentState, error) {
	if err := ValidateConvergentState(state); err != nil {
		return ConvergentState{}, err
	}
	clone := ConvergentState{
		SchemaVersion: ConvergentSchemaVersion,
		Version:       cloneVector(state.Version),
		HLC:           state.HLC,
		DotOrigins:    make(map[string]map[string]string, len(state.DotOrigins)),
		Collections:   make(map[string]ConvergentCollection, len(state.Collections)),
	}
	for node, counters := range state.DotOrigins {
		clonedCounters := make(map[string]string, len(counters))
		for counter, identity := range counters {
			clonedCounters[counter] = identity
		}
		clone.DotOrigins[node] = clonedCounters
	}
	for family, collection := range state.Collections {
		entities := make(map[string]ConvergentEntity, len(collection.Entities))
		for id, entity := range collection.Entities {
			fields := make(map[string]MultiValueRegister, len(entity.Fields))
			for field, register := range entity.Fields {
				fields[field] = normalizeRegister(register)
			}
			entities[id] = ConvergentEntity{
				Version:  DerivedEntityVersion(entity),
				Presence: normalizeRegister(entity.Presence),
				Fields:   fields,
			}
		}
		clone.Collections[family] = ConvergentCollection{Entities: entities}
	}
	return clone, nil
}

func cloneVector(vector VersionVector) VersionVector {
	clone := make(VersionVector, len(vector))
	for node, counter := range vector {
		clone[node] = counter
	}
	return clone
}

func normalizeRegister(register MultiValueRegister) MultiValueRegister {
	candidates := make([]RegisterCandidate, len(register.Candidates))
	for index, candidate := range register.Candidates {
		clone := cloneCandidate(candidate)
		SortDots(clone.Context)
		if !clone.Tombstone {
			// Validation guaranteed canonical JSON.
			clone.Value = append(json.RawMessage(nil), clone.Value...)
		}
		candidates[index] = clone
	}
	sort.Slice(candidates, func(left, right int) bool {
		return CompareDots(candidates[left].Dot, candidates[right].Dot) < 0
	})
	return MultiValueRegister{Candidates: candidates}
}

// CanonicalConvergentBytes returns the deterministic serialization of a state.
// Replicas that have seen the same set of operations produce identical bytes
// regardless of delivery order or duplication.
func CanonicalConvergentBytes(state ConvergentState) ([]byte, error) {
	normalized, err := NormalizeConvergentState(state)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

// ConvergentStateHash is the SHA-256 of the canonical state bytes.
func ConvergentStateHash(state ConvergentState) (string, error) {
	data, err := CanonicalConvergentBytes(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ConvergentEntityHash is the SHA-256 of one canonical entity.
func ConvergentEntityHash(entity ConvergentEntity) (string, error) {
	normalized := ConvergentEntity{
		Version:  DerivedEntityVersion(entity),
		Presence: normalizeRegister(entity.Presence),
		Fields:   map[string]MultiValueRegister{},
	}
	for field, register := range entity.Fields {
		normalized.Fields[field] = normalizeRegister(register)
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ConvergentManifest is the round-one comparison: a global vector plus one
// version vector per entity.
type ConvergentManifest struct {
	Version  VersionVector            `json:"version"`
	Entities map[string]VersionVector `json:"entities"`
}

// ManifestKey is the stable key of an entity in a manifest.
func ManifestKey(family, id string) string { return family + "\x00" + id }

// BuildConvergentManifest extracts the manifest.
func BuildConvergentManifest(state ConvergentState) ConvergentManifest {
	manifest := ConvergentManifest{
		Version:  cloneVector(state.Version),
		Entities: map[string]VersionVector{},
	}
	for family, collection := range state.Collections {
		for id, entity := range collection.Entities {
			manifest.Entities[ManifestKey(family, id)] = DerivedEntityVersion(entity)
		}
	}
	return manifest
}

// ManifestHash is a stable hash of the manifest.
func ManifestHash(manifest ConvergentManifest) string {
	data, _ := json.Marshal(manifest)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ManifestsEqual compares every global and per-entity vector.
func ManifestsEqual(left, right ConvergentManifest) bool {
	if !VersionVectorsEqual(left.Version, right.Version) {
		return false
	}
	if len(left.Entities) != len(right.Entities) {
		return false
	}
	for key, vector := range left.Entities {
		other, exists := right.Entities[key]
		if !exists || !VersionVectorsEqual(vector, other) {
			return false
		}
	}
	return true
}

// ManifestDominates reports whether every global and per-entity vector in
// expected is covered by candidate.
func ManifestDominates(candidate, expected ConvergentManifest) bool {
	if !VersionVectorDominates(candidate.Version, expected.Version) {
		return false
	}
	for key, vector := range expected.Entities {
		if !VersionVectorDominates(candidate.Entities[key], vector) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

// MergeConvergentStates joins two states. It is commutative, associative, and
// idempotent; conflicting payloads for one dot fail closed.
func MergeConvergentStates(left, right ConvergentState) (ConvergentState, error) {
	if err := ValidateConvergentState(left); err != nil {
		return ConvergentState{}, err
	}
	if err := ValidateConvergentState(right); err != nil {
		return ConvergentState{}, err
	}
	merged := ConvergentState{
		SchemaVersion: ConvergentSchemaVersion,
		Version:       MergeVersionVectors(left.Version, right.Version),
		HLC:           MaxHLC(left.HLC, right.HLC),
		DotOrigins:    map[string]map[string]string{},
		Collections:   map[string]ConvergentCollection{},
	}
	for node, counters := range left.DotOrigins {
		target := make(map[string]string, len(counters))
		for counter, identity := range counters {
			target[counter] = identity
		}
		merged.DotOrigins[node] = target
	}
	for node, counters := range right.DotOrigins {
		target := merged.DotOrigins[node]
		if target == nil {
			target = map[string]string{}
			merged.DotOrigins[node] = target
		}
		for counter, identity := range counters {
			if existing, exists := target[counter]; exists && existing != identity {
				return ConvergentState{}, fmt.Errorf("%w: dot %s:%s belongs to %s and %s",
					ErrConvergentInvalid, node, counter, existing, identity)
			}
			target[counter] = identity
		}
	}
	families := map[string]bool{}
	for family := range left.Collections {
		families[family] = true
	}
	for family := range right.Collections {
		families[family] = true
	}
	for family := range families {
		leftCollection := left.Collections[family]
		rightCollection := right.Collections[family]
		entityIDs := map[string]bool{}
		for id := range leftCollection.Entities {
			entityIDs[id] = true
		}
		for id := range rightCollection.Entities {
			entityIDs[id] = true
		}
		entities := make(map[string]ConvergentEntity, len(entityIDs))
		for id := range entityIDs {
			leftEntity, inLeft := leftCollection.Entities[id]
			rightEntity, inRight := rightCollection.Entities[id]
			var entity ConvergentEntity
			switch {
			case inLeft && inRight:
				presence, err := MergeRegisters(leftEntity.Presence, rightEntity.Presence)
				if err != nil {
					return ConvergentState{}, fmt.Errorf("%s/%s presence: %w", family, id, err)
				}
				fields := map[string]MultiValueRegister{}
				for field, register := range leftEntity.Fields {
					fields[field] = register
				}
				for field, rightRegister := range rightEntity.Fields {
					leftRegister, exists := fields[field]
					if !exists {
						fields[field] = rightRegister
						continue
					}
					register, err := MergeRegisters(leftRegister, rightRegister)
					if err != nil {
						return ConvergentState{}, fmt.Errorf("%s/%s.%s: %w", family, id, field, err)
					}
					fields[field] = register
				}
				entity = ConvergentEntity{Presence: presence, Fields: fields}
			case inLeft:
				entity = cloneEntity(leftEntity)
			default:
				entity = cloneEntity(rightEntity)
			}
			entity.Version = DerivedEntityVersion(entity)
			entities[id] = entity
		}
		merged.Collections[family] = ConvergentCollection{Entities: entities}
	}
	if err := ValidateConvergentState(merged); err != nil {
		return ConvergentState{}, err
	}
	return merged, nil
}

func cloneEntity(entity ConvergentEntity) ConvergentEntity {
	fields := make(map[string]MultiValueRegister, len(entity.Fields))
	for field, register := range entity.Fields {
		fields[field] = normalizeRegister(register)
	}
	return ConvergentEntity{
		Version:  DerivedEntityVersion(entity),
		Presence: normalizeRegister(entity.Presence),
		Fields:   fields,
	}
}

// ---------------------------------------------------------------------------
// Divergence
// ---------------------------------------------------------------------------

func registerFor(entity ConvergentEntity, field string) (MultiValueRegister, bool) {
	switch field {
	case "<presence>":
		return entity.Presence, true
	default:
		register, exists := entity.Fields[field]
		return register, exists
	}
}

func candidateByDot(register MultiValueRegister) map[string]RegisterCandidate {
	candidates := make(map[string]RegisterCandidate, len(register.Candidates))
	for _, candidate := range register.Candidates {
		candidates[DotKey(candidate.Dot)] = candidate
	}
	return candidates
}

// DetectDivergence compares two replicas and returns a genuine divergence, or
// nil for ordinary concurrency. Concurrency is not divergence: neither side
// dominates and the join resolves it. Divergence means the causal metadata
// says the replicas have seen the same writes but their bytes differ, or a
// dominating replica is missing a dot the dominated one claims.
func DetectDivergence(local, remote ConvergentState) *ConvergentDivergence {
	if err := ValidateConvergentState(local); err != nil {
		return &ConvergentDivergence{Kind: "invalid-local", Detail: err.Error()}
	}
	if err := ValidateConvergentState(remote); err != nil {
		return &ConvergentDivergence{Kind: "invalid-remote", Detail: err.Error()}
	}
	localHash, _ := ConvergentStateHash(local)
	remoteHash, _ := ConvergentStateHash(remote)

	// The permanent dot-origin index must agree for every dot both replicas
	// claim to have allocated.
	originNodes := make([]string, 0, len(local.DotOrigins))
	for node := range local.DotOrigins {
		originNodes = append(originNodes, node)
	}
	sort.Strings(originNodes)
	for _, node := range originNodes {
		counters := make([]string, 0, len(local.DotOrigins[node]))
		for counter := range local.DotOrigins[node] {
			counters = append(counters, counter)
		}
		sort.Strings(counters)
		for _, counter := range counters {
			remoteIdentity, exists := remote.DotOrigins[node][counter]
			if !exists || remoteIdentity == local.DotOrigins[node][counter] {
				continue
			}
			return &ConvergentDivergence{
				Kind:         "origin-mismatch",
				Dot:          node + ":" + counter,
				LocalHash:    localHash,
				RemoteHash:   remoteHash,
				LocalVector:  cloneVector(local.Version),
				RemoteVector: cloneVector(remote.Version),
				Detail:       "one dot is owned by different registers: " + local.DotOrigins[node][counter] + " and " + remoteIdentity,
			}
		}
	}

	familyIDs := map[string]bool{}
	for family := range local.Collections {
		familyIDs[family] = true
	}
	for family := range remote.Collections {
		familyIDs[family] = true
	}
	families := make([]string, 0, len(familyIDs))
	for family := range familyIDs {
		families = append(families, family)
	}
	sort.Strings(families)

	// Same dot, different payload, is always a protocol violation.
	for _, family := range families {
		entityIDs := map[string]bool{}
		for id := range local.Collections[family].Entities {
			entityIDs[id] = true
		}
		for id := range remote.Collections[family].Entities {
			entityIDs[id] = true
		}
		ids := make([]string, 0, len(entityIDs))
		for id := range entityIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			localEntity, inLocal := local.Collections[family].Entities[id]
			remoteEntity, inRemote := remote.Collections[family].Entities[id]
			if !inLocal || !inRemote {
				continue
			}
			fields := map[string]bool{"<presence>": true}
			for field := range localEntity.Fields {
				fields[field] = true
			}
			for field := range remoteEntity.Fields {
				fields[field] = true
			}
			fieldNames := make([]string, 0, len(fields))
			for field := range fields {
				fieldNames = append(fieldNames, field)
			}
			sort.Strings(fieldNames)
			for _, field := range fieldNames {
				localRegister, hasLocal := registerFor(localEntity, field)
				remoteRegister, hasRemote := registerFor(remoteEntity, field)
				if !hasLocal || !hasRemote {
					continue
				}
				localCandidates := candidateByDot(localRegister)
				remoteCandidates := candidateByDot(remoteRegister)
				for key, localCandidate := range localCandidates {
					remoteCandidate, exists := remoteCandidates[key]
					if !exists {
						continue
					}
					if CandidateFingerprint(localCandidate) != CandidateFingerprint(remoteCandidate) {
						displayField := field
						if field == "<presence>" {
							displayField = ""
						}
						return &ConvergentDivergence{
							Kind:         "dot-payload-mismatch",
							Family:       family,
							EntityID:     id,
							Dot:          key,
							LocalHash:    localHash,
							RemoteHash:   remoteHash,
							LocalVector:  cloneVector(local.Version),
							RemoteVector: cloneVector(remote.Version),
							Detail:       "one dot carries different data in " + displayField,
						}
					}
				}
			}
			// Equal entity versions must imply equal entity bytes.
			if VersionVectorsEqual(DerivedEntityVersion(localEntity), DerivedEntityVersion(remoteEntity)) {
				localEntityHash, _ := ConvergentEntityHash(localEntity)
				remoteEntityHash, _ := ConvergentEntityHash(remoteEntity)
				if localEntityHash != remoteEntityHash {
					return &ConvergentDivergence{
						Kind:         "entity-hash-mismatch",
						Family:       family,
						EntityID:     id,
						LocalHash:    localEntityHash,
						RemoteHash:   remoteEntityHash,
						LocalVector:  cloneVector(local.Version),
						RemoteVector: cloneVector(remote.Version),
						Detail:       "the same entity version has different state bytes",
					}
				}
			}
		}
	}

	// A dominating vector must retain every candidate of the dominated side.
	localDominates := VersionVectorDominates(local.Version, remote.Version)
	remoteDominates := VersionVectorDominates(remote.Version, local.Version)
	checkDominated := func(dominant, dominated ConvergentState, direction string) *ConvergentDivergence {
		for _, family := range families {
			dominantEntities := dominant.Collections[family].Entities
			dominatedEntities := dominated.Collections[family].Entities
			ids := make([]string, 0, len(dominatedEntities))
			for id := range dominatedEntities {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				dominatedEntity := dominatedEntities[id]
				dominantEntity, exists := dominantEntities[id]
				if !exists {
					dot := ""
					if len(dominatedEntity.Presence.Candidates) > 0 {
						dot = DotKey(dominatedEntity.Presence.Candidates[0].Dot)
					}
					return &ConvergentDivergence{
						Kind:         "causal-overwrite",
						Family:       family,
						EntityID:     id,
						Dot:          dot,
						LocalHash:    localHash,
						RemoteHash:   remoteHash,
						LocalVector:  cloneVector(local.Version),
						RemoteVector: cloneVector(remote.Version),
						Detail:       direction + " replica dropped an entity its vector says it observed",
					}
				}
				fields := map[string]bool{"<presence>": true}
				for field := range dominatedEntity.Fields {
					fields[field] = true
				}
				fieldNames := make([]string, 0, len(fields))
				for field := range fields {
					fieldNames = append(fieldNames, field)
				}
				sort.Strings(fieldNames)
				for _, field := range fieldNames {
					dominatedRegister, hasDominated := registerFor(dominatedEntity, field)
					if !hasDominated {
						continue
					}
					dominantRegister, _ := registerFor(dominantEntity, field)
					dominantCandidates := candidateByDot(dominantRegister)
					dominantContext := RegisterContext(dominantRegister)
					for _, candidate := range dominatedRegister.Candidates {
						key := DotKey(candidate.Dot)
						dominantCandidate, retained := dominantCandidates[key]
						if !retained {
							// A causal write compacts the register but keeps the
							// observed dot in its exact context. A dot in that
							// context was legitimately superseded; a dot in
							// neither place was lost.
							if containsDot(dominantContext, candidate.Dot) {
								continue
							}
							return &ConvergentDivergence{
								Kind:         "causal-overwrite",
								Family:       family,
								EntityID:     id,
								Dot:          key,
								LocalHash:    localHash,
								RemoteHash:   remoteHash,
								LocalVector:  cloneVector(local.Version),
								RemoteVector: cloneVector(remote.Version),
								Detail:       direction + " replica dropped an observed candidate",
							}
						}
						if CandidateFingerprint(candidate) != CandidateFingerprint(dominantCandidate) {
							return &ConvergentDivergence{
								Kind:         "dot-payload-mismatch",
								Family:       family,
								EntityID:     id,
								Dot:          key,
								LocalHash:    localHash,
								RemoteHash:   remoteHash,
								LocalVector:  cloneVector(local.Version),
								RemoteVector: cloneVector(remote.Version),
								Detail:       direction + " replica changed the payload of an observed dot",
							}
						}
					}
				}
			}
		}
		return nil
	}
	if localDominates {
		if divergence := checkDominated(local, remote, "the local"); divergence != nil {
			return divergence
		}
	}
	if remoteDominates {
		if divergence := checkDominated(remote, local, "the remote"); divergence != nil {
			return divergence
		}
	}

	// Genuine divergence: the same causal history but different bytes. A
	// dominating state with extra candidates is concurrency/forward progress,
	// not divergence, and is checked above for lost candidates.
	if !localDominates && !remoteDominates {
		return nil
	}
	if VersionVectorsEqual(local.Version, remote.Version) && localHash != remoteHash {
		return &ConvergentDivergence{
			Kind:         "equal-vector-different-state",
			LocalHash:    localHash,
			RemoteHash:   remoteHash,
			LocalVector:  cloneVector(local.Version),
			RemoteVector: cloneVector(remote.Version),
			Detail:       "both replicas claim the same version vector but serialize differently",
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Snapshots, mutation, and materialization
// ---------------------------------------------------------------------------

// DefaultConvergentFamilies is the v1 payload family list and each family's
// record key. It matches the application's Payload JSON names and the merge
// engine's key choices.
func DefaultConvergentFamilies() []ConvergentFamily {
	return []ConvergentFamily{
		{Name: "hosts", KeyFields: []string{"id"}},
		{Name: "keys", KeyFields: []string{"id"}},
		{Name: "identities", KeyFields: []string{"id"}},
		{Name: "proxyProfiles", KeyFields: []string{"id"}},
		{Name: "snippets", KeyFields: []string{"id"}},
		{Name: "groups", KeyFields: []string{"path"}},
		{Name: "snippetPackages", KeyFields: []string{"path"}},
		{Name: "notes", KeyFields: []string{"id"}},
		{Name: "noteGroups", KeyFields: []string{"path"}},
		{Name: "portForwardingRules", KeyFields: []string{"id"}},
		{Name: "secrets", KeyFields: []string{"entityType", "entityId", "field"}},
	}
}

func recordKey(value map[string]json.RawMessage, fields []string) (string, error) {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		raw, exists := value[field]
		if !exists {
			return "", fmt.Errorf("%w: record has no %q key field", ErrConvergentInvalid, field)
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", fmt.Errorf("%w: record key %q is not a string", ErrConvergentInvalid, field)
		}
		if text == "" {
			return "", fmt.Errorf("%w: record key %q is empty", ErrConvergentInvalid, field)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\x1f"), nil
}

// ParseConvergentSnapshot turns the payload's JSON families into records. A
// missing or null family is an empty family; an explicit empty array is also
// empty. Unknown top-level fields are ignored here so the application can keep
// settings and metadata.
func ParseConvergentSnapshot(raw map[string]json.RawMessage, families []ConvergentFamily) (ConvergentSnapshot, error) {
	snapshot := ConvergentSnapshot{Families: map[string][]ConvergentRecord{}}
	for _, family := range families {
		value, exists := raw[family.Name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			snapshot.Families[family.Name] = nil
			continue
		}
		var records []json.RawMessage
		if err := json.Unmarshal(value, &records); err != nil {
			return ConvergentSnapshot{}, fmt.Errorf("%w: %s must be an array: %v", ErrConvergentInvalid, family.Name, err)
		}
		out := make([]ConvergentRecord, 0, len(records))
		for _, recordRaw := range records {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(recordRaw, &object); err != nil {
				return ConvergentSnapshot{}, fmt.Errorf("%w: %s record must be an object: %v", ErrConvergentInvalid, family.Name, err)
			}
			key, err := recordKey(object, family.KeyFields)
			if err != nil {
				return ConvergentSnapshot{}, fmt.Errorf("%s: %w", family.Name, err)
			}
			canonical, err := CanonicalValue(recordRaw)
			if err != nil {
				return ConvergentSnapshot{}, fmt.Errorf("%s/%s: %w", family.Name, key, err)
			}
			out = append(out, ConvergentRecord{ID: key, Value: canonical})
		}
		sort.Slice(out, func(left, right int) bool { return out[left].ID < out[right].ID })
		for index := 1; index < len(out); index++ {
			if out[index].ID == out[index-1].ID {
				return ConvergentSnapshot{}, fmt.Errorf("%w: %s has duplicate record %q", ErrConvergentInvalid, family.Name, out[index].ID)
			}
		}
		snapshot.Families[family.Name] = out
	}
	return snapshot, nil
}

// RawSnapshot re-serializes a snapshot as the payload family object.
func RawSnapshot(snapshot ConvergentSnapshot) (map[string]json.RawMessage, error) {
	raw := map[string]json.RawMessage{}
	for family, records := range snapshot.Families {
		values := make([]json.RawMessage, len(records))
		for index, record := range records {
			values[index] = record.Value
		}
		data, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		raw[family] = data
	}
	return raw, nil
}

func entityFieldsFromValue(value json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, fmt.Errorf("%w: entity value must be an object: %v", ErrConvergentInvalid, err)
	}
	fields := make(map[string]json.RawMessage, len(object))
	for field, raw := range object {
		canonical, err := CanonicalValue(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: field %q: %v", ErrConvergentInvalid, field, err)
		}
		fields[field] = canonical
	}
	return fields, nil
}

type stateWriter struct {
	state ConvergentState
	node  string
	now   int64
}

func (w *stateWriter) allocate(registerID string) (Dot, HybridLogicalClock, error) {
	if w.node == "" {
		return Dot{}, HybridLogicalClock{}, fmt.Errorf("%w: device id is empty", ErrConvergentInvalid)
	}
	if registerID == "" {
		return Dot{}, HybridLogicalClock{}, fmt.Errorf("%w: register identity is empty", ErrConvergentInvalid)
	}
	counter := w.state.Version[w.node] + 1
	if counter == 0 {
		return Dot{}, HybridLogicalClock{}, fmt.Errorf("%w: device counter exhausted", ErrConvergentInvalid)
	}
	clock, err := TickHLC(w.state.HLC, w.now)
	if err != nil {
		return Dot{}, HybridLogicalClock{}, err
	}
	w.state.Version[w.node] = counter
	w.state.HLC = clock
	if w.state.DotOrigins == nil {
		w.state.DotOrigins = map[string]map[string]string{}
	}
	if w.state.DotOrigins[w.node] == nil {
		w.state.DotOrigins[w.node] = map[string]string{}
	}
	w.state.DotOrigins[w.node][strconv.FormatUint(counter, 10)] = registerID
	return Dot{Node: w.node, Counter: counter}, clock, nil
}

func (w *stateWriter) write(registerID string, register MultiValueRegister, value json.RawMessage, tombstone bool) (MultiValueRegister, error) {
	dot, clock, err := w.allocate(registerID)
	if err != nil {
		return MultiValueRegister{}, err
	}
	candidate := RegisterCandidate{
		Dot:       dot,
		Context:   RegisterContext(register),
		HLC:       clock,
		Tombstone: tombstone,
	}
	if !tombstone {
		canonical, err := CanonicalValue(value)
		if err != nil {
			return MultiValueRegister{}, err
		}
		candidate.Value = canonical
	}
	// The new candidate causally dominates every prior candidate in the
	// register, so it is the only retained candidate. Retaining a single
	// candidate keeps the register compact; the context preserves the causal
	// history needed by later joins.
	return MultiValueRegister{Candidates: []RegisterCandidate{candidate}}, nil
}

func ensureCollection(state ConvergentState, family string) ConvergentCollection {
	collection, exists := state.Collections[family]
	if !exists {
		collection = ConvergentCollection{Entities: map[string]ConvergentEntity{}}
		state.Collections[family] = collection
	}
	if collection.Entities == nil {
		collection.Entities = map[string]ConvergentEntity{}
	}
	return collection
}

func getEntity(state ConvergentState, family, id string) (ConvergentEntity, bool) {
	collection, exists := state.Collections[family]
	if !exists {
		return ConvergentEntity{}, false
	}
	entity, exists := collection.Entities[id]
	return entity, exists
}

func setEntity(state ConvergentState, family, id string, entity ConvergentEntity) {
	collection := ensureCollection(state, family)
	collection.Entities[id] = entity
	state.Collections[family] = collection
}

const presenceTrue = `true`

// CreateConvergentStateFromSnapshot bootstraps a v2 lineage from a materialized
// v1 snapshot. Every record, presence, and field receives a fresh dot from the
// node that performs the migration.
func CreateConvergentStateFromSnapshot(snapshot ConvergentSnapshot, node string, now int64) (ConvergentState, error) {
	state := EmptyConvergentState()
	writer := &stateWriter{state: state, node: node, now: now}
	families := make([]string, 0, len(snapshot.Families))
	for family := range snapshot.Families {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		ensureCollection(state, family)
		records := append([]ConvergentRecord(nil), snapshot.Families[family]...)
		sort.Slice(records, func(left, right int) bool { return records[left].ID < records[right].ID })
		for _, record := range records {
			fields, err := entityFieldsFromValue(record.Value)
			if err != nil {
				return ConvergentState{}, err
			}
			presence, err := writer.write(RegisterIdentity(family, record.ID, presenceField), MultiValueRegister{}, json.RawMessage(presenceTrue), false)
			if err != nil {
				return ConvergentState{}, err
			}
			entity := ConvergentEntity{Presence: presence, Fields: map[string]MultiValueRegister{}}
			fieldNames := make([]string, 0, len(fields))
			for field := range fields {
				fieldNames = append(fieldNames, field)
			}
			sort.Strings(fieldNames)
			for _, field := range fieldNames {
				register, err := writer.write(RegisterIdentity(family, record.ID, field), MultiValueRegister{}, fields[field], false)
				if err != nil {
					return ConvergentState{}, err
				}
				entity.Fields[field] = register
			}
			entity.Version = DerivedEntityVersion(entity)
			setEntity(state, family, record.ID, entity)
		}
	}
	state = writer.state
	if err := ValidateConvergentState(state); err != nil {
		return ConvergentState{}, err
	}
	return state, nil
}

func visibleEntity(state ConvergentState, family, id string) (ConvergentEntity, bool) {
	entity, exists := getEntity(state, family, id)
	if !exists {
		return ConvergentEntity{}, false
	}
	winner, hasWinner := SelectRegisterWinner(entity.Presence)
	return entity, hasWinner && !winner.Tombstone
}

func registerWinnerValue(register MultiValueRegister) (json.RawMessage, bool) {
	winner, exists := SelectRegisterWinner(register)
	if !exists || winner.Tombstone {
		return nil, false
	}
	return winner.Value, true
}

// ApplyConvergentSnapshot turns the difference between the materialized CRDT
// state and a local payload snapshot into causal writes. force writes every
// field even when it is unchanged, so preferLocal dominates a joined state.
// It returns the new state and whether any write was allocated.
func ApplyConvergentSnapshot(state ConvergentState, snapshot ConvergentSnapshot, node string, now int64, force bool) (ConvergentState, bool, error) {
	if err := ValidateConvergentState(state); err != nil {
		return ConvergentState{}, false, err
	}
	clone, err := NormalizeConvergentState(state)
	if err != nil {
		return ConvergentState{}, false, err
	}
	writer := &stateWriter{state: clone, node: node, now: now}
	changed := false

	families := make([]string, 0, len(snapshot.Families))
	for family := range snapshot.Families {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		incoming := map[string]json.RawMessage{}
		for _, record := range snapshot.Families[family] {
			incoming[record.ID] = record.Value
		}
		entityIDs := map[string]bool{}
		for id := range incoming {
			entityIDs[id] = true
		}
		collection, exists := clone.Collections[family]
		if exists {
			for id, entity := range collection.Entities {
				if winner, ok := SelectRegisterWinner(entity.Presence); ok && !winner.Tombstone {
					entityIDs[id] = true
				}
			}
		}
		ids := make([]string, 0, len(entityIDs))
		for id := range entityIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			rawValue, inIncoming := incoming[id]
			entity, entityExists := getEntity(clone, family, id)
			_, visibleExists := visibleEntity(clone, family, id)
			presenceID := RegisterIdentity(family, id, presenceField)
			if !inIncoming {
				if visibleExists {
					presence, err := writer.write(presenceID, entity.Presence, nil, true)
					if err != nil {
						return ConvergentState{}, false, err
					}
					entity.Presence = presence
					entity.Version = DerivedEntityVersion(entity)
					setEntity(clone, family, id, entity)
					changed = true
				}
				continue
			}
			incomingFields, err := entityFieldsFromValue(rawValue)
			if err != nil {
				return ConvergentState{}, false, err
			}
			if !entityExists {
				entity = ConvergentEntity{Fields: map[string]MultiValueRegister{}}
			}
			// Presence: create, resurrect a tombstone, or force an existing
			// conflicting presence. An ordinary visible presence is unchanged.
			presenceWinner, hasPresence := SelectRegisterWinner(entity.Presence)
			needsPresence := !hasPresence || presenceWinner.Tombstone || force
			if needsPresence {
				presence, err := writer.write(presenceID, entity.Presence, json.RawMessage(presenceTrue), false)
				if err != nil {
					return ConvergentState{}, false, err
				}
				entity.Presence = presence
				changed = true
			}
			fieldNames := map[string]bool{}
			for field := range incomingFields {
				fieldNames[field] = true
			}
			for field := range entity.Fields {
				fieldNames[field] = true
			}
			names := make([]string, 0, len(fieldNames))
			for field := range fieldNames {
				names = append(names, field)
			}
			sort.Strings(names)
			for _, field := range names {
				incomingValue, hasIncoming := incomingFields[field]
				register := entity.Fields[field]
				currentValue, hasCurrent := registerWinnerValue(register)
				fieldID := RegisterIdentity(family, id, field)
				switch {
				case hasIncoming && (!hasCurrent || !bytes.Equal(currentValue, incomingValue) ||
					(force && RegisterHasConflict(register)) || (force && hasCurrent)):
					written, err := writer.write(fieldID, register, incomingValue, false)
					if err != nil {
						return ConvergentState{}, false, err
					}
					entity.Fields[field] = written
					changed = true
				case !hasIncoming && hasCurrent:
					written, err := writer.write(fieldID, register, nil, true)
					if err != nil {
						return ConvergentState{}, false, err
					}
					entity.Fields[field] = written
					changed = true
				}
			}
			entity.Version = DerivedEntityVersion(entity)
			setEntity(clone, family, id, entity)
		}
	}

	clone = writer.state
	if !changed {
		return clone, false, nil
	}
	if err := ValidateConvergentState(clone); err != nil {
		return ConvergentState{}, false, err
	}
	return clone, true, nil
}

// MaterializeConvergentSnapshot selects the deterministic winner view.
func MaterializeConvergentSnapshot(state ConvergentState) (ConvergentSnapshot, error) {
	if err := ValidateConvergentState(state); err != nil {
		return ConvergentSnapshot{}, err
	}
	snapshot := ConvergentSnapshot{Families: map[string][]ConvergentRecord{}}
	families := make([]string, 0, len(state.Collections))
	for family := range state.Collections {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		collection := state.Collections[family]
		records := make([]ConvergentRecord, 0, len(collection.Entities))
		ids := make([]string, 0, len(collection.Entities))
		for id := range collection.Entities {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			entity := collection.Entities[id]
			winner, exists := SelectRegisterWinner(entity.Presence)
			if !exists || winner.Tombstone {
				continue
			}
			fields := map[string]json.RawMessage{}
			fieldNames := make([]string, 0, len(entity.Fields))
			for field := range entity.Fields {
				fieldNames = append(fieldNames, field)
			}
			sort.Strings(fieldNames)
			for _, field := range fieldNames {
				value, present := registerWinnerValue(entity.Fields[field])
				if present {
					fields[field] = value
				}
			}
			value, err := json.Marshal(fields)
			if err != nil {
				return ConvergentSnapshot{}, err
			}
			records = append(records, ConvergentRecord{ID: id, Value: value})
		}
		snapshot.Families[family] = records
	}
	return snapshot, nil
}

// ImportConvergentDeletions turns legacy v1 syncMeta tombstones into permanent
// v2 presence tombstones. Without this step a migration drops the deleted
// entity's name entirely and a stale v1 replica can resurrect it. Deletions
// are sorted, duplicate family/id pairs keep the latest timestamp, and an
// already-tombstoned entity is left untouched, so importing the same list
// twice produces the same state.
func ImportConvergentDeletions(state ConvergentState, deletions []ConvergentDeletion, node string, now int64) (ConvergentState, bool, error) {
	if err := ValidateConvergentState(state); err != nil {
		return ConvergentState{}, false, err
	}
	if node == "" {
		return ConvergentState{}, false, fmt.Errorf("%w: device id is empty", ErrConvergentInvalid)
	}
	clone, err := NormalizeConvergentState(state)
	if err != nil {
		return ConvergentState{}, false, err
	}
	type deletionKey struct{ family, id string }
	latest := map[deletionKey]int64{}
	for _, deletion := range deletions {
		if deletion.Family == "" || deletion.ID == "" {
			return ConvergentState{}, false, fmt.Errorf("%w: deletion has an empty family or id", ErrConvergentInvalid)
		}
		key := deletionKey{deletion.Family, deletion.ID}
		if at, exists := latest[key]; !exists || deletion.DeletedAt > at {
			latest[key] = deletion.DeletedAt
		}
	}
	keys := make([]deletionKey, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].family != keys[right].family {
			return keys[left].family < keys[right].family
		}
		return keys[left].id < keys[right].id
	})
	writer := &stateWriter{state: clone, node: node, now: now}
	changed := false
	for _, key := range keys {
		entity, exists := getEntity(clone, key.family, key.id)
		if exists {
			if winner, ok := SelectRegisterWinner(entity.Presence); ok && winner.Tombstone {
				continue
			}
		} else {
			entity = ConvergentEntity{Fields: map[string]MultiValueRegister{}}
		}
		registerID := RegisterIdentity(key.family, key.id, presenceField)
		dot, _, err := writer.allocate(registerID)
		if err != nil {
			return ConvergentState{}, false, err
		}
		at := latest[key]
		if at < 0 {
			at = 0
		}
		if at == 0 {
			at = now
		}
		clock := HybridLogicalClock{WallTime: at}
		candidate := RegisterCandidate{
			Dot:       dot,
			Context:   RegisterContext(entity.Presence),
			HLC:       clock,
			Tombstone: true,
		}
		entity.Presence = MultiValueRegister{Candidates: []RegisterCandidate{candidate}}
		entity.Version = DerivedEntityVersion(entity)
		setEntity(clone, key.family, key.id, entity)
		clone.HLC = MaxHLC(clone.HLC, clock)
		changed = true
	}
	if !changed {
		return clone, false, nil
	}
	if err := ValidateConvergentState(clone); err != nil {
		return ConvergentState{}, false, err
	}
	return clone, true, nil
}

// ConvergentTombstones returns entity tombstones in deterministic family/id
// order, for the materialized v1 syncMeta.deletions list.
func ConvergentTombstones(state ConvergentState) ([]ConvergentDeletion, error) {
	if err := ValidateConvergentState(state); err != nil {
		return nil, err
	}
	deletions := []ConvergentDeletion{}
	families := make([]string, 0, len(state.Collections))
	for family := range state.Collections {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		collection := state.Collections[family]
		ids := make([]string, 0, len(collection.Entities))
		for id := range collection.Entities {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			entity := collection.Entities[id]
			winner, exists := SelectRegisterWinner(entity.Presence)
			if !exists || !winner.Tombstone {
				continue
			}
			deletions = append(deletions, ConvergentDeletion{
				Family:    family,
				ID:        id,
				DeletedAt: winner.HLC.WallTime,
				Node:      winner.Dot.Node,
			})
		}
	}
	return deletions, nil
}

// ConvergentConflict is one register that retains more than one distinct
// value. The values themselves are never included: the UI only shows that a
// field is contested and which register address it belongs to.
type ConvergentConflict struct {
	Family     string `json:"family"`
	EntityID   string `json:"entityId"`
	Field      string `json:"field"`
	Register   string `json:"register"`
	Candidates int    `json:"candidates"`
	Tombstone  bool   `json:"tombstone"`
}

// ConvergentConflicts lists retained multi-value conflicts in deterministic
// family/entity/field order.
func ConvergentConflicts(state ConvergentState) ([]ConvergentConflict, error) {
	if err := ValidateConvergentState(state); err != nil {
		return nil, err
	}
	conflicts := []ConvergentConflict{}
	families := make([]string, 0, len(state.Collections))
	for family := range state.Collections {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		collection := state.Collections[family]
		ids := make([]string, 0, len(collection.Entities))
		for id := range collection.Entities {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			entity := collection.Entities[id]
			if RegisterHasConflict(entity.Presence) {
				tombstone := false
				for _, candidate := range entity.Presence.Candidates {
					tombstone = tombstone || candidate.Tombstone
				}
				conflicts = append(conflicts, ConvergentConflict{
					Family:     family,
					EntityID:   id,
					Field:      presenceField,
					Register:   RegisterIdentity(family, id, presenceField),
					Candidates: len(entity.Presence.Candidates),
					Tombstone:  tombstone,
				})
			}
			fields := make([]string, 0, len(entity.Fields))
			for field := range entity.Fields {
				fields = append(fields, field)
			}
			sort.Strings(fields)
			for _, field := range fields {
				register := entity.Fields[field]
				if !RegisterHasConflict(register) {
					continue
				}
				tombstone := false
				for _, candidate := range register.Candidates {
					tombstone = tombstone || candidate.Tombstone
				}
				conflicts = append(conflicts, ConvergentConflict{
					Family:     family,
					EntityID:   id,
					Field:      field,
					Register:   RegisterIdentity(family, id, field),
					Candidates: len(register.Candidates),
					Tombstone:  tombstone,
				})
			}
		}
	}
	return conflicts, nil
}

// ConvergentConflictCount is the number of contested registers.
func ConvergentConflictCount(state ConvergentState) int {
	conflicts, err := ConvergentConflicts(state)
	if err != nil {
		return 0
	}
	return len(conflicts)
}

// ConvergentEntityHashes returns one canonical hash per entity, keyed by
// ManifestKey, for the second verification round.
func ConvergentEntityHashes(state ConvergentState) (map[string]string, error) {
	if err := ValidateConvergentState(state); err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for family, collection := range state.Collections {
		for id, entity := range collection.Entities {
			hash, err := ConvergentEntityHash(entity)
			if err != nil {
				return nil, err
			}
			hashes[ManifestKey(family, id)] = hash
		}
	}
	return hashes, nil
}

// ---------------------------------------------------------------------------
// Envelope
// ---------------------------------------------------------------------------

// WrapConvergentEnvelope builds the v2 envelope that lives inside the encrypted
// payload.
func WrapConvergentEnvelope(state ConvergentState) (ConvergentEnvelope, error) {
	if err := ValidateConvergentState(state); err != nil {
		return ConvergentEnvelope{}, err
	}
	return ConvergentEnvelope{
		SchemaVersion: ConvergentSchemaVersion,
		Encoding:      ConvergentEncodingMaterializedWinner,
		State:         state,
	}, nil
}

// ParseConvergentEnvelope strictly decodes the envelope. Unknown or newer
// schemas, wrong encodings, and malformed states are typed errors; this is the
// fail-closed boundary that prevents a v2 payload being read as v1 or vice
// versa.
func ParseConvergentEnvelope(raw json.RawMessage) (ConvergentEnvelope, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ConvergentEnvelope{}, fmt.Errorf("%w: missing convergent envelope", ErrConvergentSchema)
	}
	var shape struct {
		SchemaVersion *int            `json:"schemaVersion"`
		Encoding      string          `json:"encoding"`
		State         json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return ConvergentEnvelope{}, fmt.Errorf("%w: malformed envelope: %v", ErrConvergentInvalid, err)
	}
	if shape.SchemaVersion == nil {
		return ConvergentEnvelope{}, fmt.Errorf("%w: envelope has no schema version", ErrConvergentSchema)
	}
	if *shape.SchemaVersion != ConvergentSchemaVersion {
		return ConvergentEnvelope{}, fmt.Errorf("%w: envelope schema %d (supported: %d)",
			ErrConvergentSchema, *shape.SchemaVersion, ConvergentSchemaVersion)
	}
	if shape.Encoding != ConvergentEncodingMaterializedWinner {
		return ConvergentEnvelope{}, fmt.Errorf("%w: envelope encoding %q", ErrConvergentSchema, shape.Encoding)
	}
	if len(shape.State) == 0 {
		return ConvergentEnvelope{}, fmt.Errorf("%w: envelope has no state", ErrConvergentSchema)
	}
	var state ConvergentState
	if err := json.Unmarshal(shape.State, &state); err != nil {
		return ConvergentEnvelope{}, fmt.Errorf("%w: malformed state: %v", ErrConvergentInvalid, err)
	}
	normalized, err := NormalizeConvergentState(state)
	if err != nil {
		return ConvergentEnvelope{}, err
	}
	return ConvergentEnvelope{
		SchemaVersion: ConvergentSchemaVersion,
		Encoding:      ConvergentEncodingMaterializedWinner,
		State:         normalized,
	}, nil
}

// EnvelopeMatchesSnapshot verifies that the inline CRDT winners and the
// materialized v1 families agree. A mismatch means an older writer changed the
// plaintext snapshot without updating the causal metadata, which must fail
// closed rather than silently discard the edit.
func EnvelopeMatchesSnapshot(state ConvergentState, materialized ConvergentSnapshot) error {
	snapshot, err := MaterializeConvergentSnapshot(state)
	if err != nil {
		return err
	}
	// Compare the union of both family maps. Iterating only the envelope
	// would accept a populated materialized family that the envelope does not
	// mention at all, which is exactly the partial-payload case this boundary
	// must reject.
	families := make([]string, 0, len(snapshot.Families)+len(materialized.Families))
	seen := map[string]bool{}
	for family := range snapshot.Families {
		if !seen[family] {
			seen[family] = true
			families = append(families, family)
		}
	}
	for family, records := range materialized.Families {
		if seen[family] {
			continue
		}
		if len(records) > 0 {
			return fmt.Errorf("%w: materialized %s has %d records, envelope has none",
				ErrConvergentInvalid, family, len(records))
		}
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		records := snapshot.Families[family]
		materializedRecords := materialized.Families[family]
		if len(records) != len(materializedRecords) {
			return fmt.Errorf("%w: materialized %s has %d records, envelope has %d",
				ErrConvergentInvalid, family, len(materializedRecords), len(records))
		}
		byID := map[string]json.RawMessage{}
		for _, record := range materializedRecords {
			byID[record.ID] = record.Value
		}
		for _, record := range records {
			value, exists := byID[record.ID]
			if !exists || !bytes.Equal(value, record.Value) {
				return fmt.Errorf("%w: materialized %s/%s does not match the envelope",
					ErrConvergentInvalid, family, record.ID)
			}
		}
	}
	return nil
}
