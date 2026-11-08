package historysync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

// Sync engine errors. They never carry a token, key, server response body, or
// record payload; the adapter sentinels in remote.go are reused where they
// already name the failure.
var (
	// ErrSyncDisabled reports a manual sync while the user has turned history
	// sync off.
	ErrSyncDisabled = errors.New("historysync: sync disabled")
	// ErrAccountMismatch reports a remote account that does not match the
	// stored account identity. Local records are never sent to another account
	// by accident.
	ErrAccountMismatch = errors.New("historysync: remote account does not match the stored account")
	// ErrUploadConflict reports a duplicate series index whose server metadata
	// does not match the local record.
	ErrUploadConflict = errors.New("historysync: upload conflict does not match the local record")
	// ErrKeyUnverified reports an account with no remote history whose master
	// key the user has not explicitly confirmed. Uploading would make the
	// first record decide the account key, so the engine waits for the user.
	ErrKeyUnverified = errors.New("historysync: master key is not confirmed")
	// ErrIncompletePage reports a download page that made no progress or broke
	// series index continuity. The checkpoint is never advanced for it.
	ErrIncompletePage = errors.New("historysync: incomplete or discontinuous remote page")
	// ErrAppLocked reports a sync trigger while App Lock hides the app. No
	// credential is read and no remote is opened.
	ErrAppLocked = errors.New("historysync: app lock is engaged")
)

// SyncResult counts one sync operation. Applied counts downloaded records
// whose projection was committed; Uploaded counts local records the server
// acknowledged, including a matching duplicate conflict; PendingUploads is
// what remains after the run.
type SyncResult struct {
	Trigger        Trigger
	Downloaded     int
	Applied        int
	Uploaded       int
	PendingUploads int
	LastSyncAt     time.Time
}

// Trigger names why a sync ran. The values are stable for tests and status.
type Trigger string

const (
	TriggerStartup  Trigger = "startup"
	TriggerFocus    Trigger = "focus"
	TriggerInterval Trigger = "interval"
	TriggerManual   Trigger = "manual"
	TriggerCapture  Trigger = "capture"
	TriggerRetry    Trigger = "retry"
)

// SyncStatus is the engine's sanitized view. It never contains the Session
// Token, Master Key, server response text, command text, or record payload.
type SyncStatus struct {
	State          atuinmodel.SyncState
	Problem        atuinmodel.Problem
	ServerURL      string
	ServerVersion  string
	LastSyncAt     time.Time
	PendingUploads int
	NextRetryAt    time.Time
}

// RemoteDelete is one downloaded tombstone applied to the projection. A
// tombstone for a history id this device never saw is still stored in the
// record log; there is simply no projection to update.
type RemoteDelete struct {
	HistoryID string
	DeletedAt time.Time
}

// RemotePage is one page applied in a single local transaction. Checkpoint is
// committed with the records and projections, so the engine never advances
// past a page that did not commit.
type RemotePage struct {
	Checkpoint domainhistory.SeriesCheckpoint
	Records    []domainhistory.Record
	Entries    []domainhistory.Entry
	Deletes    []RemoteDelete
	// Reindexes moves unsynced local captures out of index positions the
	// downloaded records claim. It commits with the page, so a capture is never
	// lost or falsely acknowledged between two transactions.
	Reindexes []Reindex
	// Acknowledged names local records this page proved the server already
	// holds: a downloaded record described the same event at the same index.
	// They are marked uploaded in the page transaction. The record ids may
	// differ from the server copy, so resending one would look like a
	// conflicting duplicate; only the download can establish this proof.
	Acknowledged []string
	// AcknowledgedAt is the upload timestamp recorded for Acknowledged. It is
	// required when Acknowledged is non-empty.
	AcknowledgedAt time.Time
}

// Reindex assigns a new series index to one local record that has not been
// uploaded. A restore or an offline capture can otherwise collide with a
// record the server already stores at that index.
type Reindex struct {
	RecordID string
	Index    int64
}

// SyncStore is the local persistence port the sync engine needs. The SQLite
// store implements it; the engine's tests use an in-memory record log. The
// three write methods are atomic: records, projections, and checkpoints commit
// or roll back together.
type SyncStore interface {
	HistoryMetadata(context.Context) (domainhistory.Metadata, error)
	ListHistoryRecords(context.Context) ([]domainhistory.Record, error)
	ApplyRemotePage(context.Context, RemotePage) error
	MarkUploaded(context.Context, []string, time.Time, domainhistory.SeriesCheckpoint) error
	ResetUploaded(context.Context, domainhistory.SeriesCheckpoint) error
}

// Timer is one scheduled callback. Stop cancels it; the callback must not run
// after Stop returns when the implementation can guarantee it.
type Timer interface {
	Stop()
}

// Scheduler is the engine's timing seam. The app uses real time; tests drive
// a manual scheduler so startup, focus, interval, debounce, and retry are
// deterministic without sleeps.
type Scheduler interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}

type realScheduler struct{}

func (realScheduler) Now() time.Time { return time.Now() }

func (realScheduler) AfterFunc(delay time.Duration, fn func()) Timer {
	return realTimer{timer: time.AfterFunc(delay, fn)}
}

type realTimer struct {
	timer *time.Timer
}

func (timer realTimer) Stop() { timer.timer.Stop() }

// Default scheduling values. Focus and capture debounces are deliberately
// short: a capture never waits for the network, and a focus only coalesces the
// burst of redraw activity into one sync.
const (
	DefaultCaptureDebounce = 2 * time.Second
	DefaultFocusDebounce   = 5 * time.Second
	DefaultPageSize        = 100
	maxPageSize            = 1000
)

