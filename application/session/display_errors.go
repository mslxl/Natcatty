package session

import "errors"

// ErrDesktopResizeRefused reports that the server answered a desktop-resize
// request with a nonzero ExtendedDesktopSize status. The server rejected the
// requested desktop layout, so the viewer keeps local scaling.
var ErrDesktopResizeRefused = errors.New("session: the server rejected the desktop layout")

// ErrDesktopResizePending reports that the server accepted or forwarded a
// desktop-resize request and may complete it later. ExtendedDesktopSize status
// 4 ("request forwarded") is the normal answer from a server such as QEMU
// while the guest is still booting; a real result follows asynchronously. It
// is not a failure: the viewer keeps the requested size pending and does not
// latch local scaling.
var ErrDesktopResizePending = errors.New("session: the server accepted the desktop resize and may complete it later")

// ErrDesktopResizeTimeout reports that the server did not answer a
// desktop-resize request within the client's bound. It is not fatal: the
// adapter keeps the connection, and the viewer may retry on a later pane
// change.
var ErrDesktopResizeTimeout = errors.New("session: the server did not answer the desktop resize request")
