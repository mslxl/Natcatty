package app

import (
	"context"
	"strings"

	"github.com/egoist/mygo"
	atuinremote "github.com/mslxl/natcatty/adapters/atuin/remote"
	"github.com/mslxl/natcatty/application/autocomplete"
	apphistory "github.com/mslxl/natcatty/application/history"
	historysync "github.com/mslxl/natcatty/application/historysync"
)

// historySyncApplied refreshes the History surface and autocomplete after the
// sync engine commits downloaded projections. A tombstone page counts as
// applied, so a delete from another writer removes the row without a restart.
// It runs on the sync goroutine outside every service lock.
func historySyncApplied(historyService *apphistory.Service,
	autocompleteService *autocomplete.Service) func(historysync.SyncResult) {
	return func(result historysync.SyncResult) {
		if result.Applied == 0 {
			return
		}
		historyService.NotifyExternalChange()
		autocompleteService.Load(context.Background())
	}
}

// newHistorySyncServices builds the Atuin account and background sync services
// with one production Remote factory. Keeping construction in one helper lets
// the app-level integration test exercise the same wiring against a loopback
// server, and lets the wiring guard test pin that both constructors receive the
// same non-nil adapter factory.
func newHistorySyncServices(settings historysync.AccountSettings, secrets historysync.Secrets,
	metadata historysync.MetadataStore, syncStore historysync.SyncStore,
	appLocked func() bool, opts ...historysync.Option) (*historysync.AccountService, *historysync.Service) {
	factory := atuinremote.NewFactory(atuinremote.Options{
		UserAgent: historyRemoteUserAgent(),
	})
	account := historysync.NewAccountService(settings, secrets, metadata, factory,
		historysync.WithAccountAppLocked(appLocked))
	syncOptions := make([]historysync.Option, 0, len(opts)+1)
	syncOptions = append(syncOptions, historysync.WithAppLocked(appLocked))
	syncOptions = append(syncOptions, opts...)
	return account, historysync.NewService(settings, secrets, syncStore, factory, syncOptions...)
}

// historyRemoteUserAgent names the packaged application version. A development
// build without a package manifest uses the adapter's product fallback rather
// than an empty or placeholder User-Agent.
func historyRemoteUserAgent() string {
	version := strings.TrimSpace(mygo.App.Version())
	if version == "" {
		return atuinremote.DefaultUserAgent
	}
	return atuinremote.UserAgentForVersion(version)
}
