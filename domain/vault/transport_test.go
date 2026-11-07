package vault

import (
	"encoding/json"
	"testing"
)

// TestHostJSONIgnoresTheRemovedImplementation keeps old Host documents
// loadable: a transportImplementation value written by an older build is
// ignored by encoding/json, so no migration is needed and the Host still
// validates.
func TestHostJSONIgnoresTheRemovedImplementation(t *testing.T) {
	var host Host
	document := `{"id":"old","label":"web","hostname":"example.com",` +
		`"protocol":"mosh","transportImplementation":"bundled"}`
	if err := json.Unmarshal([]byte(document), &host); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("an old Host no longer validates: %v", err)
	}
}
