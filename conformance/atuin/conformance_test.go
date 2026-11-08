//go:build conformance

package atuinconformance

import (
	"bytes"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

// TestCLIUploadThenNetcattyDownload proves the official CLI's uploaded history
// is decrypted and projected by the real Netcatty adapter, engine, and store.
func TestCLIUploadThenNetcattyDownload(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)

	first := uniqueCommand("cli-upload-first")
	second := uniqueCommand("cli-upload-second")
	cli.addCommand(first, 0, 1500*time.Microsecond)
	cli.addCommand(second, 3, 2*time.Microsecond)
	cli.sync()

	status := server.status(t, cli.sessionToken)
	cliWriter := onlyHistoryWriter(t, status)
	if status[cliWriter]["history"] != 1 {
		t.Fatalf("CLI series max index = %d, want 1", status[cliWriter]["history"])
	}

	writer := newNetcattyWriter(t, server.baseURL, cli.sessionToken, cli.masterKey, false)
	result := writer.mustSync()
	if result.Downloaded != 2 || result.Applied != 2 {
		t.Fatalf("download/apply = %d/%d, want 2/2", result.Downloaded, result.Applied)
	}

	entries := commandsOf(writer.liveEntries())
	if len(entries) != 2 {
		t.Fatalf("Netcatty Command History has %d live rows, want 2: %v", len(entries), mapKeys(entries))
	}
	firstEntry := requireCommand(t, entries, first)
	secondEntry := requireCommand(t, entries, second)
	for _, entry := range []domainhistory.Entry{firstEntry, secondEntry} {
		if entry.Source != domainhistory.SourceAtuinSync {
			t.Fatalf("%q source = %q, want %q", entry.Command, entry.Source, domainhistory.SourceAtuinSync)
		}
		if entry.Scope != "remote:"+entry.Origin {
			t.Fatalf("%q scope = %q, want remote origin of %q", entry.Command, entry.Scope, entry.Origin)
		}
		if entry.HostID != "" || entry.SessionID != "" || entry.CaptureTier != 0 {
			t.Fatalf("%q leaked local context: host=%q session=%q tier=%d",
				entry.Command, entry.HostID, entry.SessionID, entry.CaptureTier)
		}
		if entry.CWD != cli.workDir {
			t.Fatalf("%q cwd = %q, want %q", entry.Command, entry.CWD, cli.workDir)
		}
	}
	if secondEntry.ExitCode != 3 || !secondEntry.HasExitCode {
		t.Fatalf("second entry exit = %d (known=%v), want 3/true", secondEntry.ExitCode, secondEntry.HasExitCode)
	}

	checkpoint := requireSeries(t, writer.metadata(), cliWriter)
	if checkpoint.AppliedIndex != 1 || checkpoint.UploadedIndex != -1 {
		t.Fatalf("CLI checkpoint = %+v, want applied 1 and no upload", checkpoint)
	}

	// A repeated sync must be idempotent and must not duplicate rows.
	result = writer.mustSync()
	if result.Downloaded != 0 || result.Applied != 0 {
		t.Fatalf("second sync downloaded/applied %d/%d, want 0/0", result.Downloaded, result.Applied)
	}
	if got := len(writer.liveEntries()); got != 2 {
		t.Fatalf("second sync left %d live rows, want 2", got)
	}
}

// TestNetcattyUploadThenCLIList proves records written by Netcatty are
// decrypted and listed by the official CLI.
func TestNetcattyUploadThenCLIList(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)

	first := uniqueCommand("netcatty-upload-first")
	second := uniqueCommand("netcatty-upload-second")
	writer := newNetcattyWriter(t, server.baseURL, cli.sessionToken, cli.masterKey, true)
	writer.record(first, 0, "/conformance/one")
	writer.record(second, 42, "/conformance/two")
	result := writer.mustSync()
	if result.Uploaded != 2 {
		t.Fatalf("first sync uploaded %d, want 2", result.Uploaded)
	}

	cli.sync()
	entries := commandsOfCLI(cli.entries())
	firstCLI, ok := entries[first]
	if !ok {
		t.Fatalf("CLI is missing %q; rows: %v", first, cli.entries())
	}
	secondCLI, ok := entries[second]
	if !ok {
		t.Fatalf("CLI is missing %q; rows: %v", second, cli.entries())
	}
	if secondCLI.Exit != 42 {
		t.Fatalf("CLI exit for %q = %d, want 42", second, secondCLI.Exit)
	}
	if secondCLI.CWD != "/conformance/two" {
		t.Fatalf("CLI cwd for %q = %q, want /conformance/two", second, secondCLI.CWD)
	}
	if secondCLI.Duration != int64(40*time.Millisecond) {
		t.Fatalf("CLI duration for %q = %d ns, want %d", second, secondCLI.Duration, int64(40*time.Millisecond))
	}
	if firstCLI.CWD != "/conformance/one" {
		t.Fatalf("CLI cwd for %q = %q, want /conformance/one", first, firstCLI.CWD)
	}

	listed := cli.listCommands()
	if !containsCommand(listed, first) || !containsCommand(listed, second) {
		t.Fatalf("atuin history list did not show both commands; listed: %v", listed)
	}

	status := server.status(t, cli.sessionToken)
	if status[writer.writerID]["history"] != 1 {
		t.Fatalf("writer series max index = %d, want 1", status[writer.writerID]["history"])
	}
}

