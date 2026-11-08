package remote

import (
	"context"
	"net/http"
	"strings"
	"testing"

	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

// TestProductionFactoryDefaultsAreSuitableForProduction pins the real factory
// used by the composition root: non-nil, bounded timeout, redirect refusal, and
// a product User-Agent instead of a placeholder.
func TestProductionFactoryDefaultsAreSuitableForProduction(t *testing.T) {
	factory := NewFactory(Options{})
	if factory == nil {
		t.Fatal("NewFactory returned nil")
	}
	if factory.userAgent != DefaultUserAgent {
		t.Fatalf("default User-Agent = %q, want %q", factory.userAgent, DefaultUserAgent)
	}
	if !strings.HasPrefix(factory.userAgent, "natcatty/") {
		t.Fatalf("default User-Agent = %q, want a natcatty product name", factory.userAgent)
	}
	if strings.Contains(factory.userAgent, "netcatty") {
		t.Fatalf("default User-Agent = %q, want no placeholder product name", factory.userAgent)
	}
	if factory.http == nil {
		t.Fatal("the factory has no HTTP client")
	}
	if factory.http.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", factory.http.Timeout, DefaultTimeout)
	}
	if err := factory.http.CheckRedirect(&http.Request{}, nil); err != http.ErrUseLastResponse {
		t.Fatalf("redirect policy = %v, want ErrUseLastResponse", err)
	}

	remote, err := factory.Open(context.Background(), historysync.Config{
		BaseURL: "http://127.0.0.1:1", AuthMode: atuinmodel.AuthModeSession,
		Token: "loopback-test-token",
	})
	if err != nil {
		t.Fatalf("Open with the production factory: %v", err)
	}
	if remote == nil {
		t.Fatal("Open returned a nil remote")
	}
	_ = remote.Close()
}

func TestUserAgentForVersionNamesTheProductAndVersion(t *testing.T) {
	for input, want := range map[string]string{
		"1.2.3":    "natcatty/1.2.3",
		" 1.2.3  ": "natcatty/1.2.3",
		"":         DefaultUserAgent,
		"   ":      DefaultUserAgent,
	} {
		if got := UserAgentForVersion(input); got != want {
			t.Errorf("UserAgentForVersion(%q) = %q, want %q", input, got, want)
		}
	}
}
