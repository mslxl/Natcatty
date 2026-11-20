package connection

import "testing"

// TestRemoteRequiresSingleChannelRecognizesRussh pins the russh software token
// against its nearest lookalikes. russh accepts one session channel per TCP
// connection, so SCP fallback would tear down the user's shell; a name that
// merely contains "russh" must not inherit that classification.
func TestRemoteRequiresSingleChannelRecognizesRussh(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"bare token", "russh", true},
		{"underscore version", "SSH-2.0-russh_0.40.0", true},
		{"dash version", "SSH-2.0-russh-0.40", true},
		{"space separated", "russh 0.40", true},
		{"case insensitive", "SSH-2.0-Russh", true},
		{"not a daemon name", "SSH-2.0-russhd_1.0", false},
		{"prefixed by a word", "SSH-2.0-myrussh", false},
		{"prefixed by a product", "SSH-2.0-notRussh", false},
		{"suffixed by a word", "SSH-2.0-russhx", false},
		{"suffixed by a digit", "SSH-2.0-russh0", false},
		{"leading letter", "SSH-2.0-xrussh", false},
		{"multiplexing peer", "SSH-2.0-OpenSSH_9.6", false},
		{"empty banner", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RemoteRequiresSingleChannel(test.version); got != test.want {
				t.Fatalf("RemoteRequiresSingleChannel(%q) = %v, want %v",
					test.version, got, test.want)
			}
		})
	}
}

// TestRusshStaysEligibleForParkedShellReuse proves the token only changes
// channel planning. A single-channel peer still accepts a shell on a parked
// transport; only TERM-SSHD, which exits immediately, is barred from reuse.
func TestRusshStaysEligibleForParkedShellReuse(t *testing.T) {
	if !RemoteAllowsIdleParkedShellReuse("SSH-2.0-russh_0.40.0") {
		t.Fatal("russh was barred from parked-shell reuse")
	}
}
