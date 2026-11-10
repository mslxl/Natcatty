package plugin

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// The workspace host services are consumer-owned ports. The application layer
// enforces permissions, scopes, quotas, and audit; the composition root wires
// the Vault, session manager, terminal observation, and remote file adapters.
// None of these ports may be satisfied by widening another plugin's grants.

// HostSummary is the non-secret projection of one Vault Host. Secret material
// never crosses this boundary.
type HostSummary struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Hostname string   `json:"hostname"`
	Port     int      `json:"port"`
	Protocol string   `json:"protocol"`
	Username string   `json:"username"`
	Tags     []string `json:"tags,omitempty"`
	// CreatedBy is the plugin id that created the Host through the hosts
	// service. Empty means the user created it in the Vault.
	CreatedBy string `json:"createdBy,omitempty"`
}

// HostDraft is one Host a plugin asks the host to create or import. Secret
// fields are references, not plaintext.
type HostDraft struct {
	Label    string
	Hostname string
	Port     int
	Protocol string
	Username string
	Tags     []string
	// CreatedBy is set by the host service to the calling plugin id; a guest
	// payload can never choose it.
	CreatedBy string
}

// HostPatch is a partial Host update. Nil fields stay unchanged.
type HostPatch struct {
	Label    *string
	Hostname *string
	Port     *int
	Protocol *string
	Username *string
	Tags     *[]string
}

// HostTestResult is one reachability probe result. Detail is a short typed
// reason; it never contains credentials or Vault values.
type HostTestResult struct {
	Reachable bool   `json:"reachable"`
	LatencyMS int64  `json:"latencyMs"`
	Detail    string `json:"detail,omitempty"`
}

// HostCatalog is the host-side Vault port for the hosts service.
type HostCatalog interface {
	// List returns every Host the port can see. The application filters the
	// result to the plugin's granted or plugin-created Hosts.
	List(ctx context.Context) ([]HostSummary, error)
	// Get returns one Host or false when it does not exist.
	Get(ctx context.Context, id string) (HostSummary, bool, error)
	// Create persists one Host and returns the saved projection.
	Create(ctx context.Context, draft HostDraft) (HostSummary, error)
	// Update applies a partial change and returns the saved projection.
	Update(ctx context.Context, id string, patch HostPatch) (HostSummary, error)
	// Import creates Hosts in input order. It returns the Hosts created so far
	// alongside an error naming the first failure; a failed entry does not
	// roll back the entries that succeeded.
	Import(ctx context.Context, drafts []HostDraft) ([]HostSummary, error)
	// Test probes reachability without echoing credentials.
	Test(ctx context.Context, id string) (HostTestResult, error)
}

