package hostatuin

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
)

// fakeFile is one remote file the detector may stat and open.
type fakeFile struct {
	content []byte
	modTime time.Time
	regular bool
	symlink bool
	// size overrides the stat size, which lets a test model a file that grows
	// after the pre-copy check.
	size *int64
}

// statErrAfter fails Stat calls for one path once it has been called more than
// after times, so a test can let detection see a candidate and then make the
// copy pass fail.
type statErrAfter struct {
	after int
	err   error
}

// fakeRemoteFiles records every read-only operation. It deliberately has no
// write or execute operation: the importer's port cannot ask for one.
type fakeRemoteFiles struct {
	root    string
	rootErr error
	files   map[string]*fakeFile
	// noInstall removes the default Atuin binary marker, modelling a Host
	// whose Atuin was uninstalled while its database stayed behind.
	noInstall bool
	// changingPath gains a modification-time change after the given stat,
	// simulating Atuin writing a file while the copy is in flight.
	changingPath  string
	changingAfter int
	// statErrs fails every Stat of a path, modelling a permission or
	// connection error.
	statErrs map[string]error
	// statErrAfter fails a path after a number of successful Stat calls,
	// modelling a candidate that becomes unreadable between detection and the
	// copy.
	statErrAfter map[string]statErrAfter
	// openErrs fails every Open of a path, modelling a hint that cannot be
	// read.
	openErrs map[string]error
	// statBlockPath stalls Stat until Close, modelling an SFTP call that does
	// not observe the deadline. statBlockAfter lets the detection pass succeed
	// before the copy pass blocks.
	statBlockPath  string
	statBlockAfter int
	statBlock      chan struct{}
	statEntered    chan struct{}
	statReturned   chan struct{}
	// openBlockPath stalls Open until Close, modelling an SFTP call that does
	// not observe the deadline.
	openBlockPath string
	openBlock     chan struct{}
	openEntered   chan struct{}
	openReturned  chan struct{}

	mu        sync.Mutex
	stats     []string
	opens     []string
	statCalls map[string]int
	closed    int
}

func (f *fakeRemoteFiles) Root(context.Context) (string, error) {
	return f.root, f.rootErr
}

func (f *fakeRemoteFiles) Stat(_ context.Context, name string) (FileInfo, error) {
	f.mu.Lock()
	f.stats = append(f.stats, name)
	if f.statCalls == nil {
		f.statCalls = make(map[string]int)
	}
	f.statCalls[name]++
	calls := f.statCalls[name]
	if err := f.statErrs[name]; err != nil {
		f.mu.Unlock()
		return FileInfo{}, err
	}
	if failure, ok := f.statErrAfter[name]; ok && calls > failure.after {
		f.mu.Unlock()
		return FileInfo{}, failure.err
	}
	file, ok := f.files[name]
	// Every test Host is an installed Atuin Host unless it opts out via
	// noInstall. The marker is a real stat the detector must see.
	marker := !f.noInstall && name == path.Join(f.root, ".local/bin/atuin")
	blocked := f.statBlock
	entered := f.statEntered
	returned := f.statReturned
	shouldBlock := f.statBlockPath == name && blocked != nil && calls > f.statBlockAfter
	var info FileInfo
	if ok {
		size := int64(len(file.content))
		if file.size != nil {
			size = *file.size
		}
		info = FileInfo{Size: size, ModTime: file.modTime, Regular: file.regular, Symlink: file.symlink}
		threshold := f.changingAfter
		if threshold == 0 {
			// Detection and the candidate re-stat account for the first stats;
			// four makes the pre-open stat the first changed one.
			threshold = 4
		}
		if name == f.changingPath && calls >= threshold {
			info.ModTime = info.ModTime.Add(time.Second)
		}
	}
	f.mu.Unlock()

	if shouldBlock {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		<-blocked
		if returned != nil {
			close(returned)
		}
		return FileInfo{}, errors.New("channel closed")
	}
	if !ok {
		if marker {
			return FileInfo{Size: 1, Regular: true}, nil
		}
		return FileInfo{}, os.ErrNotExist
	}
	return info, nil
}

func (f *fakeRemoteFiles) Open(_ context.Context, name string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opens = append(f.opens, name)
	file, ok := f.files[name]
	openErr := f.openErrs[name]
	blocked := f.openBlock
	entered := f.openEntered
	returned := f.openReturned
	shouldBlock := f.openBlockPath == name && blocked != nil
	f.mu.Unlock()

	if openErr != nil {
		return nil, openErr
	}
	if shouldBlock {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		<-blocked
		if returned != nil {
			close(returned)
		}
		return nil, errors.New("channel closed")
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(file.content)), nil
}

func (f *fakeRemoteFiles) Close() error {
	f.mu.Lock()
	f.closed++
	block := f.openBlock
	f.openBlock = nil
	statBlock := f.statBlock
	f.statBlock = nil
	f.mu.Unlock()
	if block != nil {
		close(block)
	}
	if statBlock != nil {
		close(statBlock)
	}
	return nil
}