// retryDelays is the exponential backoff sequence from the product spec,
// capped at thirty minutes. Authentication, key, account, conflict, and
// incompatible-server failures never reach it.
var retryDelays = []time.Duration{
	30 * time.Second,
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

// Option customizes a Service.
type Option func(*Service)

// WithScheduler replaces the clock and timer source. Tests pass a manual
// scheduler; the app keeps real time.
func WithScheduler(scheduler Scheduler) Option {
	return func(service *Service) {
		if scheduler != nil {
			service.scheduler = scheduler
		}
	}
}

// WithJitter replaces the retry jitter. The default applies +/-20 percent with
// a random source; tests pass an identity function for exact times.
func WithJitter(jitter func(time.Duration) time.Duration) Option {
	return func(service *Service) {
		if jitter != nil {
			service.jitter = jitter
		}
	}
}

// WithCaptureDebounce replaces the post-capture debounce. A zero or negative
// value keeps the default.
func WithCaptureDebounce(delay time.Duration) Option {
	return func(service *Service) {
		if delay > 0 {
			service.captureDebounce = delay
		}
	}
}

// WithFocusDebounce replaces the window-focus debounce. A zero or negative
// value keeps the default.
func WithFocusDebounce(delay time.Duration) Option {
	return func(service *Service) {
		if delay > 0 {
			service.focusDebounce = delay
		}
	}
}

// WithSyncCompleted registers a callback for every successful run, including a
// run that applied no downloaded rows. It runs on the sync goroutine outside
// every service lock and must not block. It reports only fully successful
// runs; use WithSyncApplied to learn about committed rows when a later phase
// fails.
func WithSyncCompleted(fn func(SyncResult)) Option {
	return func(service *Service) { service.onCompleted = fn }
}

// WithSyncApplied registers a callback for every run that committed downloaded
// projections, even when a later phase (such as upload) fails. It runs on the
// sync goroutine outside every service lock and must not block. Observers must
// learn about applied rows immediately; otherwise History and autocomplete
// stay stale until a later successful run.
func WithSyncApplied(fn func(SyncResult)) Option {
	return func(service *Service) { service.onApplied = fn }
}

// WithAppLocked supplies a predicate that reports whether App Lock currently
// hides the app. App Lock pauses sync exactly like a locked field-secret
// store, while capture continues. A nil predicate keeps sync unpaused.
func WithAppLocked(locked func() bool) Option {
	return func(service *Service) {
		if locked != nil {
			service.appLocked = locked
		}
	}
}

// Service is the background history sync engine. It downloads before it
// uploads, applies one remote page per transaction, advances checkpoints only
// after a commit, uploads the local writer series in index order, and coalesces
// concurrent triggers into at most one queued run.
//
// Capture only calls NotifyLocalChange; it never waits for the network.
type Service struct {
	settings AccountSettings
	secrets  Secrets
	store    SyncStore
	factory  Factory

	scheduler       Scheduler
	jitter          func(time.Duration) time.Duration
	captureDebounce time.Duration
	focusDebounce   time.Duration
	onCompleted     func(SyncResult)
	onApplied       func(SyncResult)
	appLocked       func() bool

	// statusMu protects cached. Status reads it without running a sync.
	statusMu sync.Mutex
	cached   SyncStatus

	// mu protects the scheduler, trigger, and listener state. It is never held
	// across a remote call or a store transaction.
	mu           sync.Mutex
	version      uint64
	listeners    map[int]func()
	nextListener int

	started bool
	stopped bool
	running bool
	pending *pendingRun
	// idle is closed while no run is active and none is queued. WaitIdle uses
	// it so tests do not need a fixed sleep.
	idle chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	// runCancel cancels the in-flight run, including a manual SyncNow started
	// with context.Background(). Stop uses it so shutdown never leaves network
	// work running.
	runCancel context.CancelFunc
	// terminal records an auth, key, or version failure that waits for the
	// user. Automatic triggers stay parked until NotifyConfigurationChanged or
	// a manual SyncNow clears it.
	terminal error

	intervalTimer Timer
	retryTimer    Timer
	focusTimer    Timer
	captureTimer  Timer
	retryAttempt  int
}

// pendingRun is a trigger received while another sync was running. Its
// waiters are manual callers that coalesced into that next run.
type pendingRun struct {
	trigger Trigger
	ctx     context.Context
	waiters []chan runOutcome
}

type runOutcome struct {
	result SyncResult
	err    error
}

// NewService returns a sync engine over the supplied settings, secret store,
// local sync store, and Remote factory. A nil factory makes every operation
// fail closed as unavailable; the HTTP adapter arrives in a later ticket.
func NewService(settings AccountSettings, secrets Secrets, store SyncStore,
	factory Factory, opts ...Option) *Service {
	idle := make(chan struct{})
	close(idle)
	service := &Service{
		settings:        settings,
		secrets:         secrets,
		store:           store,
		factory:         factory,
		scheduler:       realScheduler{},
		jitter:          jittered,
		captureDebounce: DefaultCaptureDebounce,
		focusDebounce:   DefaultFocusDebounce,
		listeners:       make(map[int]func()),
		idle:            idle,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Start begins scheduled sync: one startup run, the configured interval, and
// the timers used by focus, capture, and retry triggers. Start is idempotent
// and does not block the caller.
func (s *Service) Start(parent context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(parent)
	ctx := s.ctx
	s.mu.Unlock()

	s.scheduleInterval()
	s.enqueue(ctx, TriggerStartup, nil)
}

// Stop ends scheduled work and cancels an in-flight remote operation started
// by a scheduled trigger. It is idempotent.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	runCancel := s.runCancel
	s.runCancel = nil
	timers := []Timer{s.intervalTimer, s.retryTimer, s.focusTimer, s.captureTimer}
	s.intervalTimer, s.retryTimer, s.focusTimer, s.captureTimer = nil, nil, nil, nil
	waiters := []chan runOutcome(nil)
	if s.pending != nil {
		waiters = append(waiters, s.pending.waiters...)
		s.pending = nil
	}
	s.mu.Unlock()

	for _, timer := range timers {
		if timer != nil {
			timer.Stop()
		}
	}
	if runCancel != nil {
		runCancel()
	}
	if cancel != nil {
		cancel()
	}
	for _, waiter := range waiters {
		waiter <- runOutcome{err: context.Canceled}
	}
}

// NotifyConfigurationChanged clears a terminal auth, key, or server-version
// failure so the next automatic trigger may try again. The app calls it after
// a successful login, key change, logout, or History settings change. It never
// runs a sync itself and never touches credentials.
func (s *Service) NotifyConfigurationChanged() {
	s.clearTerminalFailure()
}

// NotifyFocus requests a debounced sync after the window regains focus. It
// never blocks and is a no-op before Start or after Stop.
func (s *Service) NotifyFocus() {
	s.mu.Lock()
	if !s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	if s.focusTimer != nil {
		s.focusTimer.Stop()
	}
	ctx := s.ctx
	s.focusTimer = s.scheduler.AfterFunc(s.focusDebounce, func() {
		s.enqueue(ctx, TriggerFocus, nil)
	})
	s.mu.Unlock()
}

// NotifyLocalChange requests a short debounce after a history mutation when
// the engine is idle. While a sync is running it queues the next run instead,
// so a burst of captures coalesces into one follow-up sync.
func (s *Service) NotifyLocalChange() {
	s.mu.Lock()
	if !s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	if s.running {
		if s.pending == nil {
			s.pending = &pendingRun{trigger: TriggerCapture, ctx: s.ctx}
		} else {
			s.pending.trigger = preferTrigger(s.pending.trigger, TriggerCapture)
		}
		s.mu.Unlock()
		return
	}
	if s.captureTimer != nil {
		s.captureTimer.Stop()
	}
	ctx := s.ctx
	s.captureTimer = s.scheduler.AfterFunc(s.captureDebounce, func() {
		s.enqueue(ctx, TriggerCapture, nil)
	})
	s.mu.Unlock()
}

// SyncNow runs one manual sync and waits for the run that covers the trigger.
// Concurrent triggers coalesce into the pending run.
func (s *Service) SyncNow(ctx context.Context) (SyncResult, error) {
	return s.Sync(ctx, TriggerManual)
}

// Sync runs one sync and waits for its outcome. A trigger received while
// another run is active joins the queued run rather than racing it.
func (s *Service) Sync(ctx context.Context, trigger Trigger) (SyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if trigger == "" {
		trigger = TriggerManual
	}
	waiter := make(chan runOutcome, 1)
	s.enqueue(ctx, trigger, waiter)
	select {
	case outcome := <-waiter:
		return outcome.result, outcome.err
	case <-ctx.Done():
		return SyncResult{Trigger: trigger}, ctx.Err()
	}
}

// WaitIdle blocks until no sync is running and none is queued, or ctx ends.
func (s *Service) WaitIdle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status returns the last local and remote sync state. It never performs a
// network request and never decrypts a credential.
func (s *Service) Status() SyncStatus {
	status, _, _ := s.localStatus(context.Background())
	cached := s.cachedStatus()

	status.ServerVersion = cached.ServerVersion
	status.LastSyncAt = cached.LastSyncAt
	if status.State == atuinmodel.SyncIdle {
		switch cached.State {
		case atuinmodel.SyncSyncing, atuinmodel.SyncOffline, atuinmodel.SyncAuthExpired,
			atuinmodel.SyncKeyMismatch, atuinmodel.SyncIncompatibleServer,
			atuinmodel.SyncError:
			status.State = cached.State
			status.Problem = cached.Problem
			status.NextRetryAt = cached.NextRetryAt
		}
	}
	switch status.State {
	case atuinmodel.SyncDisabled, atuinmodel.SyncLocked, atuinmodel.SyncNotConfigured:
		// A local disabled/locked/not-configured state is authoritative and
		// carries no retry: nothing is scheduled in those states.
		status.NextRetryAt = time.Time{}
	}
	return status
}

// Subscribe registers fn for status changes. The callback runs outside every
// service lock and only asks the UI for a redraw. It returns an unsubscribe
// function.
func (s *Service) Subscribe(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	id := s.nextListener
	s.nextListener++
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// Revision increments on every status change, so a UI that misses a callback
// can still observe that a reload is needed.
func (s *Service) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

func (s *Service) cachedStatus() SyncStatus {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.cached
}

func (s *Service) setStatus(status SyncStatus) {
	s.statusMu.Lock()
	s.cached = status
	s.statusMu.Unlock()

	s.mu.Lock()
	s.version++
	listeners := make([]func(), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

// enqueue starts a worker when idle, or records a coalesced pending run.
func (s *Service) enqueue(ctx context.Context, trigger Trigger, waiter chan runOutcome) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		if waiter != nil {
			waiter <- runOutcome{err: context.Canceled}
		}
		return
	}
	if s.running {
		if s.pending == nil {
			s.pending = &pendingRun{trigger: trigger, ctx: ctx}
		} else {
			s.pending.trigger = preferTrigger(s.pending.trigger, trigger)
		}
		if waiter != nil {
			s.pending.waiters = append(s.pending.waiters, waiter)
		}
		s.mu.Unlock()
		return
	}
	s.running = true
	s.idle = make(chan struct{})
	waiters := []chan runOutcome(nil)
	if waiter != nil {
		waiters = append(waiters, waiter)
	}
	s.mu.Unlock()
	go s.worker(ctx, trigger, waiters)
}

func (s *Service) worker(ctx context.Context, trigger Trigger, waiters []chan runOutcome) {
	for {
		runCtx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			cancel()
			outcome := runOutcome{err: context.Canceled}
			for _, waiter := range waiters {
				waiter <- outcome
			}
			s.finishWorker()
			return
		}
		s.runCancel = cancel
		s.mu.Unlock()

		result, err := s.run(runCtx, trigger)
		cancel()
		s.mu.Lock()
		s.runCancel = nil
		s.mu.Unlock()

		outcome := runOutcome{result: result, err: err}
		for _, waiter := range waiters {
			waiter <- outcome
		}

		s.mu.Lock()
		next := s.pending
		s.pending = nil
		if next == nil {
			s.running = false
			close(s.idle)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		ctx, trigger, waiters = next.ctx, next.trigger, next.waiters
	}
}

// finishWorker marks the engine idle after a worker stopped before running.
func (s *Service) finishWorker() {
	s.mu.Lock()
	s.running = false
	close(s.idle)
	s.mu.Unlock()
}

// preferTrigger keeps the strongest reason for a coalesced run. Manual wins so
// a user action is never reported as a routine capture sync.
func preferTrigger(current, next Trigger) Trigger {
	rank := map[Trigger]int{
		TriggerManual:   6,
		TriggerStartup:  5,
		TriggerInterval: 4,
		TriggerRetry:    3,
		TriggerFocus:    2,
		TriggerCapture:  1,
	}
	if rank[next] > rank[current] {
		return next
	}
	return current
}

// run is one download-before-upload sync. It never holds a service lock across
// a remote call or a store transaction.
func (s *Service) run(ctx context.Context, trigger Trigger) (SyncResult, error) {
	result := SyncResult{Trigger: trigger}
	if s.isStopped() {
		return result, context.Canceled
	}
	if trigger == TriggerManual {
		// A manual run is the user's explicit retry, even after an auth, key,
		// or server-version failure.
		s.clearTerminalFailure()
	} else if failure := s.terminalFailure(); failure != nil {
		// These failures wait for the user. Scheduled, focus, capture, and
		// interval triggers must not keep opening remotes until the account,
		// key, or server configuration changes.
		result.PendingUploads = s.cachedStatus().PendingUploads
		return result, failure
	}

	status, metadata, err := s.localStatus(ctx)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	result.PendingUploads = status.PendingUploads
	switch status.State {
	case atuinmodel.SyncDisabled:
		s.clearRetry()
		s.setStatus(status)
		return result, ErrSyncDisabled
	case atuinmodel.SyncLocked:
		s.clearRetry()
		s.setStatus(status)
		if s.appLockEngaged() {
			return result, ErrAppLocked
		}
		return result, ErrSecretsLocked
	case atuinmodel.SyncNotConfigured:
		s.clearRetry()
		s.setStatus(status)
		return result, ErrNotConfigured
	}

	syncing := status
	syncing.State = atuinmodel.SyncSyncing
	syncing.Problem = atuinmodel.ProblemNone
	syncing.NextRetryAt = time.Time{}
	previous := s.cachedStatus()
	syncing.ServerVersion = previous.ServerVersion
	syncing.LastSyncAt = previous.LastSyncAt
	s.setStatus(syncing)

	// Re-read the configuration the credentials are about to be sent to. The
	// status snapshot above must not authorize a different address that the
	// user selected in the meantime.
	settings := s.settings.HistorySettings()
	address, err := NormalizeSyncAddress(settings.SyncAddress)
	if err != nil {
		return result, s.fail(status, ErrInvalidSyncAddress, &result)
	}
	if !settings.SyncEnabled {
		return result, s.fail(status, ErrSyncDisabled, &result)
	}
	// Re-read the stored account before reading any credential. The status
	// snapshot may be stale: another actor on the same server (a second
	// window, the CLI, or the account service) can change the stored account
	// after the snapshot. Comparing the address alone is not enough because
	// the stale identity still names the address the user selected. The
	// session token and master key must never be released to a remote before
	// this check fails closed.
	stored, err := s.store.HistoryMetadata(ctx)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	if stored.Account != metadata.Account {
		return result, s.fail(status, ErrAccountMismatch, &result)
	}
	metadata = stored
	if address != status.ServerURL || !sameAccountAddress(metadata.Account, address) {
		return result, s.fail(status, ErrAccountMismatch, &result)
	}
	if err := s.stopOrPause(ctx); err != nil {
		return result, s.fail(status, err, &result)
	}
	token, err := s.secrets.Get(ctx, SessionTokenRef())
	if err != nil {
		return result, s.fail(status, publicSecretError(err), &result)
	}
	key, keyPresent, err := s.loadMasterKey(ctx)
	if err != nil {
		return result, s.fail(status, publicSecretError(err), &result)
	}
	if !keyPresent {
		return result, s.fail(status, ErrNotConfigured, &result)
	}
	defer domainhistory.WipeMasterKey(key)

	config := Config{
		BaseURL: address, AuthMode: atuinmodel.NormalizeAuthMode(settings.AuthMode),
		Token: token, MasterKey: key,
	}
	defer config.Wipe()
	remote, err := s.openRemote(ctx, config)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	defer func() { _ = remote.Close() }()

	auth, err := remote.Authenticate(ctx)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	if err := storedAccountMatches(metadata.Account, address, auth.Account); err != nil {
		return result, s.fail(status, err, &result)
	}
	capabilities, err := remote.Capabilities(ctx)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	if err := capabilities.Check(); err != nil {
		return result, s.fail(status, err, &result)
	}
	status.ServerVersion = capabilities.ServerVersion
	status.ServerURL = address

	statuses, err := remote.RecordStatus(ctx)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	if HasHistory(statuses) {
		// Never upload with a key that cannot decrypt the account's existing
		// history. A real adapter discovers the same mismatch while decrypting
		// the first downloaded page; validating up front keeps the failure
		// before any local mutation.
		validation, err := remote.ValidateMasterKey(ctx)
		if err != nil {
			return result, s.fail(status, err, &result)
		}
		if validation != KeyValidationVerified {
			return result, s.fail(status, ErrKeyMismatch, &result)
		}
	}

	// Download before upload, including other writers' tombstones, so this
	// device never re-creates something another device already deleted.
	downloaded, applied, err := s.download(ctx, remote, capabilities, statuses, &metadata)
	result.Downloaded, result.Applied = downloaded, applied
	if applied > 0 && s.onApplied != nil {
		// The projections are committed. Announce them even if a later upload
		// phase fails; otherwise the History surface and autocomplete stay
		// stale until some later successful run.
		s.onApplied(result)
	}
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	if err := s.stopOrPause(ctx); err != nil {
		return result, s.fail(status, err, &result)
	}

	localRecords, err := s.store.ListHistoryRecords(ctx)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	pending, err := s.prepareUpload(ctx, localRecords, statuses, metadata)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	if pending > 0 && !HasHistory(statuses) && !metadata.UnverifiedKeyConfirmed {
		// The first upload establishes the account key. The account panel
		// requires an explicit confirmation before that happens.
		blocked := status
		blocked.State = atuinmodel.SyncIdle
		blocked.Problem = atuinmodel.ProblemKeyUnverified
		blocked.PendingUploads = pending
		s.clearRetry()
		s.setStatus(blocked)
		return result, ErrKeyUnverified
	}
	if err := s.stopOrPause(ctx); err != nil {
		return result, s.fail(status, err, &result)
	}

	uploaded, err := s.upload(ctx, remote, capabilities, statuses, metadata)
	result.Uploaded = uploaded
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	if err := s.stopOrPause(ctx); err != nil {
		return result, s.fail(status, err, &result)
	}

	final, _, err := s.localStatus(ctx)
	if err != nil {
		return result, s.fail(status, err, &result)
	}
	if final.State == atuinmodel.SyncIdle {
		final.Problem = atuinmodel.ProblemNone
	}
	// A lock that engaged during the run stays visible: never overwrite a
	// locked (or otherwise blocked) local state with idle.
	final.ServerVersion = capabilities.ServerVersion
	final.ServerURL = address
	final.LastSyncAt = s.scheduler.Now()
	final.NextRetryAt = time.Time{}
	result.PendingUploads = final.PendingUploads
	result.LastSyncAt = final.LastSyncAt
	s.clearRetry()
	s.clearTerminalFailure()
	s.setStatus(final)
	if s.onCompleted != nil {
		s.onCompleted(result)
	}
	return result, nil
}

func (s *Service) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *Service) appLockEngaged() bool {
	return s.appLocked != nil && s.appLocked()
}

// stopOrPause reports shutdown or App Lock before another phase starts. A
// locked UI is a pause, not a sync error to retry.
func (s *Service) stopOrPause(ctx context.Context) error {
	if s.isStopped() {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.appLockEngaged() {
		return ErrAppLocked
	}
	return nil
}

func (s *Service) terminalFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
}

func (s *Service) setTerminalFailure(err error) {
	s.mu.Lock()
	s.terminal = err
	s.mu.Unlock()
}

func (s *Service) clearTerminalFailure() {
	s.mu.Lock()
	s.terminal = nil
	s.mu.Unlock()
}

// terminalSyncError reports a failure that only the user can fix. Automatic
// triggers wait until NotifyConfigurationChanged or a manual run.
func terminalSyncError(err error) bool {
	switch {
	case errors.Is(err, ErrAuthExpired), errors.Is(err, ErrAuthFailed):
		return true
	case errors.Is(err, ErrKeyMismatch):
		return true
	case errors.Is(err, ErrIncompatibleServer):
		return true
	default:
		return false
	}
}

// localStatus computes the status from device-local state only.
func (s *Service) localStatus(ctx context.Context) (SyncStatus, domainhistory.Metadata, error) {
	settings := s.settings.HistorySettings()
	status := SyncStatus{
		State:     atuinmodel.SyncNotConfigured,
		Problem:   atuinmodel.ProblemNotConfigured,
		ServerURL: settings.SyncAddress,
	}
	metadata, err := s.store.HistoryMetadata(ctx)
	if err != nil {
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		return status, metadata, err
	}
	records, recordErr := s.store.ListHistoryRecords(ctx)
	if recordErr == nil {
		status.PendingUploads = pendingUploads(records, metadata.WriterUUID)
	}

	if s.appLockEngaged() {
		// The spec pauses sync while App Lock hides the app, just like a
		// locked field-secret store. Capture continues because it never needs
		// a credential. Check this before Mode so App Lock performs no
		// secret-store query at all, matching AccountService.localStatus.
		status.State = atuinmodel.SyncLocked
		status.Problem = atuinmodel.ProblemLocked
		return status, metadata, nil
	}
	switch s.secrets.Mode() {
	case SecretModePassphraseLocked:
		status.State = atuinmodel.SyncLocked
		status.Problem = atuinmodel.ProblemLocked
		return status, metadata, nil
	case SecretModeUnavailable:
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
		return status, metadata, nil
	}

	address, addressErr := NormalizeSyncAddress(settings.SyncAddress)
	if addressErr != nil {
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		return status, metadata, addressErr
	}
	status.ServerURL = address
	if !settings.SyncEnabled {
		status.State = atuinmodel.SyncDisabled
		status.Problem = atuinmodel.ProblemDisabled
		return status, metadata, nil
	}
	tokenPresent, err := s.secrets.Has(ctx, SessionTokenRef())
	if err != nil {
		return syncStatusForSecretError(status, err), metadata, err
	}
	keyPresent, err := s.secrets.Has(ctx, MasterKeyRef())
	if err != nil {
		return syncStatusForSecretError(status, err), metadata, err
	}
	if !sameAccountAddress(metadata.Account, address) {
		// A token must never be sent to an address the stored account does not
		// own. The account service enforces the same rule before it authenticates.
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		return status, metadata, ErrAccountMismatch
	}
	if !tokenPresent {
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
		return status, metadata, nil
	}
	if !keyPresent {
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemKeyMissing
		return status, metadata, nil
	}
	status.State = atuinmodel.SyncIdle
	status.Problem = atuinmodel.ProblemNone
	return status, metadata, nil
}

func syncStatusForSecretError(status SyncStatus, err error) SyncStatus {
	switch {
	case errors.Is(err, ErrSecretsLocked), errors.Is(err, ErrAppLocked):
		status.State = atuinmodel.SyncLocked
		status.Problem = atuinmodel.ProblemLocked
	case errors.Is(err, ErrSecretsUnavailable):
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
	default:
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
	}
	return status
}

func (s *Service) loadMasterKey(ctx context.Context) ([]byte, bool, error) {
	present, err := s.secrets.Has(ctx, MasterKeyRef())
	if err != nil || !present {
		return nil, false, err
	}
	encoded, err := s.secrets.Get(ctx, MasterKeyRef())
	if err != nil {
		return nil, false, err
	}
	key, err := domainhistory.ParseMasterKey(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("%w: stored master key is invalid", ErrSecretUnreadable)
	}
	return key, true, nil
}

func (s *Service) openRemote(ctx context.Context, config Config) (Remote, error) {
	if s.factory == nil {
		return nil, ErrRemoteUnavailable
	}
	remote, err := s.factory.Open(ctx, config)
	if err != nil || remote == nil {
		return nil, ErrRemoteUnavailable
	}
	return remote, nil
}

// download applies every remote history series that is ahead of its local
// checkpoint. One page is one transaction; the checkpoint advances only after
// that transaction commits.
func (s *Service) download(ctx context.Context, remote Remote, capabilities Capabilities,
	statuses []SeriesStatus, metadata *domainhistory.Metadata) (int, int, error) {
	records, err := s.store.ListHistoryRecords(ctx)
	if err != nil {
		return 0, 0, err
	}
	if metadata.Series == nil {
		metadata.Series = make(map[string]domainhistory.SeriesCheckpoint)
	}
	pageSize := pageSizeFor(capabilities)

	downloaded, applied := 0, 0
	for _, series := range remoteHistorySeries(statuses) {
		key := domainhistory.SeriesKey(series.SeriesHost, series.Tag)
		checkpoint, known := metadata.Series[key]
		if !known {
			// A missing checkpoint means nothing has been applied. Index 0 is
			// a real record, so the zero value is not a valid position.
			checkpoint.AppliedIndex = -1
			checkpoint.UploadedIndex = -1
		}
		checkpoint.SeriesHost = series.SeriesHost
		checkpoint.Tag = series.Tag
		if checkpoint.AppliedIndex < -1 {
			checkpoint.AppliedIndex = -1
		}
		// A logout clears checkpoints but not the appended log, and a restored
		// or older database can hold captures the server never received. The
		// applied position may therefore only be rebuilt from records known to
		// have reached the server: a downloaded remote record or an
		// acknowledged local record. An unacknowledged capture proves nothing,
		// even when its index matches the server's high-water mark: on a
		// restored database it can occupy the index the server already uses
		// for a different record, and starting past it would silently drop
		// that record and later acknowledge the capture without sending it.
		// A persisted position above every trusted record is likewise re-
		// examined instead of trusted.
		checkpoint.AppliedIndex = maxTrustedSeriesIndex(records, series.SeriesHost, series.Tag)
		if checkpoint.AppliedIndex >= series.MaxIndex {
			metadata.Series[key] = checkpoint
			continue
		}
		for checkpoint.AppliedIndex < series.MaxIndex {
			start := checkpoint.AppliedIndex + 1
			page, err := remote.Download(ctx, series.Series, start, pageSize)
			if err != nil {
				return downloaded, applied, err
			}
			// A capture can commit while the page is in flight. Re-read the log
			// so both the leading-gap check and collision handling see it.
			localRecords, err := s.store.ListHistoryRecords(ctx)
			if err != nil {
				return downloaded, applied, err
			}
			prepared, last, err := prepareRemotePage(series, start, page,
				localIndexSet(localRecords, series.SeriesHost, series.Tag))
			if err != nil {
				return downloaded, applied, err
			}
			kept, reindexes, acknowledged, err := reconcileRemotePage(series, prepared, localRecords, last)
			if err != nil {
				return downloaded, applied, err
			}
			next := checkpoint
			next.AppliedIndex = last
			remotePage := RemotePage{
				Checkpoint: next, Reindexes: reindexes,
				Acknowledged: acknowledged, AcknowledgedAt: s.scheduler.Now(),
			}
			for _, item := range kept {
				remotePage.Records = append(remotePage.Records, item.record)
				if item.entry != nil {
					remotePage.Entries = append(remotePage.Entries, *item.entry)
				}
				if item.deleted != nil {
					remotePage.Deletes = append(remotePage.Deletes, *item.deleted)
				}
			}
			if err := s.store.ApplyRemotePage(ctx, remotePage); err != nil {
				return downloaded, applied, err
			}
			checkpoint = next
			metadata.Series[key] = checkpoint
			downloaded += len(remotePage.Records)
			applied += len(remotePage.Records)
		}
	}
	return downloaded, applied, nil
}

// preparedPageRecord is one accepted record of a downloaded page together with
// the projection it applies.
type preparedPageRecord struct {
	record  domainhistory.Record
	entry   *domainhistory.Entry
	deleted *RemoteDelete
}

// prepareRemotePage validates index continuity, ignores records for any tag
// other than history, and builds the domain log records plus typed projections
// for one atomic page. localIndexes holds every index already present in the
// local log for this series; it lets a page that begins after the requested
// start skip only indexes already held locally, never missing records.
func prepareRemotePage(series SeriesStatus, start int64, page Page,
	localIndexes map[int64]struct{}) ([]preparedPageRecord, int64, error) {
	if start < 0 {
		return nil, 0, ErrIncompletePage
	}
	next := start
	seen := false
	var prepared []preparedPageRecord
	for _, record := range page.Records {
		if record.Tag != domainhistory.RecordTagHistory || record.SeriesHost != series.SeriesHost {
			// Other Atuin tags are out of scope. They are never applied and
			// never advance a history checkpoint.
			continue
		}
		if record.Index < start {
			// A page may overlap a committed page after a retried request.
			// The checkpoint already proves this index committed, so applying
			// it again would add nothing.
			continue
		}
		if record.Index > next {
			// The server's series is missing one or more indexes the page
			// requested. That is only safe when every skipped index is
			// already in the local log; otherwise this page would silently
			// skip records we still need. A skipped index backed only by an
			// unsent local capture is not an acknowledgement: prepareUpload
			// must still upload that capture at this index.
			if !localIndexGapKnown(localIndexes, next, record.Index) {
				return nil, 0, ErrIncompletePage
			}
			next = record.Index
		}
		if record.Index != next || !record.Valid() {
			return nil, 0, ErrIncompletePage
		}
		seen = true
		switch record.Kind {
		case domainhistory.RecordKindCreate:
			entry := *record.Entry
			if entry.ID == "" {
				entry.ID = record.HistoryID
			}
			if entry.ID != record.HistoryID {
				return nil, 0, ErrIncompletePage
			}
			entry.Source = domainhistory.SourceAtuinSync
			if entry.RecordedAt.IsZero() {
				entry.RecordedAt = record.RecordedAt
			}
			// A remote record never carries this device's Host, Session, or
			// capture tier. Keeping them would attach another device's Host id
			// to a local Connection Log and mislabel an unobserved row.
			entry.HostID = ""
			entry.HostLabel = ""
			entry.UnsavedHost = false
			entry.SessionID = ""
			entry.CaptureTier = 0
			entry.Scope = RemoteScope(entry.Origin)
			entry = domainhistory.NormalizeEntry(entry)
			payload, err := json.Marshal(entry)
			if err != nil {
				return nil, 0, err
			}
			prepared = append(prepared, preparedPageRecord{
				record: remoteRecord(record, string(payload)),
				entry:  &entry,
			})
		case domainhistory.RecordKindDelete:
			payload, err := json.Marshal(domainhistory.DeleteRecordPayload{
				HistoryID: record.HistoryID,
				DeletedAt: record.DeletedAt,
			})
			if err != nil {
				return nil, 0, err
			}
			prepared = append(prepared, preparedPageRecord{
				record:  remoteRecord(record, string(payload)),
				deleted: &RemoteDelete{HistoryID: record.HistoryID, DeletedAt: record.DeletedAt},
			})
		default:
			return nil, 0, ErrIncompletePage
		}
		next++
	}
	if !seen {
		return nil, 0, ErrIncompletePage
	}
	return prepared, next - 1, nil
}

// localIndexSet returns every index already present in the local log for one
// series, regardless of direction.
func localIndexSet(records []domainhistory.Record, seriesHost, tag string) map[int64]struct{} {
	indexes := make(map[int64]struct{})
	for _, record := range records {
		if record.SeriesHost == seriesHost && record.Tag == tag {
			indexes[record.Index] = struct{}{}
		}
	}
	return indexes
}

// localIndexGapKnown reports whether every index in [from, to) is present. The
// length bound keeps a malformed page from making the check walk a huge range.
func localIndexGapKnown(indexes map[int64]struct{}, from, to int64) bool {
	if to <= from {
		return true
	}
	if to-from > int64(len(indexes)) {
		return false
	}
	for index := from; index < to; index++ {
		if _, ok := indexes[index]; !ok {
			return false
		}
	}
	return true
}

// reconcileRemotePage compares one downloaded page with the local log. A local
// record that already describes the same event is kept and the remote copy is
// dropped, so a restored log is never rewritten; the local record is reported
// as acknowledged because the download observed the same event on the server.
// An unacknowledged local capture that occupies an index the server also uses
// is moved after the page: both records survive and the capture is uploaded
// under its new index. An acknowledged local record that differs from the
// server is a real conflict and fails closed.
func reconcileRemotePage(series SeriesStatus, prepared []preparedPageRecord,
	local []domainhistory.Record, last int64) ([]preparedPageRecord, []Reindex, []string, error) {
	occupied := make(map[int64]domainhistory.Record)
	maxIndex := last
	for _, record := range local {
		if record.SeriesHost != series.SeriesHost || record.Tag != series.Tag {
			continue
		}
		occupied[record.Index] = record
		if record.Index > maxIndex {
			maxIndex = record.Index
		}
	}
	kept := make([]preparedPageRecord, 0, len(prepared))
	collisions := make([]preparedPageRecord, 0)
	acknowledged := make([]string, 0)
	for _, item := range prepared {
		existing, found := occupied[item.record.Index]
		if !found {
			kept = append(kept, item)
			continue
		}
		if sameLogicalRecord(existing, item.record) {
			// The local log already holds this command. Do not replace it with
			// a reconstructed remote payload; the checkpoint still advances.
			// The downloaded copy proves the server holds the event, so the
			// local record is acknowledged even when its log id differs.
			if existing.Direction == domainhistory.RecordDirectionLocal &&
				existing.UploadedAt.IsZero() {
				acknowledged = append(acknowledged, existing.ID)
			}
			continue
		}
		if existing.Direction != domainhistory.RecordDirectionLocal || !existing.UploadedAt.IsZero() {
			return nil, nil, nil, ErrUploadConflict
		}
		collisions = append(collisions, item)
	}
	sort.Slice(collisions, func(i, j int) bool {
		return collisions[i].record.Index < collisions[j].record.Index
	})
	reindexes := make([]Reindex, 0, len(collisions))
	next := maxIndex + 1
	for _, item := range collisions {
		existing := occupied[item.record.Index]
		reindexes = append(reindexes, Reindex{RecordID: existing.ID, Index: next})
		next++
		kept = append(kept, item)
	}
	sort.Slice(kept, func(i, j int) bool {
		return kept[i].record.Index < kept[j].record.Index
	})
	return kept, reindexes, acknowledged, nil
}

// sameLogicalRecord reports whether a local log record and an incoming remote
// record describe the same history event. The record id is excluded because a
// record restored from an older database can carry a different log id for the
// same writer-series position; history id, timestamp, version, and kind are
// the event's identity.
func sameLogicalRecord(local, remote domainhistory.Record) bool {
	return local.SeriesHost == remote.SeriesHost &&
		local.Tag == remote.Tag &&
		local.Index == remote.Index &&
		local.HistoryID == remote.HistoryID &&
		local.Version == remote.Version &&
		local.Kind == remote.Kind &&
		local.RecordedAt.UnixNano() == remote.RecordedAt.UnixNano()
}

func remoteRecord(record SyncRecord, payload string) domainhistory.Record {
	return domainhistory.Record{
		ID:         record.ID,
		SeriesHost: record.SeriesHost,
		Tag:        domainhistory.RecordTagHistory,
		Index:      record.Index,
		RecordedAt: record.RecordedAt,
		Version:    record.Version,
		Kind:       record.Kind,
		HistoryID:  record.HistoryID,
		Payload:    payload,
		Direction:  domainhistory.RecordDirectionRemote,
	}
}

// RemoteScope is the autocomplete scope of a synced row: another writer's
// origin, never a local Host identity.
func RemoteScope(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		origin = "unknown"
	}
	return "remote:" + origin
}

// remoteHistorySeries returns every history series the server reports. The
// local writer series is included: when the server is ahead of this device
// (a restored or older database), its missing records are downloaded before
// upload rather than silently skipped.
func pageSizeFor(capabilities Capabilities) int {
	pageSize := capabilities.PageSize
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return pageSize
}

// serverMaxIndex returns the highest history index the server reports for the
// local writer series, or -1 when the account has no series for this writer.
func serverMaxIndex(statuses []SeriesStatus, writer string) int64 {
	serverMax := int64(-1)
	for _, status := range statuses {
		if status.Tag == domainhistory.RecordTagHistory && status.SeriesHost == writer &&
			status.MaxIndex > serverMax {
			serverMax = status.MaxIndex
		}
	}
	return serverMax
}

func remoteHistorySeries(statuses []SeriesStatus) []SeriesStatus {
	merged := make(map[string]SeriesStatus)
	for _, status := range statuses {
		if status.Tag != domainhistory.RecordTagHistory || status.SeriesHost == "" {
			continue
		}
		key := domainhistory.SeriesKey(status.SeriesHost, status.Tag)
		if current, ok := merged[key]; !ok || status.MaxIndex > current.MaxIndex {
			merged[key] = status
		}
	}
	series := make([]SeriesStatus, 0, len(merged))
	for _, status := range merged {
		series = append(series, status)
	}
	sort.Slice(series, func(i, j int) bool {
		if series[i].SeriesHost == series[j].SeriesHost {
			return series[i].Tag < series[j].Tag
		}
		return series[i].SeriesHost < series[j].SeriesHost
	})
	return series
}

// prepareUpload reconciles the local writer series with the server status.
// Uploaded timestamps are only a display cache; every unacknowledged local
// record still has to be sent. The server's highest index is not an
// acknowledgement: the pinned server permits holes, so a local capture can sit
// at or below that index without the server ever holding it. Only a real
// upload acknowledgement or a downloaded copy of the same event may mark a
// record uploaded. An account change (or a server that lost the series) has no
// local series status, so every local record is sent to the new account.
func (s *Service) prepareUpload(ctx context.Context, records []domainhistory.Record,
	statuses []SeriesStatus, metadata domainhistory.Metadata) (int, error) {
	local := localSeriesRecords(records, metadata.WriterUUID)
	if !continuousSeries(records, metadata.WriterUUID) {
		// One writer series shares its index space between local captures and
		// downloaded records. Refuse to upload a series with a gap rather than
		// skip an index.
		return 0, ErrIncompletePage
	}
	serverMax := serverMaxIndex(statuses, metadata.WriterUUID)
	checkpoint := metadata.Series[domainhistory.SeriesKey(metadata.WriterUUID, domainhistory.RecordTagHistory)]
	checkpoint.SeriesHost = metadata.WriterUUID
	checkpoint.Tag = domainhistory.RecordTagHistory
	if checkpoint.UploadedIndex < -1 {
		checkpoint.UploadedIndex = -1
	}
	maxUploaded := maxUploadedIndex(local)
	if serverMax < maxUploaded {
		// The account changed or the server no longer holds what this device
		// thought it uploaded. Clear the acknowledgements it can no longer
		// justify and send those records again. Acknowledgements at or below
		// the server's high-water mark are real past uploads of this account
		// and stay valid; re-marking them is not an inference about an
		// unsent row.
		preserved := uploadedRecordIDsInRange(local, 0, serverMax)
		checkpoint.UploadedIndex = serverMax
		if err := s.store.ResetUploaded(ctx, checkpoint); err != nil {
			return 0, err
		}
		if len(preserved) > 0 {
			if err := s.store.MarkUploaded(ctx, preserved, s.scheduler.Now(), checkpoint); err != nil {
				return 0, err
			}
		}
	} else if serverMax > checkpoint.UploadedIndex {
		// Persist the server's high-water mark for display. No local record is
		// acknowledged here; upload marks each row only after the server
		// accepts it (or the download proves it).
		checkpoint.UploadedIndex = serverMax
		if err := s.store.MarkUploaded(ctx, nil, s.scheduler.Now(), checkpoint); err != nil {
			return 0, err
		}
	}
	// Count what upload will actually send: unacknowledged captures, plus
	// records whose acknowledgement the reset above cleared because the
	// server's high-water mark no longer covers them.
	pending := 0
	for _, record := range local {
		if record.UploadedAt.IsZero() || record.Index > serverMax {
			pending++
		}
	}
	return pending, nil
}

// upload sends every unacknowledged local record in index order in
// page-sized batches. Each batch is marked uploaded only after the server
// acknowledges it. A matching duplicate conflict is acknowledged; a mismatched
// one fails closed.
//
// A record at or below the server's high-water mark is still sent when it has
// no acknowledgement: the server series may contain a hole at that index, and
// the server is the only authority that can accept the record into the hole or
// reject it as a conflicting duplicate.
func (s *Service) upload(ctx context.Context, remote Remote, capabilities Capabilities,
	statuses []SeriesStatus, metadata domainhistory.Metadata) (int, error) {
	records, err := s.store.ListHistoryRecords(ctx)
	if err != nil {
		return 0, err
	}
	local := localSeriesRecords(records, metadata.WriterUUID)
	serverMax := serverMaxIndex(statuses, metadata.WriterUUID)
	pageSize := pageSizeFor(capabilities)
	checkpoint := metadata.Series[domainhistory.SeriesKey(metadata.WriterUUID, domainhistory.RecordTagHistory)]
	checkpoint.SeriesHost = metadata.WriterUUID
	checkpoint.Tag = domainhistory.RecordTagHistory
	checkpoint.UploadedIndex = serverMax

	uploaded := 0
	position := 0
	for position < len(local) {
		chunk := make([]domainhistory.Record, 0, pageSize)
		for position < len(local) && len(chunk) < pageSize {
			if local[position].UploadedAt.IsZero() {
				chunk = append(chunk, local[position])
			}
			position++
		}
		if len(chunk) == 0 {
			continue
		}
		acked, err := s.uploadChunk(ctx, remote, chunk)
		if err != nil {
			return uploaded, err
		}
		last := acked[len(acked)-1].Index
		if last > checkpoint.UploadedIndex {
			// Filling a hole below the high-water mark must not move the
			// recorded server position backwards.
			checkpoint.UploadedIndex = last
		}
		if err := s.store.MarkUploaded(ctx, recordIDs(acked), s.scheduler.Now(), checkpoint); err != nil {
			return uploaded, err
		}
		uploaded += len(acked)
	}
	return uploaded, nil
}

// uploadChunk acknowledges one contiguous batch. The server may report any of
// its indexes as already present; a matching duplicate is removed from the
// retry set, while the records before and after it are retried. This converges
// after a lost response without ever marking an index the server did not
// confirm.
func (s *Service) uploadChunk(ctx context.Context, remote Remote,
	chunk []domainhistory.Record) ([]domainhistory.Record, error) {
	records := append([]domainhistory.Record(nil), chunk...)
	acked := make([]domainhistory.Record, 0, len(chunk))
	for len(records) > 0 {
		batch := make([]SyncRecord, 0, len(records))
		for _, record := range records {
			syncRecord, err := syncRecordFromLocal(record)
			if err != nil {
				return nil, err
			}
			batch = append(batch, syncRecord)
		}
		err := remote.Upload(ctx, batch)
		if err == nil {
			acked = append(acked, records...)
			break
		}
		duplicate, ok := AsDuplicateRecord(err)
		if !ok {
			return nil, err
		}
		position := -1
		for index, record := range records {
			if record.Index == duplicate.Existing.Index {
				position = index
				break
			}
		}
		if position < 0 || !duplicateMatches(records[position], duplicate.Existing) {
			return nil, ErrUploadConflict
		}
		acked = append(acked, records[position])
		records = append(records[:position:position], records[position+1:]...)
		if len(acked) == len(chunk) {
			break
		}
	}
	sort.Slice(acked, func(i, j int) bool { return acked[i].Index < acked[j].Index })
	return acked, nil
}

// duplicateMatches compares the record metadata the Atuin server reports on a
// conflict: id, series host, tag, index, timestamp, and version. Payload fields
// are not part of the server's metadata and are not compared.
func duplicateMatches(local domainhistory.Record, existing SyncRecord) bool {
	return local.ID == existing.ID &&
		local.SeriesHost == existing.SeriesHost &&
		local.Tag == existing.Tag &&
		local.Index == existing.Index &&
		local.Version == existing.Version &&
		!local.RecordedAt.IsZero() &&
		local.RecordedAt.UnixNano() == existing.RecordedAt.UnixNano()
}

func syncRecordFromLocal(record domainhistory.Record) (SyncRecord, error) {
	syncRecord := SyncRecord{
		ID:         record.ID,
		SeriesHost: record.SeriesHost,
		Tag:        record.Tag,
		Index:      record.Index,
		RecordedAt: record.RecordedAt,
		Version:    record.Version,
		Kind:       record.Kind,
		HistoryID:  record.HistoryID,
	}
	switch record.Kind {
	case domainhistory.RecordKindCreate:
		var entry domainhistory.Entry
		if err := json.Unmarshal([]byte(record.Payload), &entry); err != nil {
			return SyncRecord{}, err
		}
		if entry.ID == "" {
			entry.ID = record.HistoryID
		}
		syncRecord.Entry = &entry
	case domainhistory.RecordKindDelete:
		var payload domainhistory.DeleteRecordPayload
		if err := json.Unmarshal([]byte(record.Payload), &payload); err != nil {
			return SyncRecord{}, err
		}
		if payload.DeletedAt.IsZero() {
			payload.DeletedAt = record.RecordedAt
		}
		syncRecord.DeletedAt = payload.DeletedAt
	}
	if !syncRecord.Valid() {
		return SyncRecord{}, ErrIncompletePage
	}
	return syncRecord, nil
}

func localSeriesRecords(records []domainhistory.Record, writer string) []domainhistory.Record {
	local := make([]domainhistory.Record, 0, len(records))
	for _, record := range records {
		if record.SeriesHost == writer && record.Tag == domainhistory.RecordTagHistory &&
			record.Direction == domainhistory.RecordDirectionLocal {
			local = append(local, record)
		}
	}
	sort.Slice(local, func(i, j int) bool { return local[i].Index < local[j].Index })
	return local
}

// continuousSeries reports whether the writer series holds every index from
// zero to its maximum, regardless of direction. A local capture and a
// downloaded record share one index space, so a gap anywhere means the upload
// order cannot be proven.
func continuousSeries(records []domainhistory.Record, writer string) bool {
	indexes := make([]int64, 0)
	for _, record := range records {
		if record.SeriesHost == writer && record.Tag == domainhistory.RecordTagHistory {
			indexes = append(indexes, record.Index)
		}
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	for position, index := range indexes {
		if index != int64(position) {
			return false
		}
	}
	return true
}

// maxTrustedSeriesIndex returns the highest index whose record is known to
// have reached the server: a downloaded remote record or a local record the
// engine acknowledged. An unacknowledged local capture proves nothing about
// the server position and must never extend the applied checkpoint.
func maxTrustedSeriesIndex(records []domainhistory.Record, seriesHost, tag string) int64 {
	max := int64(-1)
	for _, record := range records {
		if record.SeriesHost != seriesHost || record.Tag != tag {
			continue
		}
		if record.Direction == domainhistory.RecordDirectionRemote || !record.UploadedAt.IsZero() {
			if record.Index > max {
				max = record.Index
			}
		}
	}
	return max
}

func maxUploadedIndex(records []domainhistory.Record) int64 {
	max := int64(-1)
	for _, record := range records {
		if !record.UploadedAt.IsZero() && record.Index > max {
			max = record.Index
		}
	}
	return max
}

func pendingUploads(records []domainhistory.Record, writer string) int {
	count := 0
	for _, record := range records {
		if record.SeriesHost == writer && record.Tag == domainhistory.RecordTagHistory &&
			record.Direction == domainhistory.RecordDirectionLocal && record.UploadedAt.IsZero() {
			count++
		}
	}
	return count
}

func recordIDs(records []domainhistory.Record) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// uploadedRecordIDsInRange returns the records in [from, to] that already
// carry an upload acknowledgement. A reset can restore those instead of
// reporting them pending forever, without acknowledging any unsent row.
func uploadedRecordIDsInRange(records []domainhistory.Record, from, to int64) []string {
	ids := make([]string, 0)
	for _, record := range records {
		if record.Index >= from && record.Index <= to && !record.UploadedAt.IsZero() {
			ids = append(ids, record.ID)
		}
	}
	return ids
}

func storedAccountMatches(stored domainhistory.AccountIdentity, address string, account Account) error {
	if stored.ServerURL == "" {
		return ErrAccountMismatch
	}
	recorded, err := NormalizeSyncAddress(stored.ServerURL)
	if err != nil || recorded != address {
		return ErrAccountMismatch
	}
	if stored.UserID != "" && account.UserID != "" && stored.UserID != account.UserID {
		return ErrAccountMismatch
	}
	return nil
}

// fail maps a sync error to a status and schedules a retry only for transient
// failures. It returns the sanitized public error.
func (s *Service) fail(base SyncStatus, err error, result *SyncResult) error {
	status := statusForSyncError(base, err)
	status.ServerVersion = base.ServerVersion
	// LastSyncAt names the last run that finished, so a failure keeps it.
	previous := s.cachedStatus()
	if status.ServerVersion == "" {
		status.ServerVersion = previous.ServerVersion
	}
	if status.LastSyncAt.IsZero() {
		status.LastSyncAt = previous.LastSyncAt
	}
	if retryableSyncError(err) {
		attempt := s.nextRetryAttempt()
		delay := s.backoff(attempt)
		status.NextRetryAt = s.scheduler.Now().Add(delay)
		s.scheduleRetry(delay)
	} else {
		s.clearRetry()
		status.NextRetryAt = time.Time{}
	}
	if terminalSyncError(err) {
		s.setTerminalFailure(publicSyncError(err))
	} else {
		s.clearTerminalFailure()
	}
	status.PendingUploads = base.PendingUploads
	result.PendingUploads = status.PendingUploads
	s.setStatus(status)
	return publicSyncError(err)
}

func (s *Service) nextRetryAttempt() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt := s.retryAttempt
	s.retryAttempt++
	return attempt
}

func (s *Service) clearRetry() {
	s.mu.Lock()
	s.retryAttempt = 0
	timer := s.retryTimer
	s.retryTimer = nil
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (s *Service) scheduleRetry(delay time.Duration) {
	s.mu.Lock()
	if s.stopped || !s.started {
		s.mu.Unlock()
		return
	}
	if s.retryTimer != nil {
		s.retryTimer.Stop()
	}
	ctx := s.ctx
	s.retryTimer = s.scheduler.AfterFunc(delay, func() {
		s.enqueue(ctx, TriggerRetry, nil)
	})
	s.mu.Unlock()
}

func (s *Service) scheduleInterval() {
	if s.settings == nil {
		return
	}
	minutes := s.settings.HistorySettings().SyncIntervalMinutes
	if minutes <= 0 {
		minutes = 5
	}
	delay := time.Duration(minutes) * time.Minute

	s.mu.Lock()
	if s.stopped || !s.started {
		s.mu.Unlock()
		return
	}
	ctx := s.ctx
	s.intervalTimer = s.scheduler.AfterFunc(delay, func() {
		s.enqueue(ctx, TriggerInterval, nil)
		s.scheduleInterval()
	})
	s.mu.Unlock()
}

func (s *Service) backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(retryDelays) {
		attempt = len(retryDelays) - 1
	}
	return s.jitter(retryDelays[attempt])
}

// jittered applies +/-20 percent so many clients do not retry in lockstep.
func jittered(delay time.Duration) time.Duration {
	factor := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(delay) * factor)
}

