package transfer

import (
	"strings"
	"time"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// paneTitle is a Tab's display title: Local for a local Tab, the Host label
// otherwise.
func paneTitle(m locales.Messages, tab *transferstate.Tab) string {
	if tab == nil {
		return ""
	}
	if tab.Kind == transferstate.KindLocal {
		return m.SftpLocal()
	}
	return tab.Title()
}

// noticeText localizes the surface's non-fatal status code. Unknown codes
// render nothing, so no untranslated token reaches the window.
func noticeText(m locales.Messages, code string) string {
	switch code {
	case "copied":
		return m.SftpNoticeCopied()
	case "cut":
		return m.SftpNoticeCut()
	case "clipboard-empty":
		return m.SftpClipboardEmpty()
	case "nothing-selected":
		return m.SftpNothingSelected()
	case "select-one":
		return m.SftpSelectOne()
	case "pane-unavailable":
		return m.SftpPaneUnavailable()
	case "same-directory":
		return m.SftpSameDirectory()
	case "copy-into-self":
		return m.SftpCopyIntoSelf()
	case "bookmarks-unreadable":
		return m.SftpBookmarksUnreadable()
	case "busy":
		return m.SftpBusy()
	case "operation-done":
		return m.SftpOperationDone()
	case "operation-failed":
		return m.SftpOperationFailed()
	case "operation-cancelled":
		return m.SftpOperationCancelled()
	case "overwrite-required":
		return m.SftpOverwriteRequired()
	case "conflict-required":
		return m.SftpConflictRequired()
	case "hosts-unavailable":
		return m.SftpHostsUnavailable()
	case "open-file":
		return m.SftpOpenFileUnavailable()
	case "transfer-unavailable":
		return m.SftpTransferUnavailable()
	case "transfer-reconnect-required":
		return m.SftpTransferReconnectRequired()
	case "transfer-not-retryable":
		return m.SftpTransferNotRetryable()
	case "transfer-control-failed":
		return m.SftpTransferControlFailed()
	case "transfer-conflict-failed":
		return m.SftpTransferConflictFailed()
	}
	return ""
}

// errorText localizes a file-operation failure by its kind. The detail is
// never shown raw: it may contain a remote path or transport text.
func errorText(m locales.Messages, err error) string {
	if err == nil {
		return ""
	}
	return errorKindText(m, apptransfer.KindOf(err))
}

// errorKindText localizes a persisted failure classification. It takes the
// stable kind token so a row restored after a restart never shows it raw.
func errorKindText(m locales.Messages, kind apptransfer.ErrorKind) string {
	switch kind {
	case apptransfer.KindExists:
		return m.SftpErrorExists()
	case apptransfer.KindNotFound:
		return m.SftpErrorNotFound()
	case apptransfer.KindPermission:
		return m.SftpErrorPermission()
	case apptransfer.KindNotDir:
		return m.SftpErrorNotDir()
	case apptransfer.KindIsDir:
		return m.SftpErrorIsDir()
	case apptransfer.KindNotEmpty:
		return m.SftpErrorNotEmpty()
	case apptransfer.KindInvalidName:
		return m.SftpErrorInvalidName()
	case apptransfer.KindInvalidMode:
		return m.SftpErrorInvalidMode()
	case apptransfer.KindSelfCopy:
		return m.SftpCopyIntoSelf()
	case apptransfer.KindUnsupported:
		return m.SftpErrorUnsupported()
	case apptransfer.KindCancelled:
		return m.SftpErrorCancelled()
	case apptransfer.KindConnection:
		return m.SftpErrorConnection()
	case apptransfer.KindTooLarge:
		return m.SftpErrorTooLarge()
	case apptransfer.KindReserved:
		return m.SftpErrorReserved()
	case apptransfer.KindTypeMismatch:
		return m.SftpErrorTypeMismatch()
	case apptransfer.KindInternal:
		return m.SftpErrorInternal()
	}
	return m.SftpErrorGeneric()
}

// operationLabel is the running operation's progress line.
func operationLabel(m locales.Messages, op *transferstate.Operation) string {
	if op == nil {
		return ""
	}
	current := op.Current
	if current == "" {
		current = operationKindLabel(m, op.Kind)
	}
	if !op.Running && op.Err == nil && op.Metadata() {
		// A finished metadata operation has a completion state, not a
		// meaningless 0%.
		return m.SftpProgressDone(baseName(current))
	}
	label := m.SftpProgress(baseName(current), op.Percent())
	if eta := etaText(m, op.ETASeconds()); eta != "" {
		label += " · " + eta
	}
	if op.Skipped > 0 {
		label += " · " + m.SftpTransferSkipped(op.Skipped)
	}
	return label
}

// operationKindLabel names an operation before its first file is known. The
// progress line never renders the domain's stable identifier as UI text.
func operationKindLabel(m locales.Messages, kind domainsftp.OperationKind) string {
	switch kind {
	case domainsftp.OpMove:
		return m.SftpMoveTo()
	case domainsftp.OpUpload:
		return m.SftpUpload()
	case domainsftp.OpDownload:
		return m.SftpDownload()
	case domainsftp.OpDelete:
		return m.SftpDelete()
	case domainsftp.OpRename:
		return m.SftpRename()
	case domainsftp.OpMkdir:
		return m.SftpNewFolder()
	case domainsftp.OpCreateFile:
		return m.SftpNewFile()
	case domainsftp.OpChmod:
		return m.SftpPermissions()
	default:
		return m.SftpCopyTo()
	}
}

// baseName trims a path to its last component for the progress line.
func baseName(path string) string {
	trimmed := strings.TrimRight(path, `/\`)
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// sortSummary names the Tab's current ordering beside the Sort menu, so the
// menu button itself can stay one stable, localized label. The arrow is
// direction, not translated text.
func sortSummary(m locales.Messages, order domainsftp.Sort) string {
	var field string
	switch order.Normalized().Field {
	case domainsftp.SortSize:
		field = m.SftpSortSize()
	case domainsftp.SortModified:
		field = m.SftpSortModified()
	case domainsftp.SortType:
		field = m.SftpSortType()
	case domainsftp.SortOwner:
		field = m.SftpSortOwner()
	default:
		field = m.SftpSortName()
	}
	arrow := "↑"
	if order.Descending {
		arrow = "↓"
	}
	return field + " " + arrow
}

// secondaryText is a row's right-hand detail: a file's size, or nothing for
// a folder.
func secondaryText(entry domainsftp.Entry) string {
	if entry.IsDir() {
		return ""
	}
	return domainsftp.FormatSize(entry.Size)
}

// displayName adds the folder separator that makes a directory row legible
// without a glyph column.
func displayName(entry domainsftp.Entry) string {
	if entry.IsDir() && entry.Name != ".." {
		return entry.Name + "/"
	}
	if entry.Name == ".." {
		return "../"
	}
	return entry.Name
}

// modifiedText formats a row's modification time compactly.
func modifiedText(when time.Time) string {
	if when.IsZero() {
		return ""
	}
	return when.Local().Format("2006-01-02 15:04")
}