func (f *fakeRemoteFiles) openedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opens...)
}

type fakeRemoteSource struct {
	files  *fakeRemoteFiles
	err    error
	hostID model.HostID
	opened int
}

func (s *fakeRemoteSource) OpenReadOnly(_ context.Context, hostID model.HostID) (RemoteFiles, error) {
	s.opened++
	s.hostID = hostID
	if s.err != nil {
		return nil, s.err
	}
	return s.files, nil
}

// fakeDatabase returns the rows the copied database would hold and records the
// side files the copy produced beside the main database.
type fakeDatabase struct {
	rows []domainhistory.AtuinRow
	err  error
	// errsByCall overrides err for the first calls, letting a test model one
	// unreadable candidate followed by a readable one.
	errsByCall []error
	calls      int
	dirs       []string
	copied     map[string][]byte
	copiedSet  bool
}

func (d *fakeDatabase) ReadHistory(_ context.Context, databasePath string) ([]domainhistory.AtuinRow, error) {
	d.calls++
	dir := filepath.Dir(databasePath)
	d.dirs = append(d.dirs, dir)
	if d.err != nil {
		return nil, d.err
	}
	if len(d.errsByCall) > 0 {
		err := d.errsByCall[0]
		d.errsByCall = d.errsByCall[1:]
		if err != nil {
			return nil, err
		}
	}
	d.copied = make(map[string][]byte)
	for _, name := range []string{historyName, walName, shmName} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			d.copied[name] = data
		}
	}
	d.copiedSet = true
	return append([]domainhistory.AtuinRow(nil), d.rows...), nil
}

type importCall struct {
	hostID string
	rows   []domainhistory.AtuinRow
	origin string
}

type fakeRecorder struct {
	mu        sync.Mutex
	imports   []importCall
	importErr error
	noticed   map[string]bool
	notifyErr error
	onImport  chan struct{}

	noticeCalls int
}

func (r *fakeRecorder) ImportHostAtuin(_ context.Context, hostID string,
	rows []domainhistory.AtuinRow, hostOrigin string) (int, error) {
	r.mu.Lock()
	r.imports = append(r.imports, importCall{hostID: hostID, rows: rows, origin: hostOrigin})
	err := r.importErr
	channel := r.onImport
	r.mu.Unlock()
	if channel != nil {
		channel <- struct{}{}
	}
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

func (r *fakeRecorder) MarkHostAtuinNoticed(_ context.Context, hostID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noticeCalls++
	if r.notifyErr != nil {
		return false, r.notifyErr
	}
	if r.noticed == nil {
		r.noticed = make(map[string]bool)
	}
	if r.noticed[hostID] {
		return false, nil
	}
	r.noticed[hostID] = true
	return true, nil
}

func (r *fakeRecorder) importCalls() []importCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]importCall(nil), r.imports...)
}

func (r *fakeRecorder) noticeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.noticeCalls
}

func testRow(id string, timestamp int64, command string) domainhistory.AtuinRow {
	return domainhistory.AtuinRow{ID: id, Timestamp: timestamp, Command: command}
}

// testWALHeader builds a WAL header with committed frames (size > 32) and the
// given salts. The WAL header is always big-endian.
func testWALHeader(salt1, salt2 uint32) []byte {
	header := make([]byte, walHeaderSize+1)
	binary.BigEndian.PutUint32(header[0:4], walMagic2)
	binary.BigEndian.PutUint32(header[8:12], 4096)
	binary.BigEndian.PutUint32(header[16:20], salt1)
	binary.BigEndian.PutUint32(header[20:24], salt2)
	return header
}

// testSHMHeader builds an initialized wal-index header. Its salts use the
// Host's byte order; the tests model a little-endian Host, and the matcher
// must accept either order.
func testSHMHeader(salt1, salt2 uint32) []byte {
	header := make([]byte, shmHeaderSize)
	header[12] = 1
	binary.LittleEndian.PutUint32(header[32:36], salt1)
	binary.LittleEndian.PutUint32(header[36:40], salt2)
	return header
}

func testService(source RemoteSource, database Database, recorder Recorder, opts ...Option) *Service {
	options := []Option{WithRemote(source), WithDatabase(database), WithRecorder(recorder)}
	options = append(options, opts...)
	return New(options...)
}

var testRef = HostRef{
	ID: "host-1", Hostname: "web.example.test", Username: "deploy", Label: "web",
}