func retryableSyncError(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, ErrAuthExpired), errors.Is(err, ErrAuthFailed):
		return false
	case errors.Is(err, ErrKeyMismatch), errors.Is(err, ErrKeyUnverified):
		return false
	case errors.Is(err, ErrIncompatibleServer):
		return false
	case errors.Is(err, ErrAccountMismatch), errors.Is(err, ErrUploadConflict):
		return false
	case errors.Is(err, ErrSecretsLocked), errors.Is(err, ErrSecretsUnavailable),
		errors.Is(err, ErrAppLocked):
		return false
	case errors.Is(err, ErrNotConfigured), errors.Is(err, ErrSyncDisabled):
		return false
	case errors.Is(err, ErrRemoteUnavailable), errors.Is(err, ErrInvalidSyncAddress):
		return false
	case errors.Is(err, ErrIncompletePage):
		return false
	default:
		return true
	}
}

func statusForSyncError(base SyncStatus, err error) SyncStatus {
	status := base
	status.NextRetryAt = time.Time{}
	switch {
	case errors.Is(err, ErrSecretsLocked), errors.Is(err, ErrAppLocked):
		status.State = atuinmodel.SyncLocked
		status.Problem = atuinmodel.ProblemLocked
	case errors.Is(err, ErrSecretsUnavailable):
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
	case errors.Is(err, ErrAuthExpired), errors.Is(err, ErrAuthFailed):
		status.State = atuinmodel.SyncAuthExpired
		status.Problem = atuinmodel.ProblemAuthExpired
	case errors.Is(err, ErrKeyMismatch):
		status.State = atuinmodel.SyncKeyMismatch
		status.Problem = atuinmodel.ProblemKeyMismatch
	case errors.Is(err, ErrKeyUnverified):
		status.State = atuinmodel.SyncIdle
		status.Problem = atuinmodel.ProblemKeyUnverified
	case errors.Is(err, ErrIncompatibleServer):
		status.State = atuinmodel.SyncIncompatibleServer
		status.Problem = atuinmodel.ProblemIncompatibleServer
	case errors.Is(err, ErrAccountMismatch):
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
	case errors.Is(err, ErrNotConfigured), errors.Is(err, ErrRemoteUnavailable):
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
	case errors.Is(err, ErrSyncDisabled):
		status.State = atuinmodel.SyncDisabled
		status.Problem = atuinmodel.ProblemDisabled
	case errors.Is(err, ErrOffline):
		status.State = atuinmodel.SyncOffline
		status.Problem = atuinmodel.ProblemOffline
	default:
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
	}
	return status
}

