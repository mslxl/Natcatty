package app

import (
	"context"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// notifyingSFTPBookmarks forwards the transfer service's bookmark port and
// reports only global-list writes. Local-pane bookmarks never leave the
// device, so SaveLocalBookmarks is intentionally not intercepted.
type notifyingSFTPBookmarks struct {
	apptransfer.BookmarkRepository
	notify func()
}

func (r notifyingSFTPBookmarks) SaveBookmarks(ctx context.Context, items []domainsftp.Bookmark) error {
	if err := r.BookmarkRepository.SaveBookmarks(ctx, items); err != nil {
		return err
	}
	if r.notify != nil {
		r.notify()
	}
	return nil
}