// TestCLIDeleteThenNetcattyApply proves a tombstone written and uploaded by the
// official CLI is applied by Netcatty.
func TestCLIDeleteThenNetcattyApply(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)

	deletedCommand := uniqueCommand("cli-delete-me")
	keptCommand := uniqueCommand("cli-keep-me")
	cli.addCommand(deletedCommand, 7, time.Millisecond)
	cli.addCommand(keptCommand, 0, time.Millisecond)
	cli.sync()

	deletedID := ""
	writer := newNetcattyWriter(t, server.baseURL, cli.sessionToken, cli.masterKey, false)
	writer.mustSync()
	before := commandsOf(writer.liveEntries())
	deletedEntry := requireCommand(t, before, deletedCommand)
	requireCommand(t, before, keptCommand)
	deletedID = deletedEntry.ID

	entry, ok := cli.entryByCommand(deletedCommand)
	if !ok {
		t.Fatalf("CLI does not hold %q before prune", deletedCommand)
	}
	cli.addHistoryFilter(escapedCommandPattern(deletedCommand))
	cli.prune(entry.Session)

	if _, ok := cli.entryByCommand(deletedCommand); ok {
		t.Fatalf("CLI still holds %q after prune", deletedCommand)
	}
	if _, ok := cli.entryByCommand(keptCommand); !ok {
		t.Fatalf("CLI prune removed the unrelated command %q", keptCommand)
	}
	cli.sync()

	status := server.status(t, cli.sessionToken)
	cliWriter := onlyHistoryWriter(t, status)
	if status[cliWriter]["history"] != 2 {
		t.Fatalf("CLI series max index = %d, want 2 after two creates and one delete", status[cliWriter]["history"])
	}

	if result := writer.mustSync(); result.Applied < 1 {
		t.Fatalf("tombstone sync applied %d records, want at least 1", result.Applied)
	}
	after := commandsOf(writer.liveEntries())
	if _, ok := after[deletedCommand]; ok {
		t.Fatalf("Netcatty still shows deleted command %q", deletedCommand)
	}
	requireCommand(t, after, keptCommand)

	deleted, found := writer.entry(deletedID)
	if !found {
		t.Fatalf("Netcatty lost the tombstoned row %s", deletedID)
	}
	if deleted.DeletedAt.IsZero() {
		t.Fatalf("Netcatty row %s was not soft-deleted", deletedID)
	}
	tombstones := 0
	for _, record := range writer.records() {
		if record.Kind == domainhistory.RecordKindDelete && record.HistoryID == deletedID &&
			record.Direction == domainhistory.RecordDirectionRemote {
			tombstones++
		}
	}
	if tombstones != 1 {
		t.Fatalf("Netcatty record log holds %d downloaded tombstones for %s, want 1", tombstones, deletedID)
	}
}

// TestNetcattyDeleteThenCLIApply proves a Netcatty tombstone removes the row
// from the official CLI's history database.
func TestNetcattyDeleteThenCLIApply(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)

	deletedCommand := uniqueCommand("netcatty-delete-me")
	keptCommand := uniqueCommand("netcatty-keep-me")
	writer := newNetcattyWriter(t, server.baseURL, cli.sessionToken, cli.masterKey, true)
	deletedID := writer.record(deletedCommand, 0, "/conformance/delete")
	writer.record(keptCommand, 0, "/conformance/keep")
	writer.mustSync()

	cli.sync()
	if _, ok := cli.entryByCommand(deletedCommand); !ok {
		t.Fatalf("CLI is missing %q before the Netcatty delete", deletedCommand)
	}
	if _, ok := cli.entryByCommand(keptCommand); !ok {
		t.Fatalf("CLI is missing %q before the Netcatty delete", keptCommand)
	}

	writer.delete(deletedID)
	writer.mustSync()
	cli.sync()

	if _, ok := cli.entryByCommand(deletedCommand); ok {
		t.Fatalf("CLI still shows %q after the Netcatty tombstone", deletedCommand)
	}
	if _, ok := cli.entryByCommand(keptCommand); !ok {
		t.Fatalf("CLI lost the unrelated command %q", keptCommand)
	}
}