func TestProbeCopiesTheNewDataDirWithWALAndSHMAndImports(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	wal := testWALHeader(7, 9)
	shm := testSHMHeader(7, 9)
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			historyPath:          {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
			historyPath + "-wal": {content: wal, modTime: time.Unix(101, 0), regular: true},
			historyPath + "-shm": {content: shm, modTime: time.Unix(102, 0), regular: true},
			// A legacy copy must not win over the newer directory.
			"/home/me/.local/share/atuin/history.db": {content: []byte("legacy"), regular: true},
		},
	}
	source := &fakeRemoteSource{files: files}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}

	service := testService(source, database, recorder)
	result, err := service.Probe(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !result.Present || result.Imported != 1 {
		t.Fatalf("result = %+v", result)
	}
	if source.opened != 1 || source.hostID != "host-1" {
		t.Fatalf("source opened %d times for %q", source.opened, source.hostID)
	}
	if files.closed != 1 {
		t.Fatalf("remote files closed %d times, want 1", files.closed)
	}
	if !database.copiedSet {
		t.Fatal("the database was not read")
	}
	if string(database.copied[historyName]) != "database" ||
		!bytes.Equal(database.copied[walName], wal) ||
		!bytes.Equal(database.copied[shmName], shm) {
		t.Fatalf("copied files = %q / %x / %x",
			database.copied[historyName], database.copied[walName], database.copied[shmName])
	}
	if got := files.openedNames(); len(got) != 3 {
		t.Fatalf("opened = %v, want the database and both side files", got)
	}
	imports := recorder.importCalls()
	if len(imports) != 1 {
		t.Fatalf("imports = %d, want 1", len(imports))
	}
	if imports[0].hostID != "host-1" || imports[0].origin != "web.example.test:deploy" {
		t.Fatalf("import = %+v", imports[0])
	}
	if len(imports[0].rows) != 1 || imports[0].rows[0].ID != "id-1" {
		t.Fatalf("imported rows = %+v", imports[0].rows)
	}
	// The private copy is removed once the snapshot is read.
	for _, dir := range database.dirs {
		if strings.HasPrefix(dir, "/home/me") {
			t.Fatalf("the database path points at the Host: %q", dir)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary copy %q was not removed: %v", dir, err)
		}
	}
}

func TestProbeFallsBackToTheLegacyDataDirAndUsesTheLabelOrigin(t *testing.T) {
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			"/home/me/.local/share/atuin/history.db": {
				content: []byte("legacy"), modTime: time.Unix(100, 0), regular: true,
			},
		},
	}
	source := &fakeRemoteSource{files: files}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "pwd")}}
	recorder := &fakeRecorder{}
	ref := HostRef{ID: "host-1", Username: "me", Label: "lab"}

	service := testService(source, database, recorder)
	result, err := service.Probe(context.Background(), ref)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !result.Present || result.Imported != 1 {
		t.Fatalf("result = %+v", result)
	}
	imports := recorder.importCalls()
	if len(imports) != 1 || imports[0].origin != "lab:me" {
		t.Fatalf("imports = %+v, want the label fallback origin", imports)
	}
}

