// SPDX-License-Identifier: GPL-3.0-or-later

package locales

import (
	"strings"
	"testing"
)

// TestTelnetPlaintextWarningDisclosesUnencryptedTraffic pins the disclosure the
// Host editor shows for a Telnet Host: the transport is plaintext, so the
// password crosses the network unencrypted. A translation may word it
// differently, but it must keep naming Telnet and the exposure.
func TestTelnetPlaintextWarningDisclosesUnencryptedTraffic(t *testing.T) {
	for _, tag := range Tags() {
		warning := In(tag).VaultHostTelnetPlaintextWarning()
		if !strings.Contains(warning, "Telnet") {
			t.Errorf("%s Telnet warning = %q, missing the protocol name", tag, warning)
		}
	}
	english := In("en").VaultHostTelnetPlaintextWarning()
	for _, phrase := range []string{"plaintext", "password", "unencrypted"} {
		if !strings.Contains(english, phrase) {
			t.Errorf("English Telnet warning = %q, missing %q", english, phrase)
		}
	}
}