// TestTwoNetcattyWritersConverge drives two independent writers through the
// real adapter, engine, and store against one server. Each store must end with
// the union of both series and no duplicate rows.
func TestTwoNetcattyWritersConverge(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)

	writerA := newNetcattyWriter(t, server.baseURL, cli.sessionToken, cli.masterKey, true)
	writerB := newNetcattyWriter(t, server.baseURL, cli.sessionToken, cli.masterKey, true)
	if writerA.writerID == writerB.writerID {
		t.Fatalf("two writers share the series host %s", writerA.writerID)
	}

	commandsA := []string{uniqueCommand("writer-a-one"), uniqueCommand("writer-a-two")}
	commandsB := []string{uniqueCommand("writer-b-one"), uniqueCommand("writer-b-two")}
	for _, command := range commandsA {
		writerA.record(command, 0, "/conformance/a")
	}
	for _, command := range commandsB {
		writerB.record(command, 0, "/conformance/b")
	}

	writerA.mustSync()
	writerB.mustSync()
	writerA.mustSync()
	writerB.mustSync()

	all := append(append([]string(nil), commandsA...), commandsB...)
	for name, writer := range map[string]*netcattyWriter{"A": writerA, "B": writerB} {
		entries := writer.liveEntries()
		if len(entries) != 4 {
			t.Fatalf("writer %s has %d live rows, want 4: %v", name, len(entries), commandsOf(entries))
		}
		requireNoDuplicateIDs(t, entries)
		seen := commandsOf(entries)
		for _, command := range all {
			requireCommand(t, seen, command)
		}
		for _, command := range commandsA {
			entry := requireCommand(t, seen, command)
			wantSource := domainhistory.SourceAtuinSync
			if writer == writerA {
				wantSource = domainhistory.SourceSession
			}
			if entry.Source != wantSource {
				t.Fatalf("writer %s source of %q = %q, want %q", name, command, entry.Source, wantSource)
			}
		}
	}

	status := server.status(t, cli.sessionToken)
	if seriesCount(status) != 2 {
		t.Fatalf("server holds %d record series, want 2: %v", seriesCount(status), status)
	}
	for _, writerID := range []string{writerA.writerID, writerB.writerID} {
		if status[writerID]["history"] != 1 {
			t.Fatalf("series %s max index = %d, want 1", writerID, status[writerID]["history"])
		}
	}

	// Each writer acknowledged its own two uploads and applied the other
	// writer's two records. The applied position of a writer's own series is
	// rebuilt from acknowledged records on every sync rather than persisted by
	// a download, so only the upload position is stable local state there.
	for name, check := range map[string]struct {
		metadata domainhistory.Metadata
		own      string
		other    string
	}{
		"A": {metadata: writerA.metadata(), own: writerA.writerID, other: writerB.writerID},
		"B": {metadata: writerB.metadata(), own: writerB.writerID, other: writerA.writerID},
	} {
		if checkpoint := requireSeries(t, check.metadata, check.own); checkpoint.UploadedIndex != 1 {
			t.Fatalf("writer %s own-series checkpoint = %+v, want uploaded 1", name, checkpoint)
		}
		if checkpoint := requireSeries(t, check.metadata, check.other); checkpoint.AppliedIndex != 1 {
			t.Fatalf("writer %s peer-series checkpoint = %+v, want applied 1", name, checkpoint)
		}
	}

	// A further sync from either writer changes nothing.
	writerA.mustSync()
	if got := len(writerA.liveEntries()); got != 4 {
		t.Fatalf("writer A has %d live rows after the convergence sync, want 4", got)
	}
}