func TestProbeWithoutADatabaseIsAbsentAndDoesNotImport(t *testing.T) {
	files := &fakeRemoteFiles{root: "/home/me", files: map[string]*fakeFile{
		"/home/me/.atuin/config.toml": {content: []byte("x"), regular: true},
	}}
	source := &fakeRemoteSource{files: files}
	database := &fakeDatabase{}
	recorder := &fakeRecorder{}

	service := testService(source, database, recorder)
	result, err := service.Probe(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if result != (ProbeResult{}) {
		t.Fatalf("result = %+v, want absent", result)
	}
	if database.calls != 0 || len(recorder.importCalls()) != 0 || recorder.noticeCount() != 0 {
		t.Fatalf("an absent Host imported or announced: db=%d imports=%d notices=%d",
			database.calls, len(recorder.importCalls()), recorder.noticeCount())
	}
}

// A saved Host without SFTP must keep the normal capture hook: a channel that
// never opens is the accepted residual window, because treating every channel
// failure as Atuin would disable persistent history for every Host without
// SFTP. Once any install evidence is seen, every later failure is Present.
func TestProbeWithoutAReadOnlyChannelIsAbsent(t *testing.T) {
	openError := errors.New("sftp unavailable")
	source := &fakeRemoteSource{err: openError}
	recorder := &fakeRecorder{}
	service := testService(source, &fakeDatabase{}, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if result != (ProbeResult{}) {
		t.Fatalf("result = %+v, want absent", result)
	}
	if len(recorder.importCalls()) != 0 {
		t.Fatal("an unreadable Host was imported")
	}
}

// A candidate the detection pass saw can become unreadable before the copy
// pass. That is not proof the Host stopped running Atuin: Probe must stay
// Present and return ErrProbeUnavailable so the Session captures transiently.
func TestProbeKeepsPresentWhenACandidateStatFailsAfterDetection(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root: "/home/me",
		statErrAfter: map[string]statErrAfter{
			historyPath: {after: 1, err: fs.ErrPermission},
		},
		files: map[string]*fakeFile{
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if !result.Present {
		t.Fatal("an inconclusive candidate Stat erased the Host's Atuin presence")
	}
	if !errors.Is(err, ErrProbeUnavailable) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error = %v, want ErrProbeUnavailable and fs.ErrPermission", err)
	}
	if database.calls != 0 || len(recorder.importCalls()) != 0 {
		t.Fatalf("an unreadable candidate was imported: db=%d imports=%d",
			database.calls, len(recorder.importCalls()))
	}
}

// A config hint that cannot be checked is inconclusive once the Host shows
// other evidence: a regular candidate database exists but no binary marker
// rescues the installation check. Probe must stay Present and not import.
func TestProbeKeepsPresentWhenAConfigCannotBeRead(t *testing.T) {
	const configPath = "/home/me/.config/atuin/config.toml"
	const historyPath = "/home/me/.atuin/data/history.db"
	for name, configure := range map[string]func(*fakeRemoteFiles){
		"stat": func(files *fakeRemoteFiles) {
			files.statErrs = map[string]error{configPath: fs.ErrPermission}
		},
		"open": func(files *fakeRemoteFiles) {
			files.openErrs = map[string]error{configPath: fs.ErrPermission}
		},
	} {
		t.Run(name, func(t *testing.T) {
			files := &fakeRemoteFiles{
				root:      "/home/me",
				noInstall: true,
				files: map[string]*fakeFile{
					configPath:  {content: []byte("x"), regular: true},
					historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
				},
			}
			configure(files)
			database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
			recorder := &fakeRecorder{}
			service := testService(&fakeRemoteSource{files: files}, database, recorder)

			result, err := service.Probe(context.Background(), testRef)
			if !result.Present {
				t.Fatal("an unreadable config erased the Host's Atuin presence")
			}
			if !errors.Is(err, ErrProbeUnavailable) || !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("error = %v, want ErrProbeUnavailable and fs.ErrPermission", err)
			}
			if database.calls != 0 || len(recorder.importCalls()) != 0 {
				t.Fatalf("an unreadable config still imported: db=%d imports=%d",
					database.calls, len(recorder.importCalls()))
			}
		})
	}
}

// Generic shell profiles are only hint sources. An oversized ~/.bashrc and an
// unreadable ~/.zshrc must not turn a Host with no Atuin evidence into Present:
// a false Present would permanently disable journaled history for a Host that
// does not run Atuin. The probe must be a conclusive absence and import
// nothing.
func TestProbeTreatsOversizedAndUnreadableProfilesAsNoHint(t *testing.T) {
	const bashrc = "/home/me/.bashrc"
	const zshrc = "/home/me/.zshrc"
	oversized := int64(maxHintBytes + 1)
	files := &fakeRemoteFiles{
		root:      "/home/me",
		noInstall: true,
		statErrs:  map[string]error{zshrc: fs.ErrPermission},
		files: map[string]*fakeFile{
			bashrc: {content: []byte("export PATH=$HOME/bin:$PATH\n"), size: &oversized, regular: true},
		},
	}
	database := &fakeDatabase{}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if err != nil || result != (ProbeResult{}) {
		t.Fatalf("Probe = %+v, %v, want conclusive absence", result, err)
	}
	if database.calls != 0 || len(recorder.importCalls()) != 0 {
		t.Fatalf("a profile-only Host imported: db=%d imports=%d",
			database.calls, len(recorder.importCalls()))
	}
}

// A non-not-exist Stat error on either optional side file must reject the copy
// rather than silently import a partial snapshot that bypasses the WAL/SHM salt
// check. The Host stays Present and no checkpoint advances.
func TestProbeRejectsStatErrorsOnSideFiles(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			files := &fakeRemoteFiles{
				root:     "/home/me",
				statErrs: map[string]error{historyPath + suffix: fs.ErrPermission},
				files: map[string]*fakeFile{
					historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
				},
			}
			database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
			recorder := &fakeRecorder{}
			service := testService(&fakeRemoteSource{files: files}, database, recorder)

			result, err := service.Probe(context.Background(), testRef)
			if !result.Present {
				t.Fatalf("a side-file Stat failure erased the Host's Atuin presence: %+v, %v",
					result, err)
			}
			if !errors.Is(err, ErrInconsistentCopy) {
				t.Fatalf("error = %v, want ErrInconsistentCopy", err)
			}
			if database.calls != 0 || len(recorder.importCalls()) != 0 {
				t.Fatalf("a partial snapshot was imported: db=%d imports=%d",
					database.calls, len(recorder.importCalls()))
			}
		})
	}
}

