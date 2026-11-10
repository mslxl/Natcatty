//go:build !windows

package plugin

// webviewAcceptWindowsSpelling is false where the page runs at the custom
// scheme itself: an http:// spelling is never a plugin page there.
const webviewAcceptWindowsSpelling = false
