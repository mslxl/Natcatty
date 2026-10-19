package app

import (
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func TestNotificationModePolicy(t *testing.T) {
	cases := []struct {
		name    string
		mode    domainterminal.NotificationMode
		focused bool
		want    bool
	}{
		{"off focused", domainterminal.NotificationOff, true, false},
		{"off unfocused", domainterminal.NotificationOff, false, false},
		{"unfocused while unfocused", domainterminal.NotificationUnfocused, false, true},
		{"unfocused while focused", domainterminal.NotificationUnfocused, true, false},
		{"always focused", domainterminal.NotificationAlways, true, true},
		{"always unfocused", domainterminal.NotificationAlways, false, true},
	}
	for _, test := range cases {
		if got := notificationAllowed(test.mode, test.focused); got != test.want {
			t.Fatalf("%s = %v, want %v", test.name, got, test.want)
		}
	}
}