// TestWrongMasterKeyFailsClosed proves a wrong key is a key mismatch, never a
// silent skip, and leaves local data intact and recoverable.
func TestWrongMasterKeyFailsClosed(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)

	seedCommand := uniqueCommand("cli-key-seed")
	cli.addCommand(seedCommand, 0, time.Millisecond)
	cli.sync()

	wrongKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	writer := newNetcattyWriter(t, server.baseURL, cli.sessionToken, wrongKey, false)
	localCommand := uniqueCommand("netcatty-local-survives")
	localID := writer.record(localCommand, 0, "/conformance/local")
	writerID := writer.writerID

	statusBefore := server.status(t, cli.sessionToken)
	result, err := writer.sync()
	if err == nil || !errors.Is(err, historysync.ErrKeyMismatch) {
		t.Fatalf("wrong-key sync error = %v (result %+v), want ErrKeyMismatch", err, result)
	}
	if state := writer.engine.Status(); state.State != atuinmodel.SyncKeyMismatch {
		t.Fatalf("wrong-key status = %q/%q, want %q", state.State, state.Problem, atuinmodel.SyncKeyMismatch)
	}
	if writer.pendingUploads() != 1 {
		t.Fatalf("pending uploads after wrong key = %d, want 1", writer.pendingUploads())
	}
	if _, ok := writer.metadata().Series[domainhistory.SeriesKey(writerID, domainhistory.RecordTagHistory)]; ok {
		t.Fatalf("wrong-key sync advanced a local checkpoint")
	}
	if !reflect.DeepEqual(statusBefore, server.status(t, cli.sessionToken)) {
		t.Fatalf("wrong-key sync changed the server status")
	}
	local, found := writer.entry(localID)
	if !found || local.Command != localCommand || !local.DeletedAt.IsZero() {
		t.Fatalf("local row was corrupted by the wrong-key sync: %+v (found=%v)", local, found)
	}

	// The correct key recovers the same writer series and uploads the local
	// row that was never sent.
	writer.secrets.setMasterKey(cli.masterKey)
	writer.mustSync()
	if writer.pendingUploads() != 0 {
		t.Fatalf("pending uploads after recovery = %d, want 0", writer.pendingUploads())
	}
	entries := commandsOf(writer.liveEntries())
	requireCommand(t, entries, seedCommand)
	requireCommand(t, entries, localCommand)
	if writer.writerID != writerID {
		t.Fatalf("writer UUID changed from %s to %s", writerID, writer.writerID)
	}
}

// TestVersionAndCapabilityMismatchFailsClosed proves the adapter fails closed
// on an unknown major version, a missing required capability, and an enforced
// capability this client does not implement. No local record is uploaded.
func TestVersionAndCapabilityMismatchFailsClosed(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)
	seedCommand := uniqueCommand("cli-version-seed")
	cli.addCommand(seedCommand, 0, time.Millisecond)
	cli.sync()

	cases := []struct {
		name  string
		setup func(*conformanceProxy)
	}{
		{"unknown major version", func(proxy *conformanceProxy) { proxy.rewriteAtuinVersion("19.0.0") }},
		{"missing page-size capability", func(proxy *conformanceProxy) { proxy.stripPageSizeCapability() }},
		{"unknown enforced capability", func(proxy *conformanceProxy) { proxy.enforceUnknownCapability() }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			proxy := newConformanceProxy(t, server.baseURL)
			test.setup(proxy)
			writer := newNetcattyWriter(t, proxy.URL(), cli.sessionToken, cli.masterKey, false)
			localCommand := uniqueCommand("netcatty-blocked-local")
			writer.record(localCommand, 0, "/conformance/blocked")
			statusBefore := server.status(t, cli.sessionToken)

			result, err := writer.sync()
			if err == nil || !errors.Is(err, historysync.ErrIncompatibleServer) {
				t.Fatalf("mismatch sync error = %v (result %+v), want ErrIncompatibleServer", err, result)
			}
			if state := writer.engine.Status(); state.State != atuinmodel.SyncIncompatibleServer {
				t.Fatalf("mismatch status = %q/%q, want %q", state.State, state.Problem, atuinmodel.SyncIncompatibleServer)
			}
			if writer.pendingUploads() != 1 {
				t.Fatalf("pending uploads = %d, want 1", writer.pendingUploads())
			}
			if _, ok := writer.metadata().Series[domainhistory.SeriesKey(writer.writerID, domainhistory.RecordTagHistory)]; ok {
				t.Fatalf("incompatible server advanced a local checkpoint")
			}
			if !reflect.DeepEqual(statusBefore, server.status(t, cli.sessionToken)) {
				t.Fatalf("incompatible server changed the server status")
			}
		})
	}
}

