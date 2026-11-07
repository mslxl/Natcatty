package hostatuin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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
}

// fakeRemoteFiles records every read-only operation. It deliberately has no
// write or execute operation: the importer's port cannot ask for one.
type fakeRemoteFiles struct {
	root    string
	rootErr error
	files   map[string]*fakeFile
	// changingPath gains a modification-time change after its first copy stat,
	// simulating Atuin writing the database while the copy is in flight.
	changingPath string

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
	defer f.mu.Unlock()
	f.stats = append(f.stats, name)
	if f.statCalls == nil {
		f.statCalls = make(map[string]int)
	}
	f.statCalls[name]++
	file, ok := f.files[name]
	if !ok {
		return FileInfo{}, os.ErrNotExist
	}
	info := FileInfo{Size: int64(len(file.content)), ModTime: file.modTime, Regular: file.regular}
	if name == f.changingPath && f.statCalls[name] >= 3 {
		info.ModTime = info.ModTime.Add(time.Second)
	}
	return info, nil
}

func (f *fakeRemoteFiles) Open(_ context.Context, name string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, name)
	file, ok := f.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(file.content)), nil
}

func (f *fakeRemoteFiles) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
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
	rows      []domainhistory.AtuinRow
	err       error
	calls     int
	dirs      []string
	copied    map[string][]byte
	copiedSet bool
}

func (d *fakeDatabase) ReadHistory(_ context.Context, databasePath string) ([]domainhistory.AtuinRow, error) {
	d.calls++
	dir := filepath.Dir(databasePath)
	d.dirs = append(d.dirs, dir)
	if d.err != nil {
		return nil, d.err
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
	files := &fakeRemoteFiles{
		root: "/home/me",
		files: map[string]*fakeFile{
			historyPath:          {content: []byte("database"), modTime: time.Unix(100, 0), regular: true},
			historyPath + "-wal": {content: []byte("wal"), modTime: time.Unix(101, 0), regular: true},
			historyPath + "-shm": {content: []byte("shm"), modTime: time.Unix(102, 0), regular: true},
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
		string(database.copied[walName]) != "wal" ||
		string(database.copied[shmName]) != "shm" {
		t.Fatalf("copied files = %q / %q / %q",
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

func TestPreferenceOffSkipsDetectionAndImport(t *testing.T) {
	source := &fakeRemoteSource{files: &fakeRemoteFiles{root: "/home/me"}}
	database := &fakeDatabase{}
	recorder := &fakeRecorder{}
	service := testService(source, database, recorder, WithPreference(func() bool { return false }))

	if service.Enabled() {
		t.Fatal("the service is enabled with the preference off")
	}
	result, err := service.Probe(context.Background(), testRef)
	if err != nil || result != (ProbeResult{}) {
		t.Fatalf("Probe = %+v, %v", result, err)
	}
	if source.opened != 0 || database.calls != 0 {
		t.Fatalf("preference off still detected: source=%d db=%d", source.opened, database.calls)
	}
	if imported, err := service.Import(context.Background(), testRef); err != nil || imported != (ImportResult{}) {
		t.Fatalf("Import = %+v, %v", imported, err)
	}
	if len(recorder.importCalls()) != 0 || recorder.noticeCount() != 0 {
		t.Fatal("preference off still imported or announced")
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
