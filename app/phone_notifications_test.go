package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// fakePhoneNotificationSender records what would have reached the system. It
// keeps the tests off the OS notification path and its permission prompt.
type fakePhoneNotificationSender struct {
	supported bool
	showErr   error
	shown     []fakePhoneNotification
}

type fakePhoneNotification struct {
	id    string
	title string
	body  string
}

func (f *fakePhoneNotificationSender) Supported() bool { return f.supported }

func (f *fakePhoneNotificationSender) Show(id, title, body string) error {
	f.shown = append(f.shown, fakePhoneNotification{id: id, title: title, body: body})
	return f.showErr
}

// fakePhoneNotificationRoutes stands in for the Shell's SessionID lookup.
type fakePhoneNotificationRoutes struct {
	route phoneshell.SessionRoute
	ok    bool
	asked []model.SessionID
}

func (f *fakePhoneNotificationRoutes) RouteForSession(id model.SessionID) (phoneshell.SessionRoute, bool) {
	f.asked = append(f.asked, id)
	return f.route, f.ok
}

// fakeNotificationHookSetter captures the hook the composition root installs.
type fakeNotificationHookSetter struct {
	hook func(model.SessionID, appsession.TerminalNotification)
}

func (f *fakeNotificationHookSetter) SetNotificationHook(
	fn func(model.SessionID, appsession.TerminalNotification)) {

	f.hook = fn
}

func alwaysNotify() domainterminal.NotificationMode { return domainterminal.NotificationAlways }

// synchronousBridge makes delivery deterministic in tests. Production leaves
// dispatch nil, which schedules the system call off the Session read loop.
func synchronousBridge(sender phoneNotificationSender, routes phoneNotificationRouteSource,
	mode func() domainterminal.NotificationMode, focus func() bool) *phoneNotificationBridge {

	bridge := newPhoneNotificationBridge(sender, routes, mode, focus)
	bridge.dispatch = func(job func()) { job() }
	return bridge
}

// TestPhoneNotificationHookDeliversTitleBodyAndRoute is the core delivery
// story: the registered hook receives a parsed program notification and the
// notifier receives exactly the fixed payload plus an ID that names the owning
// Tab and Pane. The program's own text never reaches the sender.
func TestPhoneNotificationHookDeliversTitleBodyAndRoute(t *testing.T) {
	route := phoneshell.SessionRoute{Tab: "tab-2", Pane: "pane-tab-2-3"}
	sender := &fakePhoneNotificationSender{supported: true}
	routes := &fakePhoneNotificationRoutes{route: route, ok: true}
	setter := &fakeNotificationHookSetter{}
	bridge := synchronousBridge(sender, routes, alwaysNotify, nil)
	installPhoneNotificationHook(setter, bridge.deliver)
	if setter.hook == nil {
		t.Fatal("the composition root did not install a notification hook")
	}

	setter.hook("session-7", appsession.TerminalNotification{
		Title: "Deploy finished",
		Body:  "all hosts are healthy",
		At:    time.Unix(1_700_000_000, 42),
	})

	if len(sender.shown) != 1 {
		t.Fatalf("notifier received %d notifications, want 1", len(sender.shown))
	}
	got := sender.shown[0]
	if got.title != phoneNotificationFixedTitle || got.body != phoneNotificationFixedBody {
		t.Fatalf("notification = %+v, want the fixed payload (%q, %q)",
			got, phoneNotificationFixedTitle, phoneNotificationFixedBody)
	}
	if strings.Contains(got.title, "Deploy finished") || strings.Contains(got.body, "all hosts are healthy") {
		t.Fatalf("the program's text reached the notification: %+v", got)
	}
	if want := phoneNotificationID(route); got.id != want {
		t.Fatalf("notification ID = %q, want %q", got.id, want)
	}
	parsed, ok := parsePhoneNotificationID(got.id)
	if !ok || parsed != route {
		t.Fatalf("notification ID %q parsed to %+v (%v), want %+v", got.id, parsed, ok, route)
	}
	if len(routes.asked) != 1 || routes.asked[0] != "session-7" {
		t.Fatalf("route lookup asked for %v, want session-7", routes.asked)
	}
}