// TestInterruptedUploadRetryCompletesWithoutDuplicates simulates a response
// lost after the server committed the batch: the checkpoint must not advance,
// and the retry must complete with exactly one copy of every record.
func TestInterruptedUploadRetryCompletesWithoutDuplicates(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)

	proxy := newConformanceProxy(t, server.baseURL)
	proxy.dropNextUploadResponse()
	writer := newNetcattyWriter(t, proxy.URL(), cli.sessionToken, cli.masterKey, true)

	commands := []string{
		uniqueCommand("interrupted-one"),
		uniqueCommand("interrupted-two"),
		uniqueCommand("interrupted-three"),
	}
	for _, command := range commands {
		writer.record(command, 0, "/conformance/interrupted")
	}

	result, err := writer.sync()
	if err == nil || !errors.Is(err, historysync.ErrOffline) {
		t.Fatalf("interrupted upload error = %v (result %+v), want ErrOffline", err, result)
	}
	if writer.pendingUploads() != len(commands) {
		t.Fatalf("pending uploads after the drop = %d, want %d", writer.pendingUploads(), len(commands))
	}
	if checkpoint, ok := writer.metadata().Series[domainhistory.SeriesKey(writer.writerID, domainhistory.RecordTagHistory)]; ok && checkpoint.UploadedIndex >= 0 {
		t.Fatalf("checkpoint advanced to %+v after a lost response", checkpoint)
	}
	for _, record := range writer.records() {
		if record.Direction == domainhistory.RecordDirectionLocal && !record.UploadedAt.IsZero() {
			t.Fatalf("record %s was marked uploaded after a lost response", record.ID)
		}
	}

	// The server did commit the batch even though the client never saw the
	// response.
	status := server.status(t, cli.sessionToken)
	if status[writer.writerID]["history"] != int64(len(commands)-1) {
		t.Fatalf("server max index after the drop = %d, want %d", status[writer.writerID]["history"], len(commands)-1)
	}

	// The retry may either re-send the unacknowledged batch or acknowledge it
	// from the server's copy during the download phase. Both are convergent;
	// what matters is that the lost response advanced no checkpoint and that
	// the retry finishes with one copy of every record.
	result = writer.mustSync()
	if writer.pendingUploads() != 0 {
		t.Fatalf("pending uploads after the retry = %d, want 0 (result %+v)", writer.pendingUploads(), result)
	}
	for _, record := range writer.records() {
		if record.Direction == domainhistory.RecordDirectionLocal && record.UploadedAt.IsZero() {
			t.Fatalf("record %s stayed unacknowledged after the retry", record.ID)
		}
	}
	checkpoint := requireSeries(t, writer.metadata(), writer.writerID)
	if checkpoint.UploadedIndex != int64(len(commands)-1) {
		t.Fatalf("retry checkpoint = %+v, want uploaded index %d", checkpoint, len(commands)-1)
	}
	if entries := writer.liveEntries(); len(entries) != len(commands) {
		t.Fatalf("retry left %d live rows, want %d", len(entries), len(commands))
	} else {
		requireNoDuplicateIDs(t, entries)
	}

	stored := server.records(t, cli.sessionToken, writer.writerID, domainhistory.RecordTagHistory)
	if len(stored) != len(commands) {
		t.Fatalf("server holds %d records for the writer series, want %d", len(stored), len(commands))
	}
	for index, record := range stored {
		if record.Index != uint64(index) {
			t.Fatalf("server record order = index %d at position %d", record.Index, index)
		}
	}
	histories := decodeServerHistories(t, cli.masterKey, stored)
	seen := make(map[string]int)
	for _, history := range histories {
		if history.History == nil {
			t.Fatalf("writer series contains a non-create record")
		}
		seen[history.History.Command]++
	}
	for _, command := range commands {
		if seen[command] != 1 {
			t.Fatalf("server command %q appears %d times, want exactly 1", command, seen[command])
		}
	}

	// One more sync after the retry must be a no-op.
	if result = writer.mustSync(); result.Uploaded != 0 {
		t.Fatalf("post-retry sync uploaded %d records, want 0", result.Uploaded)
	}
}