// Evidence seen, candidate not yet confirmed: the detection pass sees a
// readable config, then the candidate Stat stalls past the deadline. Close
// must unblock the Stat, and the probe must stay Present because evidence was
// already found.
func TestProbeDeadlineKeepsPresentWhenACandidateStatStalls(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root:           "/home/me",
		statBlockPath:  historyPath,
		statBlockAfter: 1,
		statBlock:      make(chan struct{}),
		statEntered:    make(chan struct{}, 1),
		statReturned:   make(chan struct{}),
		files: map[string]*fakeFile{
			"/home/me/.config/atuin/config.toml": {content: []byte("x"), regular: true},
			historyPath:                          {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	service := testService(&fakeRemoteSource{files: files}, &fakeDatabase{}, &fakeRecorder{},
		WithTimeout(50*time.Millisecond))

	start := time.Now()
	result, err := service.Probe(context.Background(), testRef)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a stalled candidate Stat completed without an error")
	}
	if !result.Present {
		t.Fatal("a stalled candidate Stat erased the evidence-seen presence")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the probe took %v, want a bounded attempt", elapsed)
	}
	select {
	case <-files.statEntered:
	default:
		t.Fatal("the fake Stat never started")
	}
	select {
	case <-files.statReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("the stalled Stat did not return after the channel closed")
	}
	files.mu.Lock()
	closed := files.closed
	files.mu.Unlock()
	if closed != 1 {
		t.Fatalf("the channel was closed %d times, want 1", closed)
	}
}

func TestProbeKeepsPresentAndRetriesWhenTheCopyChanges(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root:         "/home/me",
		changingPath: historyPath,
		files: map[string]*fakeFile{
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	source := &fakeRemoteSource{files: files}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(source, database, recorder)

	first, err := service.Probe(context.Background(), testRef)
	if !errors.Is(err, ErrInconsistentCopy) {
		t.Fatalf("first Probe error = %v, want ErrInconsistentCopy", err)
	}
	if !first.Present {
		t.Fatal("a changed copy erased the Host's Atuin presence")
	}
	if database.calls != 0 || len(recorder.importCalls()) != 0 {
		t.Fatalf("an inconsistent copy reached the journal: db=%d imports=%d",
			database.calls, len(recorder.importCalls()))
	}

	// The next Session sees a stable file and imports from the same rows.
	files.mu.Lock()
	files.changingPath = ""
	files.mu.Unlock()
	second, err := service.Probe(context.Background(), testRef)
	if err != nil {
		t.Fatalf("second Probe: %v", err)
	}
	if !second.Present || second.Imported != 1 {
		t.Fatalf("second result = %+v", second)
	}
	if len(recorder.importCalls()) != 1 {
		t.Fatalf("retry imports = %d, want 1", len(recorder.importCalls()))
	}
}

func TestProbeFailsClosedWhenTheCopiedDatabaseIsUnreadable(t *testing.T) {
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			"/home/me/.atuin/data/history.db": {content: []byte("broken"), regular: true},
		},
	}
	database := &fakeDatabase{err: ErrInconsistentCopy}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if !errors.Is(err, ErrInconsistentCopy) {
		t.Fatalf("error = %v", err)
	}
	if !result.Present {
		t.Fatal("an unreadable copy erased the Host's Atuin presence")
	}
	if len(recorder.importCalls()) != 0 {
		t.Fatal("an unreadable copy was imported")
	}
}

func TestEmptyHostIdentityIsNeverProbed(t *testing.T) {
	source := &fakeRemoteSource{files: &fakeRemoteFiles{root: "/home/me"}}
	service := testService(source, &fakeDatabase{}, &fakeRecorder{})

	if result, err := service.Probe(context.Background(), HostRef{}); err != nil || result != (ProbeResult{}) {
		t.Fatalf("Probe = %+v, %v", result, err)
	}
	if source.opened != 0 {
		t.Fatal("a Host with no identity opened a channel")
	}
}

func TestMarkNoticedIsOneTimePerHostAndDetectionDoesNotConsumeIt(t *testing.T) {
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			"/home/me/.atuin/data/history.db": {content: []byte("db"), regular: true},
		},
	}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, &fakeDatabase{}, recorder)

	if result, err := service.Probe(context.Background(), testRef); err != nil || !result.Present {
		t.Fatalf("Probe = %+v, %v", result, err)
	}
	if recorder.noticeCount() != 0 {
		t.Fatal("detection consumed the one-time notice before the Session opened")
	}
	if !service.MarkNoticed(context.Background(), testRef) {
		t.Fatal("the first successful Session did not announce the notice")
	}
	if service.MarkNoticed(context.Background(), testRef) {
		t.Fatal("the same Host announced the notice twice")
	}
}

func TestImportAfterCloseRunsInTheBackgroundWithoutANotice(t *testing.T) {
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			"/home/me/.atuin/data/history.db": {content: []byte("db"), regular: true},
		},
	}
	recorder := &fakeRecorder{onImport: make(chan struct{}, 4)}
	service := testService(&fakeRemoteSource{files: files}, &fakeDatabase{
		rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")},
	}, recorder)

	service.ImportAfterClose(testRef)
	select {
	case <-recorder.onImport:
	case <-time.After(5 * time.Second):
		t.Fatal("the post-Session import did not run")
	}
	if got := len(recorder.importCalls()); got != 1 {
		t.Fatalf("imports = %d, want 1", got)
	}
	if recorder.noticeCount() != 0 {
		t.Fatal("the post-Session import published a detection notice")
	}
}