// TestPhoneNotificationPayloadNeverCarriesRemoteText is the box-5 acceptance
// check. Terminal notification text is arbitrary program output, so the design
// does not inspect it: the bridge sends a constant payload and the program's
// text never reaches the sender. Each row is a hostile shape (including the
// adversarial verifier's survivors) placed in BOTH the title and the body.
// The credentials are inert test values, not real secrets.
func TestPhoneNotificationPayloadNeverCarriesRemoteText(t *testing.T) {
	// The PEM fixture is assembled from pieces so the literal key header does
	// not look like a real key blob to a scanner. Its body is inert base64.
	pemKey := "-----BEGIN " + "OPENSSH " + "PRIVATE KEY-----\n" +
		"ZmFrZS1rZXktYm9keS1ub3QtYS1zZWNyZXQ" +
		"-----END " + "OPENSSH " + "PRIVATE KEY-----"

	route := phoneshell.SessionRoute{Tab: "tab-3", Pane: "pane-tab-3-2"}
	cases := []struct {
		name    string
		input   string
		markers []string
	}{
		{
			name:    "prose labelled token",
			input:   "the token is hunter2",
			markers: []string{"hunter2", "token is"},
		},
		{
			name:    "prose labelled secret",
			input:   "the secret is hunter2",
			markers: []string{"hunter2", "secret is"},
		},
		{
			name:    "prose unlabelled random secret",
			input:   "our secret is correcthorsebatterystaple",
			markers: []string{"correcthorsebatterystaple"},
		},
		{
			name:    "space-delimited token",
			input:   "token abcd",
			markers: []string{"abcd"},
		},
		{
			name:    "tab-delimited token",
			input:   "token\tabcd",
			markers: []string{"abcd"},
		},
		{
			name:    "bearer prose",
			input:   "bearer hunter2",
			markers: []string{"hunter2", "bearer"},
		},
		{
			name:    "authorization bearer header without colon",
			input:   "Authorization Bearer hunter2",
			markers: []string{"hunter2", "Authorization"},
		},
		{
			name:    "cyrillic confusable label",
			input:   "tоken hunter2",
			markers: []string{"hunter2", "оken"},
		},
		{
			name:    "fullwidth confusable label",
			input:   "ｔｏｋｅｎ hunter2",
			markers: []string{"hunter2", "ｔｏｋｅｎ"},
		},
		{
			name:    "delimiter-free passphrase",
			input:   "correcthorsebatterystaple",
			markers: []string{"correcthorse", "correcthorsebatterystaple"},
		},
		{
			name:    "PEM private key",
			input:   pemKey,
			markers: []string{"ZmFrZS1rZXktYm9keS1ub3QtYS1zZWNyZXQ", "BEGIN OPENSSH"},
		},
		{
			name:    "pgpass line",
			input:   "db.example.com:5432:appdb:deploy:hunter2",
			markers: []string{"hunter2", "appdb", "deploy"},
		},
		{
			name:    "JSON credentials",
			input:   `{"token":"hunter2","password":"abcd"}`,
			markers: []string{"hunter2", "abcd"},
		},
		{
			name:    "Authorization header",
			input:   "Authorization: Basic dXNlcjpwYXNz",
			markers: []string{"dXNlcjpwYXNz", "Authorization"},
		},
		{
			name:    "key=value",
			input:   "api_key=hunter2",
			markers: []string{"hunter2", "api_key"},
		},
		{
			name:    "URL userinfo",
			input:   "postgres://deploy:pw-hunter2@db.example.com/app",
			markers: []string{"hunter2", "deploy"},
		},
	}

	sender := &fakePhoneNotificationSender{supported: true}
	routes := &fakePhoneNotificationRoutes{route: route, ok: true}
	bridge := synchronousBridge(sender, routes, alwaysNotify, nil)
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			before := len(sender.shown)
			bridge.deliver("session-hostile", appsession.TerminalNotification{
				Title: tt.input,
				Body:  tt.input,
				At:    time.Unix(1_700_000_000, 0),
			})
			if len(sender.shown) != before+1 {
				t.Fatalf("notifier received %d notifications, want %d", len(sender.shown), before+1)
			}
			shown := sender.shown[before]
			if shown.title != phoneNotificationFixedTitle || shown.body != phoneNotificationFixedBody {
				t.Fatalf("notification payload = (%q, %q), want exactly (%q, %q)",
					shown.title, shown.body, phoneNotificationFixedTitle, phoneNotificationFixedBody)
			}
			for _, marker := range tt.markers {
				if strings.Contains(shown.title, marker) || strings.Contains(shown.body, marker) {
					t.Fatalf("notification leaked %q: title=%q body=%q", marker, shown.title, shown.body)
				}
			}
			if want := phoneNotificationID(route); shown.id != want {
				t.Fatalf("notification ID = %q, want %q", shown.id, want)
			}
			parsed, ok := parsePhoneNotificationID(shown.id)
			if !ok || parsed != route {
				t.Fatalf("notification ID %q parsed to %+v (%v), want %+v", shown.id, parsed, ok, route)
			}
		})
	}
}

