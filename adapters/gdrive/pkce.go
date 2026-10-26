package gdrive

import (
	"io"

	"github.com/mslxl/natcatty/adapters/oauthloopback"
)

// PKCE is the provider-neutral proof key the shared loopback flow generates.
// It stays exported from this package so existing adapters and tests keep one
// vocabulary for the flow.
type PKCE = oauthloopback.PKCE

// NewPKCE builds a verifier, its S256 challenge, and a state. It delegates to
// the shared loopback flow so Google Drive and OneDrive cannot drift.
func NewPKCE(random io.Reader) (PKCE, error) {
	return oauthloopback.NewPKCE(random)
}
