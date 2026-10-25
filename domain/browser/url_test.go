package browser

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeURLDefaultsLocalAddressesToHTTPAndOthersToHTTPS(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare localhost", "localhost", "http://localhost/"},
		{"localhost with port", "localhost:3000", "http://localhost:3000/"},
		{"localhost path", "localhost/admin", "http://localhost/admin"},
		{"loopback ipv4", "127.0.0.1:8080/status", "http://127.0.0.1:8080/status"},
		{"other loopback", "127.42.0.9", "http://127.42.0.9/"},
		{"bracketed ipv6", "[::1]:9000/", "http://[::1]:9000/"},
		{"public host", "example.com", "https://example.com/"},
		{"public host port", "example.com:8443", "https://example.com:8443/"},
		{"public path", "example.com/docs?q=1", "https://example.com/docs?q=1"},
		{"explicit http", "http://example.com/a", "http://example.com/a"},
		{"explicit https", "https://localhost/a", "https://localhost/a"},
		{"uppercase local", "LOCALHOST:3000", "http://localhost:3000/"},
		{"scheme uppercase", "HTTP://Example.COM/Path", "http://example.com/Path"},
		{"trim whitespace", "  example.com/x  ", "https://example.com/x"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeURL(test.input)
			if err != nil {
				t.Fatalf("NormalizeURL(%q) error: %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("NormalizeURL(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeURLRefusesInputsTheOriginalRefused(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{"empty", "   ", ErrAddressEmpty},
		{"too long", "example.com/" + strings.Repeat("a", MaxAddressLength), ErrAddressTooLong},
		{"javascript", "javascript:alert(1)", ErrAddressScheme},
		{"file", "file:///etc/passwd", ErrAddressScheme},
		{"credentials", "https://user:secret@example.com/", ErrAddressCredentials},
		{"missing host", "https:///path", ErrAddressInvalid},
		{"bad escape", "http://example.com/%zz", ErrAddressInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeURL(test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("NormalizeURL(%q) error = %v, want %v", test.input, err, test.want)
			}
		})
	}
}

func TestOriginSeparatesSchemeAuthorityAndPath(t *testing.T) {
	origin, err := Origin("https://Example.COM:8443/console?q=1#top")
	if err != nil {
		t.Fatal(err)
	}
	if origin != "https://Example.COM:8443" {
		t.Fatalf("Origin() = %q", origin)
	}
	if _, err := Origin("javascript:alert(1)"); !errors.Is(err, ErrAddressScheme) {
		t.Fatalf("Origin(javascript) error = %v", err)
	}
}