// TestPhoneNotificationBenignTextIsAlsoReplaced pins that the fixed payload is
// not a judgement on the program's text: an obviously harmless message is
// replaced too, because the design never inspects content.
func TestPhoneNotificationBenignTextIsAlsoReplaced(t *testing.T) {
	sender := &fakePhoneNotificationSender{supported: true}
	bridge := synchronousBridge(sender,
		&fakePhoneNotificationRoutes{route: phoneshell.SessionRoute{Tab: "tab-1", Pane: "pane-1"}, ok: true},
		alwaysNotify, nil)

	bridge.deliver("session-benign", appsession.TerminalNotification{
		Title: "Build finished",
		Body:  "all hosts are healthy",
	})

	if len(sender.shown) != 1 {
		t.Fatalf("notifier received %d notifications, want 1", len(sender.shown))
	}
	got := sender.shown[0]
	if got.title != phoneNotificationFixedTitle || got.body != phoneNotificationFixedBody {
		t.Fatalf("benign notification = (%q, %q), want exactly (%q, %q)",
			got.title, got.body, phoneNotificationFixedTitle, phoneNotificationFixedBody)
	}
}

// TestPhoneNotificationEmptyRequestIsSuppressed pins that a bare request with
// neither a program title nor a body is not worth a system banner, while a
// request with either one still notifies with the fixed payload.
func TestPhoneNotificationEmptyRequestIsSuppressed(t *testing.T) {
	sender := &fakePhoneNotificationSender{supported: true}
	bridge := synchronousBridge(sender,
		&fakePhoneNotificationRoutes{route: phoneshell.SessionRoute{Tab: "tab-1", Pane: "pane-1"}, ok: true},
		alwaysNotify, nil)

	bridge.deliver("session-empty", appsession.TerminalNotification{})
	if len(sender.shown) != 0 {
		t.Fatalf("an empty request delivered %d notifications", len(sender.shown))
	}

	bridge.deliver("session-title-only", appsession.TerminalNotification{Title: "ping"})
	if len(sender.shown) != 1 {
		t.Fatalf("a title-only request delivered %d notifications, want 1", len(sender.shown))
	}
	bridge.deliver("session-body-only", appsession.TerminalNotification{Body: "pong"})
	if len(sender.shown) != 2 {
		t.Fatalf("a body-only request delivered %d notifications, want 2", len(sender.shown))
	}
}

