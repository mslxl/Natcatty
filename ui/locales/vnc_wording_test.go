package locales

import (
	"strings"
	"testing"
)

// TestVNCUnencryptedWarningsNameVNCAndTheExposure pins both disclosures a VNC
// user can meet: the Host editor's plaintext-protocol line and the connection
// notice shown for an unrouted, genuinely unencrypted Session. Every catalog
// must keep naming VNC, and the English wording must still say what is at
// risk.
func TestVNCUnencryptedWarningsNameVNCAndTheExposure(t *testing.T) {
	for _, tag := range Tags() {
		messages := In(tag)
		for name, warning := range map[string]string{
			"editor":     messages.VaultHostPlaintextWarning("VNC"),
			"connection": messages.ConnectionVNCPlaintextWarning(),
			"prompt":     messages.ConnectionVNCPlaintextPromptBody("desk.example"),
		} {
			if !strings.Contains(warning, "VNC") {
				t.Errorf("%s %s warning = %q, missing the protocol name", tag, name, warning)
			}
		}
	}

	editor := In("en").VaultHostPlaintextWarning("VNC")
	for _, phrase := range []string{"plaintext", "unencrypted"} {
		if !strings.Contains(editor, phrase) {
			t.Errorf("English VNC editor warning = %q, missing %q", editor, phrase)
		}
	}
	connection := In("en").ConnectionVNCPlaintextWarning()
	for _, phrase := range []string{"not encrypted", "network"} {
		if !strings.Contains(connection, phrase) {
			t.Errorf("English VNC connection warning = %q, missing %q", connection, phrase)
		}
	}
}

// TestVNCKeysExistInEveryCatalog is the VNC slice of the catalog completeness
// rule: every key the VNC surfaces use is present with a non-empty value in
// all five shipped locales. The whole-catalog test already asserts identical
// key sets; this pins the VNC family explicitly so deleting a VNC string fails
// with a VNC-named test.
func TestVNCKeysExistInEveryCatalog(t *testing.T) {
	source := readCatalog(t, "en")
	var keys []string
	for key := range source {
		if strings.Contains(strings.ToLower(key), "vnc") {
			keys = append(keys, key)
		}
	}
	if len(keys) < 40 {
		t.Fatalf("found only %d VNC keys; the VNC UI strings are missing", len(keys))
	}
	for _, tag := range Tags() {
		catalog := readCatalog(t, tag)
		for _, key := range keys {
			value, ok := catalog[key]
			if !ok {
				t.Errorf("%s is missing VNC key %q", tag, key)
				continue
			}
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s has an empty VNC message for %q", tag, key)
			}
		}
	}
}
