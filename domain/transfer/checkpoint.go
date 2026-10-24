package transfer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"strings"
)

// Directory checkpoint limits. They are the original v2 manifest contract:
// a bounded, fixed-width record instead of one row per file, a 32-directory
// symlink follow depth, and the traversal work budget that keeps a hostile
// tree from growing an operation without bound.
const (
	// DirectoryCheckpointVersion is the manifest form this build writes.
	// Version 1 is still accepted and migrated on resume.
	DirectoryCheckpointVersion = 2

	// MaxFollowedSymlinkDepth is how many nested symlinked directories a
	// download may follow.
	MaxFollowedSymlinkDepth = 32

	// MaxTraversalDirectories bounds the directories one traversal visits.
	MaxTraversalDirectories = 50_000

	// MaxTraversalEntries bounds the directory entries one traversal reads.
	MaxTraversalEntries = 200_000
)

// EmptyDirectoryManifestHash is the version 1 chain's seed.
const EmptyDirectoryManifestHash = "0000000000000000000000000000000000000000000000000000000000000000"

// emptyDirectoryManifestStateV2 is the SHA-256 initial state, used as the
// version 2 seed so the first identity is one compression block.
const emptyDirectoryManifestStateV2 = "6a09e667bb67ae853c6ef372a54ff53a" +
	"510e527f9b05688c1f83d9ab5be0cd19"

// Errors reported by a traversal budget. The application maps them onto its
// too-large failure kind.
var (
	// ErrDirectoryTraversalDirectoryLimit reports more directories than the
	// budget allows.
	ErrDirectoryTraversalDirectoryLimit = errors.New("transfer: directory traversal directory limit exceeded")
	// ErrDirectoryTraversalEntryLimit reports more entries than the budget
	// allows.
	ErrDirectoryTraversalEntryLimit = errors.New("transfer: directory traversal entry limit exceeded")
	errInvalidDirectoryManifest     = errors.New("transfer: invalid directory manifest")
)

// DirectoryCheckpoint is the bounded resume record stored on a top-level
// directory Transfer. CoveredEntries is the traversal prefix whose identities
// are chained into ManifestHash; CompletedEntries is how much of that prefix
// is already written. The manifest is a fixed 64-character value no matter how
// many entries the tree holds.
type DirectoryCheckpoint struct {
	// Version 1 chained a full SHA-256 over the previous digest; version 2
	// appends independent identity blocks onto the carried state.
	Version int `json:"version"`
	// CoveredEntries is the prefix covered by the manifest.
	CoveredEntries int `json:"coveredEntries"`
	// CompletedEntries is the covered prefix already copied.
	CompletedEntries int `json:"completedEntries"`
	// ManifestHash is the chained digest of the covered prefix.
	ManifestHash string `json:"manifestHash"`
}

// CreateEmptyDirectoryCheckpoint returns the version 2 seed.
func CreateEmptyDirectoryCheckpoint() DirectoryCheckpoint {
	return DirectoryCheckpoint{
		Version:          DirectoryCheckpointVersion,
		ManifestHash:     emptyDirectoryManifestStateV2,
		CoveredEntries:   0,
		CompletedEntries: 0,
	}
}

// Valid reports whether a persisted checkpoint can be trusted as a resume
// record. It does not prove the manifest matches a plan; verification does.
func (c DirectoryCheckpoint) Valid() bool {
	if c.Version != 1 && c.Version != DirectoryCheckpointVersion {
		return false
	}
	if c.CoveredEntries < 0 || c.CompletedEntries < 0 || c.CompletedEntries > c.CoveredEntries {
		return false
	}
	return validManifestHash(c.ManifestHash)
}

// Clone returns a copy. The value holds no references, so it is its own copy;
// the method keeps the queue's cloning symmetric.
func (c DirectoryCheckpoint) Clone() DirectoryCheckpoint { return c }

func validManifestHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, character := range hash {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// CreateDirectoryEntryIdentity hashes the stable fields that make one planned
// entry: its source, its target, its size, and its modification time in
// milliseconds. A changed source therefore invalidates the covered prefix
// instead of skipping a stale file.
func CreateDirectoryEntryIdentity(sourcePath, targetPath string, size, lastModified int64) string {
	if size < 0 {
		size = 0
	}
	if lastModified < 0 {
		lastModified = 0
	}
	payload, err := json.Marshal([]any{sourcePath, targetPath, size, lastModified})
	if err != nil {
		// Strings, integers, and a fixed array cannot fail to encode. Keep a
		// stable value rather than panicking in a transfer.
		payload = []byte(fmt.Sprintf("[%q,%q,%d,%d]", sourcePath, targetPath, size, lastModified))
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// DirectoryManifestAccumulator appends entry identities to a checkpoint
// manifest. Version 2 carries the raw SHA-256 state, so each append costs one
// compression block; version 1 re-hashes the previous digest as the original
// legacy chain did.
type DirectoryManifestAccumulator struct {
	version int
	state   [8]uint32
	legacy  string
}

// NewDirectoryManifestAccumulator starts from a checkpoint's covered prefix.
func NewDirectoryManifestAccumulator(checkpoint DirectoryCheckpoint) (*DirectoryManifestAccumulator, error) {
	if !checkpoint.Valid() {
		return nil, errInvalidDirectoryManifest
	}
	if checkpoint.Version == 1 {
		return &DirectoryManifestAccumulator{version: 1, legacy: checkpoint.ManifestHash}, nil
	}
	state, err := parseManifestState(checkpoint.ManifestHash)
	if err != nil {
		return nil, err
	}
	return &DirectoryManifestAccumulator{version: 2, state: state}, nil
}

// Append adds one 64-character entry identity.
func (a *DirectoryManifestAccumulator) Append(identity string) error {
	if !validManifestHash(identity) {
		return errInvalidDirectoryManifest
	}
	if a.version == 1 {
		sum := sha256.Sum256([]byte(a.legacy + ":" + identity))
		a.legacy = hex.EncodeToString(sum[:])
		return nil
	}
	var block [64]byte
	copy(block[:], identity)
	compressSHA256Block(&a.state, block[:])
	return nil
}

// Digest returns the current fixed-width manifest.
func (a *DirectoryManifestAccumulator) Digest() string {
	if a.version == 1 {
		return a.legacy
	}
	var out [64]byte
	for index, word := range a.state {
		hex.Encode(out[index*8:index*8+8], []byte{
			byte(word >> 24), byte(word >> 16), byte(word >> 8), byte(word),
		})
	}
	return string(out[:])
}

// AppendDirectoryCheckpointIdentity returns checkpoint with identity appended
// and CoveredEntries advanced. CompletedEntries is the caller's decision.
func AppendDirectoryCheckpointIdentity(checkpoint DirectoryCheckpoint, identity string) (DirectoryCheckpoint, error) {
	accumulator, err := NewDirectoryManifestAccumulator(checkpoint)
	if err != nil {
		return DirectoryCheckpoint{}, err
	}
	if err := accumulator.Append(identity); err != nil {
		return DirectoryCheckpoint{}, err
	}
	checkpoint.ManifestHash = accumulator.Digest()
	checkpoint.CoveredEntries++
	return checkpoint, nil
}

// VerifyDirectoryCheckpoint reports whether the checkpoint's covered prefix
// matches the first identities of a freshly planned traversal.
func VerifyDirectoryCheckpoint(checkpoint DirectoryCheckpoint, identities []string) bool {
	if !checkpoint.Valid() || checkpoint.CoveredEntries > len(identities) {
		return false
	}
	seed := DirectoryCheckpoint{
		Version:      checkpoint.Version,
		ManifestHash: EmptyDirectoryManifestHash,
	}
	if checkpoint.Version == DirectoryCheckpointVersion {
		seed = CreateEmptyDirectoryCheckpoint()
	}
	accumulator, err := NewDirectoryManifestAccumulator(seed)
	if err != nil {
		return false
	}
	for _, identity := range identities[:checkpoint.CoveredEntries] {
		if err := accumulator.Append(identity); err != nil {
			return false
		}
	}
	return accumulator.Digest() == checkpoint.ManifestHash
}

// MigrateDirectoryCheckpoint upgrades a verified version 1 record to version
// 2. It returns false when the record is not a legacy record or its manifest
// does not match the plan.
func MigrateDirectoryCheckpoint(checkpoint DirectoryCheckpoint, identities []string) (DirectoryCheckpoint, bool) {
	if checkpoint.Version != 1 || !VerifyDirectoryCheckpoint(checkpoint, identities) {
		return DirectoryCheckpoint{}, false
	}
	migrated := CreateEmptyDirectoryCheckpoint()
	accumulator, err := NewDirectoryManifestAccumulator(migrated)
	if err != nil {
		return DirectoryCheckpoint{}, false
	}
	for _, identity := range identities[:checkpoint.CoveredEntries] {
		if err := accumulator.Append(identity); err != nil {
			return DirectoryCheckpoint{}, false
		}
	}
	migrated.ManifestHash = accumulator.Digest()
	migrated.CoveredEntries = checkpoint.CoveredEntries
	migrated.CompletedEntries = checkpoint.CompletedEntries
	return migrated, true
}

// DirectoryTraversalBudget bounds one traversal and tracks the current branch
// for cycle detection. Sibling aliases of one canonical directory are allowed
// because an ancestor is released when its visit ends.
type DirectoryTraversalBudget struct {
	VisitedDirectories int
	VisitedEntries     int
	MaxDirectories     int
	MaxEntries         int
}

// NewDirectoryTraversalBudget returns a budget with the original limits when
// either argument is not positive.
func NewDirectoryTraversalBudget(maxDirectories, maxEntries int) *DirectoryTraversalBudget {
	if maxDirectories <= 0 {
		maxDirectories = MaxTraversalDirectories
	}
	if maxEntries <= 0 {
		maxEntries = MaxTraversalEntries
	}
	return &DirectoryTraversalBudget{MaxDirectories: maxDirectories, MaxEntries: maxEntries}
}

// NormalizeDirectoryCanonicalPath makes canonical paths comparable across
// adapters: forward slashes, no trailing slash.
func NormalizeDirectoryCanonicalPath(path string) string {
	normalized := strings.ReplaceAll(path, `\`, "/")
	normalized = strings.TrimRight(normalized, "/")
	if normalized == "" {
		return "/"
	}
	return normalized
}

// Visit claims one directory against the budget. It returns "" with no error
// when canonicalPath is already on the current branch (a cycle); a count above
// the directory limit is an error. Release the returned key when the branch
// finishes.
func (b *DirectoryTraversalBudget) Visit(canonicalPath string, ancestors map[string]struct{}) (string, error) {
	normalized := NormalizeDirectoryCanonicalPath(canonicalPath)
	if _, exists := ancestors[normalized]; exists {
		return "", nil
	}
	if b.VisitedDirectories >= b.MaxDirectories {
		return "", ErrDirectoryTraversalDirectoryLimit
	}
	b.VisitedDirectories++
	ancestors[normalized] = struct{}{}
	return normalized, nil
}

// Release ends one branch visit.
func (b *DirectoryTraversalBudget) Release(claimed string, ancestors map[string]struct{}) {
	delete(ancestors, claimed)
}

// Account adds count entries to the traversal budget.
func (b *DirectoryTraversalBudget) Account(count int) error {
	if count <= 0 {
		return nil
	}
	if b.VisitedEntries+count > b.MaxEntries {
		return ErrDirectoryTraversalEntryLimit
	}
	b.VisitedEntries += count
	return nil
}

// ShouldFollowSymlinkDirectory reports whether a symlinked directory at
// symlinkDepth may be entered. The root is depth 0.
func ShouldFollowSymlinkDirectory(symlinkDepth int) bool {
	return symlinkDepth < MaxFollowedSymlinkDepth
}

func parseManifestState(value string) ([8]uint32, error) {
	var state [8]uint32
	if !validManifestHash(value) {
		return state, errInvalidDirectoryManifest
	}
	for index := range state {
		word, err := hex.DecodeString(value[index*8 : index*8+8])
		if err != nil || len(word) != 4 {
			return state, errInvalidDirectoryManifest
		}
		state[index] = binary.BigEndian.Uint32(word)
	}
	return state, nil
}

// SHA-256 round constants. The manifest keeps the compression state between
// appends so one identity is one block, matching the original v2 record.
var sha256RoundConstants = [64]uint32{
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
}

// compressSHA256Block applies one SHA-256 compression round to state.
func compressSHA256Block(state *[8]uint32, block []byte) {
	var schedule [64]uint32
	for index := 0; index < 16; index++ {
		schedule[index] = binary.BigEndian.Uint32(block[index*4 : index*4+4])
	}
	for index := 16; index < 64; index++ {
		previous15 := schedule[index-15]
		previous2 := schedule[index-2]
		s0 := bits.RotateLeft32(previous15, -7) ^ bits.RotateLeft32(previous15, -18) ^ (previous15 >> 3)
		s1 := bits.RotateLeft32(previous2, -17) ^ bits.RotateLeft32(previous2, -19) ^ (previous2 >> 10)
		schedule[index] = schedule[index-16] + s0 + schedule[index-7] + s1
	}
	a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3], state[4], state[5], state[6], state[7]
	for index := 0; index < 64; index++ {
		s1 := bits.RotateLeft32(e, -6) ^ bits.RotateLeft32(e, -11) ^ bits.RotateLeft32(e, -25)
		choice := (e & f) ^ (^e & g)
		temp1 := h + s1 + choice + sha256RoundConstants[index] + schedule[index]
		s0 := bits.RotateLeft32(a, -2) ^ bits.RotateLeft32(a, -13) ^ bits.RotateLeft32(a, -22)
		majority := (a & b) ^ (a & c) ^ (b & c)
		temp2 := s0 + majority
		h = g
		g = f
		f = e
		e = d + temp1
		d = c
		c = b
		b = a
		a = temp1 + temp2
	}
	state[0] += a
	state[1] += b
	state[2] += c
	state[3] += d
	state[4] += e
	state[5] += f
	state[6] += g
	state[7] += h
}
