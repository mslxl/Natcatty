// SPDX-License-Identifier: GPL-3.0-or-later

package locales

import (
	"strings"
	"testing"
)

// TestPlaintextWarningDisclosesUnencryptedTraffic pins the disclosure the Host
// editor shows for a protocol whose native transport is plaintext. The warning
// is shared by Telnet and VNC, and a future protocol inherits it by setting the
// descriptor capability. A translation may word it differently, but it must
// keep naming the protocol and the exposure.
func TestPlaintextWarningDisclosesUnencryptedTraffic(t *testing.T) {
	protocols := []string{"Telnet", "VNC"}
	for _, tag := range Tags() {
		for _, protocol := range protocols {
			warning := In(tag).VaultHostPlaintextWarning(protocol)
			if !strings.Contains(warning, protocol) {
				t.Errorf("%s %s warning = %q, missing the protocol name", tag, protocol, warning)
			}
		}
	}
	for _, protocol := range protocols {
		english := In("en").VaultHostPlaintextWarning(protocol)
		for _, phrase := range []string{"plaintext", "password", "unencrypted"} {
			if !strings.Contains(english, phrase) {
				t.Errorf("English %s warning = %q, missing %q", protocol, english, phrase)
			}
		}
	}
}
