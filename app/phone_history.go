// Phone Command History wiring. The phone keeps the same journal, the same
// autocomplete source, and the same Atuin account and sync services as the
// desktop; only the desktop chrome around them is absent. A captured command
// reaches the journal through the shared completion hook, the journal tells
// the background engine that a local row appeared, and the engine uploads it
// without ever blocking the Session that produced it.
package app

import (
	"context"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/application/autocomplete"
	apphistory "github.com/mslxl/natcatty/application/history"
	historysync "github.com/mslxl/natcatty/application/historysync"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// phoneHistory owns the phone's Command History and its Atuin sync. It is the
// same application graph the desktop builds: history.Service over the shared
// SQLite database, autocomplete.Service over that journal, and the one
// AccountService/Service pair built by newHistorySyncServices.
type phoneHistory struct {
	history      *apphistory.Service
	autocomplete *autocomplete.Service
	account      *historysync.AccountService
	sync         *historysync.Service
	settings     historysync.AccountSettings
	metadata     historysync.MetadataStore
	transient    *transientCommands
}

var _ phoneshell.HistorySyncService = (*phoneHistory)(nil)
var _ phoneshell.CommandHistoryService = (*phoneHistory)(nil)

// Entries lists the live commands newest first for the Session Tools surface.
func (h *phoneHistory) Entries(ctx context.Context) ([]domainhistory.Entry, error) {
	if h == nil || h.history == nil {
		return nil, nil
	}
	return h.history.Entries(ctx)
}

// ClearThisDevice deletes the rows this install created and leaves entries
// imported or synced from another writer, the same scope the desktop uses.
func (h *phoneHistory) ClearThisDevice(ctx context.Context) (int, error) {
	if h == nil || h.history == nil {
		return 0, nil
	}
	return h.history.ClearThisDevice(ctx)
}

// PruneHistoryEntries deletes exactly the identities the Tools confirmation
// counted.
func (h *phoneHistory) PruneHistoryEntries(ctx context.Context, ids []string) (int, error) {
	if h == nil || h.history == nil {
		return 0, nil
	}
	return h.history.PruneHistoryEntries(ctx, ids)
}

// DeleteEverywhere removes every live row this device knows and queues the
// Atuin delete records, the desktop's delete-all scope.
func (h *phoneHistory) DeleteEverywhere(ctx context.Context) (int, error) {
	if h == nil || h.history == nil {
		return 0, nil
	}
	return h.history.DeleteEverywhere(ctx)
}

// newPhoneHistory builds the phone's history graph. preferences is the same
// device-local account configuration document the desktop reads and writes,
// and the token and master key live in the same field-secret store. A phone
// without an OS credential store keeps the account unconfigured instead of
// failing startup.
func newPhoneHistory(database *store.Store, preferences *apppreferences.Service,
	terminal *appterminalsettings.Service, secretStore *store.SecretStore) *phoneHistory {
	history := apphistory.NewService(database,
		apphistory.WithRetention(func() apphistory.Retention {
			current := terminal.Settings()
			return apphistory.Retention{
				Logs: domainhistory.Retention{
					Count:  current.ConnectionLogRetentionCount,
					MaxAge: time.Duration(current.ConnectionLogRetentionDays) * 24 * time.Hour,
				},
			}
		}),
	)
	// Apply Connection Log retention once at startup so a lowered limit takes
	// effect even before the next record arrives.
	_, _ = history.Prune(context.Background())

	autocompleteService := autocomplete.NewService(history,
		func() autocomplete.Config {
			current := terminal.Settings()
			return autocomplete.Config{
				Enabled:        current.AutocompleteEnabled,
				MinChars:       current.AutocompleteMinChars,
				MaxSuggestions: current.AutocompleteMaxSuggestions,
				DebounceMs:     current.AutocompleteDebounceMs,
				Scope:          current.AutocompleteHistoryScope,
			}
		},
	)
	autocompleteService.Load(context.Background())

	account, syncService := newHistorySyncServices(
		preferences, store.OpenHistorySecrets(secretStore), database, database,
		// The phone has no App Lock surface yet, so account reads and writes
		// are never paused by it.
		func() bool { return false },
		historysync.WithSyncApplied(historySyncApplied(history, autocompleteService)))
	history.SubscribeLocal(func() { syncService.NotifyLocalChange() })

	return &phoneHistory{
		history:      history,
		autocomplete: autocompleteService,
		account:      account,
		sync:         syncService,
		settings:     preferences,
		metadata:     database,
		transient:    newTransientCommands(),
	}
}

// commandHook is the Session completion hook the phone's remote Sessions use.
// It is the desktop's hook verbatim, including the transient Host-Atuin path.
func (h *phoneHistory) commandHook() func(appsession.CommandEvent) {
	if h == nil {
		return nil
	}
	return commandCompletionHook(h.autocomplete, resolveCommandLocalIdentity(), h.transient)
}

// Start begins background syncing. It never blocks on the network: the engine
// schedules its own work and reports progress through Status.
func (h *phoneHistory) Start(ctx context.Context) {
	if h == nil || h.sync == nil {
		return
	}
	h.sync.Start(ctx)
}

// Stop ends the background engine and waits for its current run to leave.
func (h *phoneHistory) Stop() {
	if h == nil || h.sync == nil {
		return
	}
	h.sync.Stop()
}

// Status merges the account's identity and key state with the engine's live
// progress, exactly as the desktop panel does. A failure is reported here and
// is never returned to a Session.
func (h *phoneHistory) Status() historysync.Status {
	if h == nil || h.account == nil {
		return historysync.Status{}
	}
	status := h.account.Status()
	if h.sync == nil {
		return status
	}
	synced := h.sync.Status()
	if synced.State != "" {
		status.State = synced.State
	}
	if synced.Problem != "" {
		status.Problem = synced.Problem
	}
	if synced.ServerVersion != "" {
		status.ServerVersion = synced.ServerVersion
	}
	if !synced.LastSyncAt.IsZero() {
		status.LastSyncAt = synced.LastSyncAt
	}
	status.PendingUploads = synced.PendingUploads
	status.NextRetryAt = synced.NextRetryAt
	return status
}

// HistorySettings returns the persisted device-local account configuration.
func (h *phoneHistory) HistorySettings() domainpreferences.History {
	if h == nil || h.settings == nil {
		return domainpreferences.History{}
	}
	return h.settings.HistorySettings()
}

// UpdateHistorySettings persists a change and lets the engine resume after a
// terminal configuration failure.
func (h *phoneHistory) UpdateHistorySettings(change func(*domainpreferences.History)) error {
	if h == nil || h.settings == nil {
		return nil
	}
	if err := h.settings.UpdateHistorySettings(change); err != nil {
		return err
	}
	h.notifyConfigurationChanged()
	return nil
}

// Login signs in to the configured Atuin server.
func (h *phoneHistory) Login(ctx context.Context, request historysync.LoginRequest) (historysync.Status, error) {
	if h == nil || h.account == nil {
		return historysync.Status{}, nil
	}
	status, err := h.account.Login(ctx, request)
	if err == nil {
		h.notifyConfigurationChanged()
	}
	return status, err
}

// SetMasterKey stores the account's Atuin master key.
func (h *phoneHistory) SetMasterKey(ctx context.Context, input string, confirmChange bool) (historysync.Status, error) {
	if h == nil || h.account == nil {
		return historysync.Status{}, nil
	}
	status, err := h.account.SetMasterKey(ctx, input, confirmChange)
	if err == nil {
		h.notifyConfigurationChanged()
	}
	return status, err
}

// ConfirmUnverifiedKey accepts a key for an account with no records yet.
func (h *phoneHistory) ConfirmUnverifiedKey(ctx context.Context) (historysync.Status, error) {
	if h == nil || h.account == nil {
		return historysync.Status{}, nil
	}
	status, err := h.account.ConfirmUnverifiedKey(ctx)
	if err == nil {
		h.notifyConfigurationChanged()
	}
	return status, err
}

// Logout removes the token and key while keeping local history.
func (h *phoneHistory) Logout(ctx context.Context) (historysync.Status, error) {
	if h == nil || h.account == nil {
		return historysync.Status{}, nil
	}
	status, err := h.account.Logout(ctx)
	if err == nil {
		h.notifyConfigurationChanged()
	}
	return status, err
}

// Check refreshes the remote account status without syncing records.
func (h *phoneHistory) Check(ctx context.Context) (historysync.Status, error) {
	if h == nil || h.account == nil {
		return historysync.Status{}, nil
	}
	return h.account.Check(ctx)
}

// SyncNow triggers one manual run in the background. The engine coalesces it
// with a scheduled run, so the UI thread never waits on the network.
func (h *phoneHistory) SyncNow() {
	if h == nil || h.sync == nil {
		return
	}
	go func() { _, _ = h.sync.SyncNow(context.Background()) }()
}

func (h *phoneHistory) notifyConfigurationChanged() {
	if h != nil && h.sync != nil {
		h.sync.NotifyConfigurationChanged()
	}
}