// publicSyncError returns only a known sentinel so a server message, request
// body, or record payload cannot leak through an error string.
func publicSyncError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrAuthExpired):
		return ErrAuthExpired
	case errors.Is(err, ErrAuthFailed):
		return ErrAuthFailed
	case errors.Is(err, ErrIncompatibleServer):
		return ErrIncompatibleServer
	case errors.Is(err, ErrKeyMismatch):
		return ErrKeyMismatch
	case errors.Is(err, ErrKeyUnverified):
		return ErrKeyUnverified
	case errors.Is(err, ErrOffline):
		return ErrOffline
	case errors.Is(err, ErrRemoteUnavailable):
		return ErrRemoteUnavailable
	case errors.Is(err, ErrAccountMismatch):
		return ErrAccountMismatch
	case errors.Is(err, ErrUploadConflict):
		return ErrUploadConflict
	case errors.Is(err, ErrSecretsLocked):
		return ErrSecretsLocked
	case errors.Is(err, ErrAppLocked):
		return ErrAppLocked
	case errors.Is(err, ErrSecretsUnavailable):
		return ErrSecretsUnavailable
	case errors.Is(err, ErrNotConfigured):
		return ErrNotConfigured
	case errors.Is(err, ErrSyncDisabled):
		return ErrSyncDisabled
	case errors.Is(err, ErrIncompletePage):
		return ErrIncompletePage
	case errors.Is(err, ErrInvalidSyncAddress):
		return ErrInvalidSyncAddress
	default:
		return ErrRemote
	}
}