// TestUnknownTagDoesNotAffectHistorySync seeds a non-history record and proves
// the adapter never downloads or uploads it and the engine never checkpoints
// its series.
func TestUnknownTagDoesNotAffectHistorySync(t *testing.T) {
	env := newTestEnv(t)
	server := startConformanceServer(t, env)
	cli := newCLIClient(t, env, server)

	seedCommand := uniqueCommand("cli-unknown-tag-seed")
	cli.addCommand(seedCommand, 0, time.Millisecond)
	cli.sync()

	const unknownTag = "conformance-unknown"
	const unknownPayload = "non-history payload that must never become a command"
	unknownHost := seedNonHistoryRecord(t, server, cli.sessionToken, cli.masterKey, unknownTag, unknownPayload)

	recordingProxy := newConformanceProxy(t, server.baseURL)
	writer := newNetcattyWriter(t, recordingProxy.URL(), cli.sessionToken, cli.masterKey, false)
	localCommand := uniqueCommand("netcatty-history-with-unknown-tag")
	writer.record(localCommand, 0, "/conformance/unknown-tag")
	writer.mustSync()

	entries := writer.liveEntries()
	for _, entry := range entries {
		if entry.Command == unknownPayload {
			t.Fatalf("the non-history payload became a Netcatty command")
		}
	}
	if len(entries) != 2 {
		t.Fatalf("Netcatty Command History has %d live rows, want 2: %v", len(entries), commandsOf(entries))
	}
	seen := commandsOf(entries)
	requireCommand(t, seen, seedCommand)
	requireCommand(t, seen, localCommand)

	metadata := writer.metadata()
	if _, ok := metadata.Series[domainhistory.SeriesKey(unknownHost, unknownTag)]; ok {
		t.Fatalf("engine created a checkpoint for the non-history series %s/%s", unknownHost, unknownTag)
	}
	for _, tag := range recordingProxy.requestedTags() {
		if tag != domainhistory.RecordTagHistory {
			t.Fatalf("adapter requested tag %q; only history may be downloaded", tag)
		}
	}
	if len(recordingProxy.requestedTags()) == 0 {
		t.Fatalf("test did not observe a history download request")
	}

	unknownRecords := server.records(t, cli.sessionToken, unknownHost, unknownTag)
	if len(unknownRecords) != 1 || unknownRecords[0].Tag != unknownTag {
		t.Fatalf("server non-history series changed: %+v", unknownRecords)
	}
	if payload := decryptPayload(t, cli.masterKey, unknownRecords[0]); payload != unknownPayload {
		t.Fatalf("server non-history payload = %q, want %q", payload, unknownPayload)
	}

	// A repeated sync is still stable and still never requests the other tag.
	writer.mustSync()
	if got := len(writer.liveEntries()); got != 2 {
		t.Fatalf("repeated sync left %d live rows, want 2", got)
	}
	for _, tag := range recordingProxy.requestedTags() {
		if tag != domainhistory.RecordTagHistory {
			t.Fatalf("repeated sync requested tag %q", tag)
		}
	}
}

// ---------------------------------------------------------------------------
// Test-only helpers
// ---------------------------------------------------------------------------

// onlyHistoryWriter returns the single Writer UUID that owns a history series
// in the server's record status.
func onlyHistoryWriter(t *testing.T, status map[string]map[string]int64) string {
	t.Helper()
	writer := ""
	for candidate, tags := range status {
		if _, ok := tags["history"]; !ok {
			continue
		}
		if writer != "" {
			t.Fatalf("expected one history series, found %v", status)
		}
		writer = candidate
	}
	if writer == "" {
		t.Fatalf("server reports no history series: %v", status)
	}
	return writer
}

func containsCommand(commands []string, want string) bool {
	for _, command := range commands {
		if command == want {
			return true
		}
	}
	return false
}

func seedNonHistoryRecord(t *testing.T, server *conformanceServer, token, masterKey, tag, payload string) string {
	t.Helper()
	key := testWireKey(t, masterKey)
	host := domainhistory.UUIDv7()
	record := wire.Record{
		ID:         domainhistory.UUIDv7(),
		Index:      0,
		SeriesHost: host,
		Timestamp:  uint64(time.Now().UnixNano()),
		Version:    string(wire.Version1),
		Tag:        tag,
	}
	encrypted, err := record.Encrypt([]byte(payload), key)
	if err != nil {
		t.Fatalf("encrypt non-history record: %v", err)
	}
	server.postRecords(t, token, []wire.Record{encrypted})
	return host
}

func decryptPayload(t *testing.T, masterKey string, record wire.Record) string {
	t.Helper()
	payload, err := record.Decrypt(testWireKey(t, masterKey))
	if err != nil {
		t.Fatalf("decrypt record %s: %v", record.ID, err)
	}
	return string(payload)
}
