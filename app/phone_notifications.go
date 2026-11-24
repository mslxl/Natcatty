package app

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// The phone notification path. A program in a Session asks the terminal for an
// OSC 9/777/99 notification; the shared Session service parses it and calls the
// hook installed here. The hook builds a system notification through MyGo and
// gives it an ID that names the owning Tab and Pane, so the system's click —
// including the click that launched the app after the process ended — can open
// the right Session without the app holding any runtime SessionID across runs.
//
// This is the phone counterpart to app/notifications.go. That file is excluded
// on ios and android because it depends on the desktop window/tray model; the
// phone root has no windows to raise, only a Tab and Pane to show.

const (
	// phoneNotificationFixedTitle and phoneNotificationFixedBody are the whole
	// system-notification payload. The program-supplied title and body never
	// enter it: terminal notification text is arbitrary remote output, so any
	// string a program emits could itself be a credential, and no allowlist or
	// denylist of shapes can prove a given string is safe. A constant payload
	// makes "the notification contains no secret" true by construction rather
	// than by judging untrusted prose.
	//
	// Trade-off: the lock screen cannot preview what the program said. The
	// notification ID still routes the click to the owning Tab and Pane, so the
	// user reaches the Session that raised it.
	phoneNotificationFixedTitle = "Terminal notification"
	phoneNotificationFixedBody  = "Open Natcatty to view this Session."
)

// phoneNotificationSender is the system-notification seam. Production uses
// MyGo; the offscreen tests use a fake so no notification is posted and no
// permission dialog appears.
type phoneNotificationSender interface {
	Supported() bool
	Show(id, title, body string) error
}

// systemPhoneNotifier shows a notification through MyGo. MyGo registers the
// system click with App.OnNotificationClick, which the composition root points
// at applyPhoneNotificationClick.
type systemPhoneNotifier struct{}

func (systemPhoneNotifier) Supported() bool { return mygo.NotificationsSupported() }

func (systemPhoneNotifier) Show(id, title, body string) error {
	return mygo.NewNotification(mygo.NotificationOptions{
		ID:    id,
		Title: title,
		Body:  body,
		Group: "natcatty",
	}).Show()
}

// phoneNotificationRouteSource resolves the runtime SessionID the hook receives
// to the persisted Tab and Pane identities a click can use across relaunches.
// *phoneshell.Shell implements it.
type phoneNotificationRouteSource interface {
	RouteForSession(id model.SessionID) (phoneshell.SessionRoute, bool)
}

// phoneNotificationRouteShower opens one persisted Tab and Pane.
// *phoneshell.Shell implements it.
type phoneNotificationRouteShower interface {
	ShowSessionRoute(route phoneshell.SessionRoute) bool
}

// phoneNotificationBridge turns one parsed terminal notification into one
// system notification. delivery is scheduled off the Session read loop: the
// system can wait on its first-run permission prompt, and terminal output must
// not stall behind it.
type phoneNotificationBridge struct {
	sender phoneNotificationSender
	routes phoneNotificationRouteSource
	// mode is the shared OSC notification setting. Nil notifies always, as the
	// desktop default does.
	mode func() domainterminal.NotificationMode
	// focus reports whether the app is the active one, for the "unfocused"
	// mode. The production value is mygo.App.IsActive.
	focus func() bool
	// dispatch runs a delivery job. Nil runs it on its own goroutine, which is
	// production's behavior; tests set a synchronous dispatcher.
	dispatch func(func())
}

func newPhoneNotificationBridge(sender phoneNotificationSender,
	routes phoneNotificationRouteSource, mode func() domainterminal.NotificationMode,
	focus func() bool) *phoneNotificationBridge {

	return &phoneNotificationBridge{sender: sender, routes: routes, mode: mode, focus: focus}
}

// deliver publishes one program notification without blocking the caller. A
// denied permission, an unsupported sender, or the user's mode only drops the
// notification: the Session and its terminal keep running.
func (b *phoneNotificationBridge) deliver(id model.SessionID, notification appsession.TerminalNotification) {
	if b == nil || b.sender == nil {
		return
	}
	job := func() { b.show(id, notification) }
	if b.dispatch != nil {
		b.dispatch(job)
		return
	}
	go job()
}

// show applies the notification policy and publishes the notification. It runs
// off the Session read loop, so mygo's main-thread waits and the permission
// prompt do not delay terminal output.
//
// The payload is a fixed constant. Program-supplied title and body are used
// only to decide whether the request is worth a banner; they are never shown.
// See the constant's comment for why.
func (b *phoneNotificationBridge) show(id model.SessionID, notification appsession.TerminalNotification) {
	if !b.allowed() {
		return
	}
	if !b.sender.Supported() {
		return
	}
	// A bare notification request with neither a title nor a body carries no
	// information worth a system banner. Both fields are program output, so this
	// is the only thing their emptiness decides.
	if notification.Title == "" && notification.Body == "" {
		return
	}

	notificationID := phoneUnroutedNotificationID(notification.At)
	if b.routes != nil {
		if route, ok := b.routes.RouteForSession(id); ok {
			notificationID = phoneNotificationID(route)
		}
	}
	if err := b.sender.Show(notificationID, phoneNotificationFixedTitle, phoneNotificationFixedBody); err != nil {
		if phoneNotificationDenied(err) {
			// The user declined notification permission. Nothing else about the
			// app changes; the terminal was never waiting on this.
			return
		}
		// The payload is constant, but keep the error alone on the log line:
		// the Session's output is still remote program text.
		log.Printf("natcatty: phone notification not delivered: %v", err)
	}
}

