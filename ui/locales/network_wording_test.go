// SPDX-License-Identifier: GPL-3.0-or-later

package locales

import (
	"strings"
	"testing"
)

// TestNetworkPrivacyNoticeDisclosesTheLogtailUpload pins the disclosure the
// Tailscale Net editor must show: tsnet uploads operational logs to
// log.tailscale.com by default, and this build has no switch to disable it.
// A translation may word the rest differently, but the destination must stay
// readable so the notice cannot silently lose the fact it discloses.
func TestNetworkPrivacyNoticeDisclosesTheLogtailUpload(t *testing.T) {
	for _, tag := range Tags() {
		notice := In(tag).VaultNetworkPrivacyNotice()
		if !strings.Contains(notice, "log.tailscale.com") {
			t.Errorf("%s privacy notice = %q, missing log.tailscale.com", tag, notice)
		}
	}
	english := In("en").VaultNetworkPrivacyNotice()
	for _, phrase := range []string{"no switch", "self-hosted control server"} {
		if !strings.Contains(english, phrase) {
			t.Errorf("English privacy notice = %q, missing %q", english, phrase)
		}
	}
}