// TestPhoneNotificationPermissionDenialKeepsDeliveryWorking pins that a denied
// permission is consumed at the notification boundary: it neither crashes nor
// disables later notifications, and the Session keeps running.
func TestPhoneNotificationPermissionDenialKeepsDeliveryWorking(t *testing.T) {
	// The mobile host reports a denial as a plain string across the native
	// bridge, not as the platform sentinel.
	sender := &fakePhoneNotificationSender{
		supported: true,
		showErr:   errors.New("mygo: notifications are not allowed"),
	}
	routes := &fakePhoneNotificationRoutes{
		route: phoneshell.SessionRoute{Tab: "tab-1", Pane: "pane-1"}, ok: true,
	}
	bridge := synchronousBridge(sender, routes, alwaysNotify, nil)

	bridge.deliver("session-1", appsession.TerminalNotification{Title: "hello", Body: "world"})
	if len(sender.shown) != 1 {
		t.Fatalf("notifier received %d notifications, want 1", len(sender.shown))
	}

	// The platform sentinel is still recognized too.
	sender.showErr = mygo.ErrNotificationsDenied
	bridge.deliver("session-1", appsession.TerminalNotification{Title: "still denied", Body: "ignored"})

	// A later notification after the user allows them still goes through: the
	// denial did not latch delivery off.
	sender.showErr = nil
	bridge.deliver("session-1", appsession.TerminalNotification{Title: "again", Body: "works"})
	if len(sender.shown) != 3 {
		t.Fatalf("notifier received %d notifications after permission returned, want 3", len(sender.shown))
	}

	// The user's "off" mode and an unsupported sender drop notifications
	// silently; neither touches the Session.
	muted := &fakePhoneNotificationSender{supported: true}
	off := synchronousBridge(muted, routes,
		func() domainterminal.NotificationMode { return domainterminal.NotificationOff }, nil)
	off.deliver("session-1", appsession.TerminalNotification{Title: "muted", Body: "muted"})
	if len(muted.shown) != 0 {
		t.Fatalf("the off mode delivered %d notifications", len(muted.shown))
	}
	unsupportedSender := &fakePhoneNotificationSender{}
	unsupported := synchronousBridge(unsupportedSender, routes, alwaysNotify, nil)
	unsupported.deliver("session-1", appsession.TerminalNotification{Title: "none", Body: "none"})
	if len(unsupportedSender.shown) != 0 {
		t.Fatalf("an unsupported sender delivered %d notifications", len(unsupportedSender.shown))
	}
}

// TestPhoneNotificationUnfocusedModeUsesAppFocus pins the shared "unfocused"
// mode: a notification is suppressed while the app is active and delivered
// while it is not.
func TestPhoneNotificationUnfocusedModeUsesAppFocus(t *testing.T) {
	sender := &fakePhoneNotificationSender{supported: true}
	active := true
	bridge := synchronousBridge(sender, &fakePhoneNotificationRoutes{},
		func() domainterminal.NotificationMode { return domainterminal.NotificationUnfocused },
		func() bool { return active })

	bridge.deliver("session-1", appsession.TerminalNotification{Title: "while active", Body: "suppressed"})
	if len(sender.shown) != 0 {
		t.Fatalf("the unfocused mode notified while the app was active")
	}
	active = false
	bridge.deliver("session-1", appsession.TerminalNotification{Title: "in background", Body: "delivered"})
	if len(sender.shown) != 1 {
		t.Fatalf("the unfocused mode delivered %d notifications in the background, want 1", len(sender.shown))
	}
}

// TestPhoneNotificationClickOpensTheRoutedTabAndPane covers parsing the system
// click payload and routing it to the shell. A foreign or malformed ID does
// nothing.
func TestPhoneNotificationClickOpensTheRoutedTabAndPane(t *testing.T) {
	route := phoneshell.SessionRoute{Tab: "tab-9", Pane: "pane-tab-9-2"}
	shower := &fakePhoneRouteShower{}
	if !applyPhoneNotificationClick(shower, phoneNotificationID(route)) {
		t.Fatal("a phone notification ID did not route")
	}
	if !shower.shown || shower.route != route {
		t.Fatalf("shown route = %+v (%v), want %+v", shower.route, shower.shown, route)
	}

	for _, id := range []string{
		"", "other-app.session:1", phoneNotificationIDPrefix,
		phoneNotificationNoticeIDPrefix, phoneUnroutedNotificationID(time.Unix(1_700_000_000, 0)),
	} {
		if applyPhoneNotificationClick(shower, id) {
			t.Fatalf("foreign or malformed ID %q routed", id)
		}
	}
}

// fakePhoneRouteShower records the route a click asked the shell to show.
type fakePhoneRouteShower struct {
	route phoneshell.SessionRoute
	shown bool
}

func (f *fakePhoneRouteShower) ShowSessionRoute(route phoneshell.SessionRoute) bool {
	f.route, f.shown = route, true
	return true
}