// allowed applies the shared OSC notification mode. Unlike the desktop, the
// phone's "unfocused" mode means the app itself is not the active one; a
// backgrounded app or one hidden behind another app notifies, while a banner
// never covers the pane the user is watching.
func (b *phoneNotificationBridge) allowed() bool {
	if b.mode == nil {
		return true
	}
	switch b.mode() {
	case domainterminal.NotificationOff:
		return false
	case domainterminal.NotificationUnfocused:
		return b.focus == nil || !b.focus()
	default:
		return true
	}
}

// phoneNotificationDenied recognizes the permission refusal the MyGo mobile
// host reports. The host's error crosses the native bridge as a plain string,
// so the sentinel alone is not enough; both the sentinel and the host's
// message count as a denial.
func phoneNotificationDenied(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mygo.ErrNotificationsDenied) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "notifications are not allowed") ||
		strings.Contains(message, "does not allow notifications")
}

// programNotificationHookSetter is the one Service method the composition root
// needs. Keeping it an interface lets the offscreen tests prove the hook is
// installed without building a real Session service.
type programNotificationHookSetter interface {
	SetNotificationHook(func(model.SessionID, appsession.TerminalNotification))
}

// installPhoneNotificationHook registers the delivery hook. It is separate from
// installPhoneNotifications so the seam can be faked in tests.
func installPhoneNotificationHook(setter programNotificationHookSetter,
	deliver func(model.SessionID, appsession.TerminalNotification)) {

	if setter == nil || deliver == nil {
		return
	}
	setter.SetNotificationHook(deliver)
}

// installPhoneNotifications wires the phone's Session service to the system
// notification center. The phone root calls it before any Session opens, which
// is what lets the service copy the hook into each Session.
func installPhoneNotifications(service *appsession.Service, shell *phoneshell.Shell,
	mode func() domainterminal.NotificationMode) {

	if service == nil || shell == nil {
		return
	}
	bridge := newPhoneNotificationBridge(systemPhoneNotifier{}, shell, mode, mygo.App.IsActive)
	installPhoneNotificationHook(service, bridge.deliver)
}

// applyPhoneNotificationClick routes one system notification click. A
// notification of another app, or one whose payload is not a Natcatty route,
// is ignored so the click does nothing rather than navigating somewhere
// arbitrary.
func applyPhoneNotificationClick(shower phoneNotificationRouteShower, id string) bool {
	if shower == nil {
		return false
	}
	route, ok := parsePhoneNotificationID(id)
	if !ok {
		return false
	}
	return shower.ShowSessionRoute(route)
}

// phoneNotificationIDPrefix keeps the phone's click payload out of the IDs the
// desktop notifications use, so a stray click cannot be mistaken for one.
// phoneNotificationNoticeIDPrefix marks a notification with no routable
// Session; the two prefixes are distinct so an unrouted click is rejected by
// the parser rather than read as a Tab named "notice".
const (
	phoneNotificationIDPrefix       = "natcatty.phone.session."
	phoneNotificationNoticeIDPrefix = "natcatty.phone.notice."
)

// phoneNotificationID names a Tab and Pane in the notification ID. The IDs are
// non-secret persisted Workspace identities; no Session content, credential,
// Host address, or runtime SessionID enters the payload.
func phoneNotificationID(route phoneshell.SessionRoute) string {
	return phoneNotificationIDPrefix +
		url.QueryEscape(route.Tab) + ":" + url.QueryEscape(string(route.Pane))
}

// parsePhoneNotificationID returns the route a phone notification ID names.
func parsePhoneNotificationID(id string) (phoneshell.SessionRoute, bool) {
	rest, ok := strings.CutPrefix(id, phoneNotificationIDPrefix)
	if !ok {
		return phoneshell.SessionRoute{}, false
	}
	tabPart, panePart, ok := strings.Cut(rest, ":")
	if !ok {
		return phoneshell.SessionRoute{}, false
	}
	tab, err := url.QueryUnescape(tabPart)
	if err != nil || tab == "" {
		return phoneshell.SessionRoute{}, false
	}
	pane, err := url.QueryUnescape(panePart)
	if err != nil {
		return phoneshell.SessionRoute{}, false
	}
	return phoneshell.SessionRoute{Tab: tab, Pane: model.PaneID(pane)}, true
}

// phoneUnroutedNotificationID gives a notification whose Session already ended
// an ID of its own. A click on it finds no route and is ignored.
func phoneUnroutedNotificationID(at time.Time) string {
	if at.IsZero() {
		at = time.Now()
	}
	return fmt.Sprintf("%s%d", phoneNotificationNoticeIDPrefix, at.UnixNano())
}
