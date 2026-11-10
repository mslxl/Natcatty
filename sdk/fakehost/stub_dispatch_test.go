package fakehost

import (
	"context"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// TestStubDispatcherRendersTheSharedNotFoundEnvelope locks the fake host's
// absent-stub bytes. sdk/rust/tests/sdk.rs asserts the in-language mock returns
// the same envelopes, so an undefined stub behaves identically in both hosts.
func TestStubDispatcherRendersTheSharedNotFoundEnvelope(t *testing.T) {
	dispatcher := stubDispatcher{}
	for _, test := range []struct {
		name    string
		service string
		method  uint32
		want    string
	}{
		{
			name:    "unknown method",
			service: "storage",
			method:  999,
			want:    `{"code":"not-found","message":"unknown method 999 for service \"storage\""}`,
		},
		{
			name:    "declared method without a stub",
			service: "files",
			method:  2,
			want:    `{"code":"not-found","message":"no stub response for files.read"}`,
		},
		{
			name:    "unknown service",
			service: "nope",
			method:  1,
			want:    `{"code":"not-found","message":"unknown service \"nope\""}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := dispatcher.Call(context.Background(), appplugin.ServiceCall{
				Service: test.service,
				Method:  test.method,
			})
			if string(payload) != test.want {
				t.Fatalf("payload = %s, want %s", payload, test.want)
			}
		})
	}
}