func TestImportDoesNotMarkTheNotice(t *testing.T) {
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			"/home/me/.atuin/data/history.db": {content: []byte("db"), regular: true},
		},
	}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, &fakeDatabase{
		rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")},
	}, recorder)

	result, err := service.Import(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !result.Present || result.Imported != 1 {
		t.Fatalf("result = %+v", result)
	}
	if recorder.noticeCount() != 0 {
		t.Fatal("a post-Session import marked the one-time notice")
	}
}

func TestTempDirFailureLeavesPresentButImportsNothing(t *testing.T) {
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			"/home/me/.atuin/data/history.db": {content: []byte("db"), regular: true},
		},
	}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, &fakeDatabase{}, recorder,
		WithTempDir(func() (string, error) { return "", errors.New("no space") }))

	result, err := service.Probe(context.Background(), testRef)
	if err == nil {
		t.Fatal("Probe succeeded without a temporary directory")
	}
	if !result.Present {
		t.Fatal("a copy failure erased the Host's Atuin presence")
	}
	if len(recorder.importCalls()) != 0 {
		t.Fatal("a failed copy was imported")
	}
}

// The three files must describe one WAL generation: a mismatched WAL and SHM
// means the copy could mix committed rows from different instants. The copy is
// rejected and the recorder is never called, so the checkpoint cannot advance.
func TestProbeRejectsMismatchedWALAndSHMHeadersWithoutImporting(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			historyPath:          {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
			historyPath + "-wal": {content: testWALHeader(11, 12), modTime: time.Unix(101, 0), regular: true},
			historyPath + "-shm": {content: testSHMHeader(21, 22), modTime: time.Unix(102, 0), regular: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if !errors.Is(err, ErrInconsistentCopy) {
		t.Fatalf("error = %v, want ErrInconsistentCopy", err)
	}
	if !result.Present {
		t.Fatal("a mismatched side file erased the Host's Atuin presence")
	}
	if database.calls != 0 {
		t.Fatalf("a mismatched copy was parsed %d times", database.calls)
	}
	if len(recorder.importCalls()) != 0 {
		t.Fatal("a mismatched copy advanced the import checkpoint")
	}
}

// A file that changes after its own per-file check must still be caught by the
// full second pass over all three files.
func TestProbeCatchesAChangeAfterAFileWasCopied(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root:          "/home/me",
		changingPath:  historyPath,
		changingAfter: 6,
		files: map[string]*fakeFile{
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if !errors.Is(err, ErrInconsistentCopy) {
		t.Fatalf("error = %v, want ErrInconsistentCopy", err)
	}
	if !result.Present {
		t.Fatal("a late change erased the Host's Atuin presence")
	}
	if database.calls != 0 || len(recorder.importCalls()) != 0 {
		t.Fatalf("a late change reached the journal: db=%d imports=%d",
			database.calls, len(recorder.importCalls()))
	}
}

// SFTP calls ignore contexts. The attempt must close the channel on its
// deadline, which unblocks the read and lets the goroutine exit.
func TestProbeDeadlineClosesAStalledRemoteOpen(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root:          "/home/me",
		openBlockPath: historyPath,
		openBlock:     make(chan struct{}),
		openEntered:   make(chan struct{}, 1),
		openReturned:  make(chan struct{}),
		files: map[string]*fakeFile{
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	service := testService(&fakeRemoteSource{files: files}, &fakeDatabase{}, &fakeRecorder{},
		WithTimeout(50*time.Millisecond))

	start := time.Now()
	result, err := service.Probe(context.Background(), testRef)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a stalled remote read completed without an error")
	}
	if !result.Present {
		t.Fatal("a stalled read erased the Host's Atuin presence")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the probe took %v, want a bounded attempt", elapsed)
	}
	select {
	case <-files.openEntered:
	default:
		t.Fatal("the fake Open never started")
	}
	select {
	case <-files.openReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("the stalled Open did not return after the channel closed")
	}
	files.mu.Lock()
	closed := files.closed
	files.mu.Unlock()
	if closed != 1 {
		t.Fatalf("the channel was closed %d times, want 1", closed)
	}
}

func TestProbeRejectsAnOversizedDatabaseBeforeCopying(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			historyPath: {content: []byte("0123456789"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder,
		WithMaxTotalBytes(4))

	result, err := service.Probe(context.Background(), testRef)
	if !errors.Is(err, ErrCopyTooLarge) {
		t.Fatalf("error = %v, want ErrCopyTooLarge", err)
	}
	if !result.Present {
		t.Fatal("an oversized database erased the Host's Atuin presence")
	}
	if opened := files.openedNames(); len(opened) != 0 {
		t.Fatalf("an oversized database was opened: %v", opened)
	}
	if database.calls != 0 || len(recorder.importCalls()) != 0 {
		t.Fatal("an oversized database was imported")
	}
}

// The stat size can understate a file that grows after the check; the stream
// itself is capped too.
func TestProbeStopsACopyThatGrowsPastTheLimit(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	reported := int64(2)
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			historyPath: {
				content: []byte("0123456789"), size: &reported,
				modTime: time.Unix(100, 0), regular: true,
			},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder,
		WithMaxTotalBytes(4))

	result, err := service.Probe(context.Background(), testRef)
	if !errors.Is(err, ErrCopyTooLarge) {
		t.Fatalf("error = %v, want ErrCopyTooLarge", err)
	}
	if !result.Present {
		t.Fatal("a growing copy erased the Host's Atuin presence")
	}
	if database.calls != 0 || len(recorder.importCalls()) != 0 {
		t.Fatal("a growing copy was imported")
	}
}

// A database left behind by an uninstalled Atuin must not suppress the normal
// capture hook. Without a config, binary, or profile hint, the Host is absent.
func TestProbeTreatsAStaleDatabaseWithoutAtuinAsAbsent(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root:      "/home/me",
		noInstall: true,
		files: map[string]*fakeFile{
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if err != nil || result != (ProbeResult{}) {
		t.Fatalf("Probe = %+v, %v, want absent", result, err)
	}
	if database.calls != 0 || len(recorder.importCalls()) != 0 {
		t.Fatal("a stale database was imported without an Atuin installation")
	}
}

// A profile with an ATUIN_HOME assignment is installation evidence and a data
// directory hint, all without running the shell or Atuin.
func TestProbeUsesAnAtuinHomeHintFromAProfile(t *testing.T) {
	const historyPath = "/home/me/.atuin-custom/history.db"
	files := &fakeRemoteFiles{
		root:      "/home/me",
		noInstall: true,
		files: map[string]*fakeFile{
			"/home/me/.profile": {
				content: []byte("export PATH=$HOME/bin:$PATH\nexport ATUIN_HOME=\"$HOME/.atuin-custom\"\n"),
				regular: true,
			},
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !result.Present || result.Imported != 1 {
		t.Fatalf("result = %+v, want an import from the hinted directory", result)
	}
	if database.calls != 1 {
		t.Fatalf("database reads = %d, want 1", database.calls)
	}
}

// XDG_DATA_HOME is the other documented relocation hint; its Atuin directory
// is checked without running the shell.
func TestProbeUsesAnXDGDataHomeHintFromAProfile(t *testing.T) {
	const historyPath = "/home/me/.xdg/atuin/history.db"
	files := &fakeRemoteFiles{
		root:      "/home/me",
		noInstall: true,
		files: map[string]*fakeFile{
			"/home/me/.bashrc": {
				content: []byte("export XDG_DATA_HOME=\"$HOME/.xdg\"\n"),
				regular: true,
			},
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !result.Present || result.Imported != 1 {
		t.Fatalf("result = %+v, want an import from XDG_DATA_HOME", result)
	}
}

// A db_path in Atuin's config overrides the default directory scan.
func TestProbeUsesTheDatabasePathFromAConfig(t *testing.T) {
	const historyPath = "/home/me/custom/atuin-history.db"
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			"/home/me/.config/atuin/config.toml": {
				content: []byte("[general]\n# db_path = \"/ignored\"\ndb_path = \"~/custom/atuin-history.db\"\n"),
				regular: true,
			},
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !result.Present || result.Imported != 1 {
		t.Fatalf("result = %+v, want an import from db_path", result)
	}
}

// An unknown Atuin schema is not imported, but the Host stays present: the
// Netcatty hook must not run next to a Host's own Atuin, and a later Session
// retries from the same checkpoint.
func TestProbeFailsClosedOnAnUnknownSchema(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
		},
	}
	database := &fakeDatabase{err: domainhistory.ErrUnknownAtuinSchema}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if !errors.Is(err, domainhistory.ErrUnknownAtuinSchema) {
		t.Fatalf("error = %v, want ErrUnknownAtuinSchema", err)
	}
	if !result.Present {
		t.Fatal("an unknown schema erased the Host's Atuin presence")
	}
	if len(recorder.importCalls()) != 0 {
		t.Fatal("an unknown schema was imported")
	}
}

// An unknown schema at the first candidate does not stop detection: the next
// regular candidate is tried, and a readable one is imported.
func TestProbeTriesTheNextCandidateAfterAnUnknownSchema(t *testing.T) {
	const primary = "/home/me/.atuin/data/history.db"
	const legacy = "/home/me/.local/share/atuin/history.db"
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			primary: {content: []byte("newer"), modTime: time.Unix(100, 0), regular: true},
			legacy:  {content: []byte("legacy"), modTime: time.Unix(99, 0), regular: true},
		},
	}
	database := &fakeDatabase{
		errsByCall: []error{domainhistory.ErrUnknownAtuinSchema, nil},
		rows:       []domainhistory.AtuinRow{testRow("id-1", 100, "ls")},
	}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !result.Present || result.Imported != 1 {
		t.Fatalf("result = %+v, want an import from the readable candidate", result)
	}
	if database.calls != 2 {
		t.Fatalf("database reads = %d, want 2", database.calls)
	}
	if len(recorder.importCalls()) != 1 {
		t.Fatalf("imports = %d, want 1", len(recorder.importCalls()))
	}
}

// When an unknown schema is followed by a candidate whose read fails, the Host
// stays present and the unknown schema remains the fail-closed reason while the
// later error is preserved for diagnosis.
func TestProbeKeepsUnknownSchemaWhenALaterCandidateFailsToRead(t *testing.T) {
	const primary = "/home/me/.atuin/data/history.db"
	const legacy = "/home/me/.local/share/atuin/history.db"
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			primary: {content: []byte("newer"), modTime: time.Unix(100, 0), regular: true},
			legacy:  {content: []byte("legacy"), modTime: time.Unix(99, 0), regular: true},
		},
	}
	database := &fakeDatabase{
		errsByCall: []error{domainhistory.ErrUnknownAtuinSchema, ErrInconsistentCopy},
	}
	recorder := &fakeRecorder{}
	service := testService(&fakeRemoteSource{files: files}, database, recorder)

	result, err := service.Probe(context.Background(), testRef)
	if !errors.Is(err, domainhistory.ErrUnknownAtuinSchema) ||
		!errors.Is(err, ErrInconsistentCopy) {
		t.Fatalf("error = %v, want both ErrUnknownAtuinSchema and ErrInconsistentCopy", err)
	}
	if !result.Present {
		t.Fatal("a mixed candidate failure erased the Host's Atuin presence")
	}
	if len(recorder.importCalls()) != 0 {
		t.Fatal("a failed candidate set was imported")
	}
}

// A symlinked database is never followed, so detection cannot read outside the
// Host's own Atuin directory. The Host falls back to the capture hook.
func TestProbeDoesNotFollowASymlinkedDatabase(t *testing.T) {
	const historyPath = "/home/me/.atuin/data/history.db"
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			historyPath: {content: []byte("database"), modTime: time.Unix(100, 0), regular: true, symlink: true},
		},
	}
	database := &fakeDatabase{rows: []domainhistory.AtuinRow{testRow("id-1", 100, "ls")}}
	service := testService(&fakeRemoteSource{files: files}, database, &fakeRecorder{})

	result, err := service.Probe(context.Background(), testRef)
	if err != nil || result != (ProbeResult{}) {
		t.Fatalf("Probe = %+v, %v, want absent", result, err)
	}
	if database.calls != 0 {
		t.Fatal("a symlinked database was copied and read")
	}
}