// SessionSummary is the non-secret projection of one live Session.
type SessionSummary struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	HostID  string `json:"hostId"`
	State   string `json:"state"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	CursorX int    `json:"cursorX"`
	CursorY int    `json:"cursorY"`
	// ProviderCreatedBy names the plugin whose connection provider created
	// this Session. The creating plugin needs no per-session grant; every
	// other plugin needs an exact Session grant.
	ProviderCreatedBy string `json:"providerCreatedBy,omitempty"`
}

// SessionCatalog is the host-side session manager port for the sessions
// service. The application owns authorization; the port only reports and
// performs the requested operation.
type SessionCatalog interface {
	List(ctx context.Context) ([]SessionSummary, error)
	Get(ctx context.Context, id string) (SessionSummary, bool, error)
	// Connect opens a Session for one Host and returns its projection.
	Connect(ctx context.Context, hostID string) (SessionSummary, error)
	// Reconnect reopens a retryable Session transport.
	Reconnect(ctx context.Context, id string) error
	// Disconnect closes one Session.
	Disconnect(ctx context.Context, id string) error
}

// TerminalInfo is the bounded terminal metadata exposed by terminal.info.
type TerminalInfo struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	CursorX   int    `json:"cursorX"`
	CursorY   int    `json:"cursorY"`
}

// TerminalObservation is one host-side lossy, read-only output tap. Read
// blocks until output arrives or the tap closes; Close must unblock Read.
type TerminalObservation interface {
	Read(ctx context.Context, buffer []byte) (int, error)
	Info() TerminalInfo
	Close() error
}

// TerminalCatalog is the host-side terminal port. Observe is read-only and
// lossy; Write is the separate input path; Info reports metadata.
type TerminalCatalog interface {
	Observe(ctx context.Context, sessionID string) (TerminalObservation, error)
	Write(ctx context.Context, sessionID string, data []byte) error
	Info(ctx context.Context, sessionID string) (TerminalInfo, bool, error)
}

// RemoteEntry is one entry on a Host's filesystem.
type RemoteEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Mode     uint32    `json:"mode"`
	Modified time.Time `json:"modified"`
}

// RemoteFiles is the host-mediated remote filesystem port. Every operation is
// bound to one Host; the adapter must never consult the local filesystem for a
// remote request.
type RemoteFiles interface {
	List(ctx context.Context, host, path string) ([]RemoteEntry, error)
	Stat(ctx context.Context, host, path string) (RemoteEntry, bool, error)
	// Read returns at most length bytes at offset. eof reports the end of the
	// file. It must never read outside the Host's filesystem.
	Read(ctx context.Context, host, path string, offset int64, length int) ([]byte, bool, error)
	// Write writes data at offset and returns the bytes written.
	Write(ctx context.Context, host, path string, offset int64, data []byte) (int, error)
	Rename(ctx context.Context, host, from, to string) error
	Delete(ctx context.Context, host, path string, recursive bool) error
	Mkdir(ctx context.Context, host, path string) error
}

// MemoryHostCatalog is a deterministic HostCatalog for tests and for a
// composition without a Vault.
type MemoryHostCatalog struct {
	mu    sync.Mutex
	hosts map[string]HostSummary
	order []string
	seq   int
	// Reachable, when non-nil, decides the Test result for a Host id.
	Reachable func(id string) bool
}

// NewMemoryHostCatalog returns a catalog holding the supplied Hosts in order.
func NewMemoryHostCatalog(hosts ...HostSummary) *MemoryHostCatalog {
	catalog := &MemoryHostCatalog{hosts: make(map[string]HostSummary)}
	for _, host := range hosts {
		catalog.put(host)
	}
	return catalog
}

func (m *MemoryHostCatalog) put(host HostSummary) {
	if _, exists := m.hosts[host.ID]; !exists {
		m.order = append(m.order, host.ID)
	}
	m.hosts[host.ID] = cloneHostSummary(host)
}

// List implements HostCatalog.
func (m *MemoryHostCatalog) List(context.Context) ([]HostSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]HostSummary, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, cloneHostSummary(m.hosts[id]))
	}
	return out, nil
}

// Get implements HostCatalog.
func (m *MemoryHostCatalog) Get(_ context.Context, id string) (HostSummary, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	host, ok := m.hosts[id]
	return cloneHostSummary(host), ok, nil
}

// Create implements HostCatalog.
func (m *MemoryHostCatalog) Create(_ context.Context, draft HostDraft) (HostSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	host := HostSummary{
		ID:        fmt.Sprintf("host-%d", m.seq),
		Label:     draft.Label,
		Hostname:  draft.Hostname,
		Port:      draft.Port,
		Protocol:  draft.Protocol,
		Username:  draft.Username,
		Tags:      append([]string(nil), draft.Tags...),
		CreatedBy: draft.CreatedBy,
	}
	m.put(host)
	return cloneHostSummary(host), nil
}

// Update implements HostCatalog.
func (m *MemoryHostCatalog) Update(_ context.Context, id string, patch HostPatch) (HostSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	host, ok := m.hosts[id]
	if !ok {
		return HostSummary{}, &workspaceFailure{Code: ServiceCodeNotFound,
			Problem: "host " + id + " does not exist"}
	}
	if patch.Label != nil {
		host.Label = *patch.Label
	}
	if patch.Hostname != nil {
		host.Hostname = *patch.Hostname
	}
	if patch.Port != nil {
		host.Port = *patch.Port
	}
	if patch.Protocol != nil {
		host.Protocol = *patch.Protocol
	}
	if patch.Username != nil {
		host.Username = *patch.Username
	}
	if patch.Tags != nil {
		host.Tags = append([]string(nil), (*patch.Tags)...)
	}
	m.hosts[id] = host
	return cloneHostSummary(host), nil
}

// Import implements HostCatalog.
func (m *MemoryHostCatalog) Import(ctx context.Context, drafts []HostDraft) ([]HostSummary, error) {
	out := make([]HostSummary, 0, len(drafts))
	for _, draft := range drafts {
		host, err := m.Create(ctx, draft)
		if err != nil {
			return out, err
		}
		out = append(out, host)
	}
	return out, nil
}

// Test implements HostCatalog.
func (m *MemoryHostCatalog) Test(_ context.Context, id string) (HostTestResult, error) {
	m.mu.Lock()
	_, ok := m.hosts[id]
	reachable := m.Reachable
	m.mu.Unlock()
	if !ok {
		return HostTestResult{}, &workspaceFailure{Code: ServiceCodeNotFound,
			Problem: "host " + id + " does not exist"}
	}
	result := HostTestResult{Reachable: true, LatencyMS: 1}
	if reachable != nil {
		result.Reachable = reachable(id)
	}
	if !result.Reachable {
		result.Detail = "unreachable"
	}
	return result, nil
}

// MemorySessionCatalog is a deterministic SessionCatalog for tests and for a
// composition without a live session manager.
type MemorySessionCatalog struct {
	mu       sync.Mutex
	sessions map[string]SessionSummary
	order    []string
	next     int
}

// NewMemorySessionCatalog returns a catalog holding the supplied Sessions.
func NewMemorySessionCatalog(sessions ...SessionSummary) *MemorySessionCatalog {
	catalog := &MemorySessionCatalog{sessions: make(map[string]SessionSummary)}
	for _, session := range sessions {
		catalog.put(session)
	}
	return catalog
}

func (m *MemorySessionCatalog) put(session SessionSummary) {
	if _, exists := m.sessions[session.ID]; !exists {
		m.order = append(m.order, session.ID)
	}
	m.sessions[session.ID] = session
}

// List implements SessionCatalog.
func (m *MemorySessionCatalog) List(context.Context) ([]SessionSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SessionSummary, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.sessions[id])
	}
	return out, nil
}

// Get implements SessionCatalog.
func (m *MemorySessionCatalog) Get(_ context.Context, id string) (SessionSummary, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	return session, ok, nil
}

// Connect implements SessionCatalog.
func (m *MemorySessionCatalog) Connect(_ context.Context, hostID string) (SessionSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	session := SessionSummary{
		ID:     fmt.Sprintf("session-%d", m.next),
		Kind:   "ssh",
		Title:  hostID,
		HostID: hostID,
		State:  "running",
	}
	m.put(session)
	return session, nil
}

// Reconnect implements SessionCatalog.
func (m *MemorySessionCatalog) Reconnect(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return &workspaceFailure{Code: ServiceCodeNotFound,
			Problem: "session " + id + " does not exist"}
	}
	return nil
}

// Disconnect implements SessionCatalog.
func (m *MemorySessionCatalog) Disconnect(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return &workspaceFailure{Code: ServiceCodeNotFound,
			Problem: "session " + id + " does not exist"}
	}
	delete(m.sessions, id)
	return nil
}

// MemoryTerminalCatalog is a deterministic TerminalCatalog. Observations
// replay the fixed chunks supplied for the Session and then block until Close.
type MemoryTerminalCatalog struct {
	mu       sync.Mutex
	infos    map[string]TerminalInfo
	chunks   map[string][][]byte
	writes   map[string][][]byte
	watchers []*memoryTerminalObservation
}

// NewMemoryTerminalCatalog returns a terminal catalog with Sessions, their
// metadata, and the output chunks each observation replays.
func NewMemoryTerminalCatalog(infos []TerminalInfo, chunks map[string][][]byte) *MemoryTerminalCatalog {
	catalog := &MemoryTerminalCatalog{
		infos:  make(map[string]TerminalInfo, len(infos)),
		chunks: make(map[string][][]byte, len(chunks)),
		writes: make(map[string][][]byte),
	}
	for _, info := range infos {
		catalog.infos[info.SessionID] = info
	}
	for id, list := range chunks {
		for _, chunk := range list {
			catalog.chunks[id] = append(catalog.chunks[id], append([]byte(nil), chunk...))
		}
	}
	return catalog
}

// Observe implements TerminalCatalog.
func (m *MemoryTerminalCatalog) Observe(_ context.Context, sessionID string) (TerminalObservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.infos[sessionID]
	if !ok {
		return nil, &workspaceFailure{Code: ServiceCodeNotFound,
			Problem: "session " + sessionID + " does not exist"}
	}
	observation := &memoryTerminalObservation{
		catalog: m,
		info:    info,
		closed:  make(chan struct{}),
	}
	for _, chunk := range m.chunks[sessionID] {
		observation.chunks = append(observation.chunks, append([]byte(nil), chunk...))
	}
	m.watchers = append(m.watchers, observation)
	return observation, nil
}

// Write implements TerminalCatalog.
func (m *MemoryTerminalCatalog) Write(_ context.Context, sessionID string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.infos[sessionID]; !ok {
		return &workspaceFailure{Code: ServiceCodeNotFound,
			Problem: "session " + sessionID + " does not exist"}
	}
	m.writes[sessionID] = append(m.writes[sessionID], append([]byte(nil), data...))
	return nil
}

// Info implements TerminalCatalog.
func (m *MemoryTerminalCatalog) Info(_ context.Context, sessionID string) (TerminalInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.infos[sessionID]
	return info, ok, nil
}

// Writes returns the bytes written to one Session in order.
func (m *MemoryTerminalCatalog) Writes(sessionID string) [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.writes[sessionID]...)
}

// Push appends live output to every open observation of a Session.
func (m *MemoryTerminalCatalog) Push(sessionID string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, observation := range m.watchers {
		if observation.info.SessionID == sessionID {
			observation.push(data)
		}
	}
}

// ObservationCount reports live observations, for leak tests.
func (m *MemoryTerminalCatalog) ObservationCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := 0
	for _, observation := range m.watchers {
		if !observation.isClosed() {
			live++
		}
	}
	return live
}

type memoryTerminalObservation struct {
	catalog *MemoryTerminalCatalog
	info    TerminalInfo
	closed  chan struct{}
	once    sync.Once

	mu     sync.Mutex
	chunks [][]byte
}

func (o *memoryTerminalObservation) push(data []byte) {
	o.mu.Lock()
	o.chunks = append(o.chunks, append([]byte(nil), data...))
	o.mu.Unlock()
}

func (o *memoryTerminalObservation) Read(ctx context.Context, buffer []byte) (int, error) {
	for {
		o.mu.Lock()
		if len(o.chunks) > 0 {
			chunk := o.chunks[0]
			n := copy(buffer, chunk)
			if n == len(chunk) {
				o.chunks = o.chunks[1:]
			} else {
				o.chunks[0] = chunk[n:]
			}
			o.mu.Unlock()
			return n, nil
		}
		o.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-o.closed:
			return 0, io.EOF
		default:
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-o.closed:
			return 0, io.EOF
		case <-time.After(5 * time.Millisecond):
			// A memory tap has no condition variable; polling keeps Read
			// cancellable and deterministic without depending on scheduling.
		}
	}
}

func (o *memoryTerminalObservation) Info() TerminalInfo { return o.info }

func (o *memoryTerminalObservation) Close() error {
	o.once.Do(func() { close(o.closed) })
	return nil
}

func (o *memoryTerminalObservation) isClosed() bool {
	select {
	case <-o.closed:
		return true
	default:
		return false
	}
}

// MemoryRemoteFiles is a deterministic in-memory RemoteFiles over a per-Host
// map of POSIX paths. It exists for tests and never touches the local disk.
type MemoryRemoteFiles struct {
	mu    sync.Mutex
	files map[string]map[string][]byte
	dirs  map[string]map[string]bool
}

// NewMemoryRemoteFiles returns an empty remote filesystem.
func NewMemoryRemoteFiles() *MemoryRemoteFiles {
	return &MemoryRemoteFiles{
		files: make(map[string]map[string][]byte),
		dirs:  make(map[string]map[string]bool),
	}
}

// SeedFile installs one remote file.
func (m *MemoryRemoteFiles) SeedFile(host, path string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files[host] == nil {
		m.files[host] = make(map[string][]byte)
	}
	m.files[host][path] = append([]byte(nil), data...)
	m.ensureParentsLocked(host, path)
}

func (m *MemoryRemoteFiles) ensureParentsLocked(host, path string) {
	if m.dirs[host] == nil {
		m.dirs[host] = make(map[string]bool)
	}
	for _, parent := range remoteParents(path) {
		m.dirs[host][parent] = true
	}
}

// List implements RemoteFiles.
func (m *MemoryRemoteFiles) List(_ context.Context, host, path string) ([]RemoteEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := make(map[string]RemoteEntry)
	for name, data := range m.files[host] {
		if remoteParent(name) == path {
			entries[name] = RemoteEntry{Name: remoteBase(name), Path: name, Size: int64(len(data))}
		}
	}
	for name := range m.dirs[host] {
		if name != path && remoteParent(name) == path {
			entries[name] = RemoteEntry{Name: remoteBase(name), Path: name, Dir: true}
		}
	}
	out := make([]RemoteEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Stat implements RemoteFiles.
func (m *MemoryRemoteFiles) Stat(_ context.Context, host, path string) (RemoteEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if data, ok := m.files[host][path]; ok {
		return RemoteEntry{Name: remoteBase(path), Path: path, Size: int64(len(data))}, true, nil
	}
	if m.dirs[host][path] {
		return RemoteEntry{Name: remoteBase(path), Path: path, Dir: true}, true, nil
	}
	return RemoteEntry{}, false, nil
}

// Read implements RemoteFiles.
func (m *MemoryRemoteFiles) Read(_ context.Context, host, path string, offset int64, length int) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[host][path]
	if !ok {
		return nil, false, &workspaceFailure{Code: ServiceCodeNotFound,
			Problem: "remote file " + path + " does not exist"}
	}
	if offset < 0 || offset > int64(len(data)) {
		return nil, true, nil
	}
	if length < 0 {
		return nil, false, &workspaceFailure{Code: ServiceCodeInvalid,
			Problem: "read length must not be negative"}
	}
	end := offset + int64(length)
	eof := false
	if end >= int64(len(data)) {
		end = int64(len(data))
		eof = true
	}
	return append([]byte(nil), data[offset:end]...), eof, nil
}

// Write implements RemoteFiles.
func (m *MemoryRemoteFiles) Write(_ context.Context, host, path string, offset int64, data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if offset < 0 {
		return 0, &workspaceFailure{Code: ServiceCodeInvalid,
			Problem: "write offset must not be negative"}
	}
	if m.files[host] == nil {
		m.files[host] = make(map[string][]byte)
	}
	existing := m.files[host][path]
	end := offset + int64(len(data))
	if end > int64(len(existing)) {
		grown := make([]byte, end)
		copy(grown, existing)
		existing = grown
	}
	copy(existing[offset:], data)
	m.files[host][path] = existing
	m.ensureParentsLocked(host, path)
	return len(data), nil
}

// Rename implements RemoteFiles.
func (m *MemoryRemoteFiles) Rename(_ context.Context, host, from, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[host][from]
	if !ok {
		return &workspaceFailure{Code: ServiceCodeNotFound,
			Problem: "remote file " + from + " does not exist"}
	}
	delete(m.files[host], from)
	m.files[host][to] = data
	m.ensureParentsLocked(host, to)
	return nil
}

// Delete implements RemoteFiles.
func (m *MemoryRemoteFiles) Delete(_ context.Context, host, path string, recursive bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[host][path]; ok {
		delete(m.files[host], path)
		return nil
	}
	if m.dirs[host][path] {
		for name := range m.files[host] {
			if name == path || isRemoteDescendant(name, path) {
				delete(m.files[host], name)
			}
		}
		for name := range m.dirs[host] {
			if name == path || isRemoteDescendant(name, path) {
				delete(m.dirs[host], name)
			}
		}
		return nil
	}
	return &workspaceFailure{Code: ServiceCodeNotFound,
		Problem: "remote path " + path + " does not exist"}
}

// Mkdir implements RemoteFiles.
func (m *MemoryRemoteFiles) Mkdir(_ context.Context, host, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirs[host] == nil {
		m.dirs[host] = make(map[string]bool)
	}
	m.dirs[host][path] = true
	m.ensureParentsLocked(host, path)
	return nil
}

// File returns one remote file's bytes, for tests.
func (m *MemoryRemoteFiles) File(host, path string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[host][path]
	return append([]byte(nil), data...), ok
}

func cloneHostSummary(host HostSummary) HostSummary {
	host.Tags = append([]string(nil), host.Tags...)
	return host
}
