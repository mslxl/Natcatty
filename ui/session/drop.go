package sessionui

import (
	"strings"
	"sync"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// QuoteDropPaths joins paths for insertion into a shell prompt. Every path is
// quoted so spaces and metacharacters stay literal. Windows file names cannot
// contain a double quote, and both cmd.exe and PowerShell accept double-quoted
// paths, so the Windows dialect is a plain double-quoted span. A POSIX shell
// single-quotes the path and closes the span around an embedded apostrophe.
// The original inserted raw paths; quoting is required here so a dropped file
// name can never become shell syntax.
func QuoteDropPaths(files []string, windows bool) string {
	quoted := make([]string, 0, len(files))
	for _, file := range files {
		quoted = append(quoted, quoteDropPath(file, windows))
	}
	return strings.Join(quoted, " ")
}

func quoteDropPath(path string, windows bool) string {
	if windows {
		return `"` + path + `"`
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// DropNotice is the result of one drop. It is a code, not user-visible text:
// the view localizes it.
type DropNotice string

const (
	DropNoticeNone        DropNotice = ""
	DropNoticeInserted    DropNotice = "inserted"
	DropNoticeUploaded    DropNotice = "uploaded"
	DropNoticeUnavailable DropNotice = "unavailable"
	DropNoticeFailed      DropNotice = "failed"
)

// UploadFunc is the seam a future SFTP or ZMODEM ticket implements. A nil
// uploader means uploads are unavailable: the drop changes nothing and the
// Session reports that plainly instead of silently inserting a path.
type UploadFunc func([]string) error

// DropHandler applies the configured drop action. Insert needs no transport;
// upload never falls back to inserting when no transport exists, because that
// would contradict the user's configuration and could send an unintended path
// to the shell.
type DropHandler struct {
	action func() domainterminal.DropAction
	upload UploadFunc
	insert func(string)

	mu     sync.Mutex
	notice DropNotice
	count  int
}

// NewDropHandler returns a handler. action, insert, and upload may be nil;
// nil action inserts.
func NewDropHandler(action func() domainterminal.DropAction, insert func(string), upload UploadFunc) *DropHandler {
	if action == nil {
		action = func() domainterminal.DropAction { return domainterminal.DropInsert }
	}
	return &DropHandler{action: action, insert: insert, upload: upload}
}

// Drop applies the configured action to files and records the visible result.
// windows selects the quoting dialect; callers derive it from the Session.
func (h *DropHandler) Drop(files []string, windows bool) DropNotice {
	if len(files) == 0 {
		return DropNoticeNone
	}
	action := h.action()
	if !action.Valid() {
		action = domainterminal.DropInsert
	}
	notice := DropNoticeInserted
	switch action {
	case domainterminal.DropUpload:
		if h.upload == nil {
			notice = DropNoticeUnavailable
		} else if err := h.upload(files); err != nil {
			notice = DropNoticeFailed
		} else {
			notice = DropNoticeUploaded
		}
	default:
		if h.insert == nil {
			notice = DropNoticeUnavailable
		} else {
			h.insert(QuoteDropPaths(files, windows))
		}
	}
	h.mu.Lock()
	h.notice = notice
	h.count = len(files)
	h.mu.Unlock()
	return notice
}

// Notice returns the most recent drop result. Dismiss clears it.
func (h *DropHandler) Notice() (DropNotice, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.notice, h.count
}

// Dismiss clears the drop banner.
func (h *DropHandler) Dismiss() {
	h.mu.Lock()
	h.notice = DropNoticeNone
	h.count = 0
	h.mu.Unlock()
}