func TestVerifyWALAndSHMAcceptsEitherSaltByteOrder(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, walName)
	shmPath := filepath.Join(dir, shmName)
	wal := testWALHeader(0x11223344, 0x55667788)
	if err := os.WriteFile(walPath, wal, 0o600); err != nil {
		t.Fatalf("write WAL: %v", err)
	}
	for name, shm := range map[string][]byte{
		"little endian": testSHMHeader(0x11223344, 0x55667788),
		"big endian": func() []byte {
			header := make([]byte, shmHeaderSize)
			header[12] = 1
			binary.BigEndian.PutUint32(header[32:36], 0x11223344)
			binary.BigEndian.PutUint32(header[36:40], 0x55667788)
			return header
		}(),
	} {
		if err := os.WriteFile(shmPath, shm, 0o600); err != nil {
			t.Fatalf("%s: write SHM: %v", name, err)
		}
		if err := verifyWALAndSHM(walPath, shmPath); err != nil {
			t.Errorf("%s: verifyWALAndSHM: %v", name, err)
		}
	}
}

func TestVerifyWALAndSHMRejectsAnUnknownWALHeader(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, walName)
	wal := make([]byte, walHeaderSize+1)
	if err := os.WriteFile(walPath, wal, 0o600); err != nil {
		t.Fatalf("write WAL: %v", err)
	}
	if err := verifyWALAndSHM(walPath, filepath.Join(dir, shmName)); !errors.Is(err, ErrInconsistentCopy) {
		t.Fatalf("error = %v, want ErrInconsistentCopy", err)
	}
}

func TestDisabledServiceReportsAbsentAndDoesNothing(t *testing.T) {
	service := New()
	if service.Enabled() {
		t.Fatal("a service without ports is enabled")
	}
	if result, err := service.Probe(context.Background(), testRef); err != nil || result != (ProbeResult{}) {
		t.Fatalf("Probe = %+v, %v", result, err)
	}
	service.ImportAfterClose(testRef)
}
