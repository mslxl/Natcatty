package main

import "slices"

// A required behavior is a gate item from ticket 27's acceptance list. Each
// behavior maps to one or more tests. Every reference must be a test that
// actually ran and passed in the named suite; a missing or skipped test fails
// the behavior, so renaming or deleting a test cannot silently shrink the
// gate. Every behavior must have at least one real-server (lab) test: the gate
// proves behavior against stock etserver, not only against fakes.
type Behavior struct {
	ID       string
	Title    string
	Versions []string
	Refs     []TestRef
}

// TestRef names one test result to require.
type TestRef struct {
	// Suite is "lab" (NATCATTY_ET_LAB=1, real SSH and stock etserver) or
	// "unit" (hermetic protocol and adapter tests).
	Suite string
	// Package is the full Go import path.
	Package string
	// Name is an anchored regular expression for the test name. Subtests use
	// their slash-separated name.
	Name string
	// Versions, when non-empty, limits the reference to those stock server
	// versions. Empty means every version the behavior applies to.
	Versions []string
}

const (
	suiteLab  = "lab"
	suiteUnit = "unit"

	wirePackage    = "github.com/mslxl/natcatty/adapters/et/internal/etgo/wire"
	sealPackage    = "github.com/mslxl/natcatty/adapters/et/internal/etgo/seal"
	etcpPackage    = "github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	sessionPackage = "github.com/mslxl/natcatty/adapters/et/internal/etgo/session"
	serverPackage  = "github.com/mslxl/natcatty/adapters/et/internal/etgo/etservertest"
)

func lab(name string) TestRef  { return TestRef{Suite: suiteLab, Package: etPackage, Name: name} }
func unit(name string) TestRef { return TestRef{Suite: suiteUnit, Package: etPackage, Name: name} }

func wireTest(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: wirePackage, Name: name}
}

func sealTest(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: sealPackage, Name: name}
}

func etcpTest(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: etcpPackage, Name: name}
}

func sessionTest(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: sessionPackage, Name: name}
}

func serverTest(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: serverPackage, Name: name}
}

// Behaviors is the full required set. A version in Versions selects the
// stock server versions the behavior is required on; 6.2.10 has no v7
// challenge/reset or RETRY_LATER, so those are 7.1.1-only.
var Behaviors = []Behavior{
	{
		ID: "server-identity", Title: "cell runs the pinned stock etserver version",
		Versions: []string{v711, v6210},
		Refs:     []TestRef{lab(`TestLabServerVersionIsPinned`)},
	},
	{
		ID: "bootstrap", Title: "SSH bootstrap starts etterminal and parses IDPASSKEY",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestInProcessAgainstStockServers/etserver-.*`),
			lab(`TestInProcessThroughARealProxy`),
		},
	},
	{
		ID: "auth-password", Title: "password authentication through the shared Connection Manager",
		Versions: []string{v711, v6210},
		Refs:     []TestRef{lab(`TestLabAuthenticationMethods/etserver-.*/password`)},
	},
	{
		ID: "auth-key", Title: "selected private key authentication",
		Versions: []string{v711, v6210},
		Refs:     []TestRef{lab(`TestLabAuthenticationMethods/etserver-.*/selected-key`)},
	},
	{
		ID: "auth-agent", Title: "ssh-agent authentication",
		Versions: []string{v711, v6210},
		Refs:     []TestRef{lab(`TestLabAuthenticationMethods/etserver-.*/agent`)},
	},
	{
		ID: "auth-certificate", Title: "user-certificate authentication",
		Versions: []string{v711, v6210},
		Refs:     []TestRef{lab(`TestLabAuthenticationMethods/etserver-.*/certificate`)},
	},
	{
		ID: "auth-identity", Title: "Vault-selected identity overrides the fallback username",
		Versions: []string{v711, v6210},
		Refs:     []TestRef{lab(`TestLabAuthenticationMethods/etserver-.*/identity`)},
	},
	{
		ID: "routing-proxy", Title: "HTTP CONNECT proxy route",
		Versions: []string{v711, v6210},
		Refs:     []TestRef{lab(`TestInProcessThroughARealProxy`)},
	},
	{
		ID: "routing-jump", Title: "real SSH jump-host route",
		Versions: []string{v711, v6210},
		Refs:     []TestRef{lab(`TestInProcessThroughARealJumpHost`)},
	},
	{
		ID: "framing", Title: "host-order handshake and 4-byte network-order packet framing",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestLabLargeOutputFramingAndSecretbox`),
			wireTest(`TestFrameRoundTrip`),
			wireTest(`TestFrameSizeBoundary`),
			wireTest(`TestFrameLengthIsBigEndian`),
			wireTest(`FuzzReadFrame`),
			wireTest(`FuzzParsePacket`),
		},
	},
	{
		ID: "secretbox", Title: "libsodium secretbox with direction nonces and stream ordering",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestLabLargeOutputFramingAndSecretbox`),
			sealTest(`TestNaClVector`),
			sealTest(`TestGolden`),
			sealTest(`TestGoldenOpen`),
			sealTest(`FuzzOpen`),
		},
	},
	{
		ID: "initial-payload", Title: "INITIAL_PAYLOAD is the first packet and starts the shell",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestInProcessAgainstStockServers/etserver-.*`),
			sessionTest(`TestStartSendsPayloadAndAcceptsResponse`),
			sessionTest(`TestStartRejected`),
		},
	},
	{
		ID: "keepalive", Title: "ET keepalive probes keep an idle link up",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestLabKeepAliveKeepsTheLinkWithoutReconnect`),
			serverTest(`TestServerEchoesKeepAlive`),
			etcpTest(`TestLivenessKeepsHealthyLink`),
		},
	},
	{
		ID: "reconnect-network-change", Title: "network change reconnects without a second bootstrap",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestLabNetworkChangeReconnectsWithoutBootstrap/etserver-.*`),
			etcpTest(`TestReconnectAfterCut`),
			etcpTest(`TestReconnectSurvivesCutsAnywhere`),
		},
	},
	{
		ID: "reconnect-client-restart", Title: "a fresh process reattaches where stock supports it",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestLabAppRestartReattach/etserver-.*`),
			unit(`TestFreshProcessReattachesBlankAndSkipsBootstrap`),
		},
	},
	{
		ID: "replay-window", Title: "SequenceHeader/CatchupBuffer replay and the 64 MiB window boundary",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestLabNetworkChangeReconnectsWithoutBootstrap/etserver-.*`),
			etcpTest(`TestRingReplaysAtThe64MiBBoundary`),
			etcpTest(`TestReplayWindowExceeded`),
			etcpTest(`TestCatchupBothWays`),
			etcpTest(`FuzzReplayHandling`),
			unit(`TestReplayWindowExceededIsATypedUnavailableSession`),
		},
	},
	{
		ID: "v7-challenge", Title: "v7 challenge authentication for a first and returning client",
		Versions: []string{v711},
		Refs: []TestRef{
			lab(`TestInProcessAttachDisplacesFirstClientAgainstStock711`),
			etcpTest(`TestDialChallengeAuthentication`),
		},
	},
	{
		ID: "v7-reset-attach", Title: "v7 reset/attach starts blank and keeps shell state",
		Versions: []string{v711},
		Refs: []TestRef{
			lab(`TestInProcessAttachDisplacesFirstClientAgainstStock711`),
			lab(`TestLabAppRestartReattach/etserver-7\.1\.1`),
			etcpTest(`TestDialChallengeResetAttach`),
			unit(`TestFreshProcessReattachesBlankAndSkipsBootstrap`),
		},
	},
	{
		ID: "legacy-reattach", Title: "v7-only attach against 6.2.10 fails like stock, not silently",
		Versions: []string{v6210},
		Refs: []TestRef{
			lab(`TestInProcessLegacyReattachFailsLikeStock6210`),
			etcpTest(`TestLegacyServerRejectsResetIntent`),
		},
	},
	{
		ID: "v7-exit-status", Title: "terminal exit status is propagated (and absent on 6.2.10)",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestInProcessAgainstStockServers/etserver-.*`),
			sessionTest(`TestRunOutputAndExitStatus`),
		},
	},
	{
		ID: "retry-later", Title: "RETRY_LATER drives backoff instead of ending the Session",
		Versions: []string{v711},
		Refs: []TestRef{
			lab(`TestRetryLaterDrivesBackoffAgainstStock711`),
			unit(`TestRetryLaterRetriesWithBackoff`),
			etcpTest(`TestRetryLaterDuringReconnectIsNotFatal`),
		},
	},
	{
		// Displacement is a v7 reset-flow behavior. A second client on a
		// 6.2.10 server cannot enter that flow, so its required parity
		// outcome is the separate typed legacy-reattach refusal behavior.
		ID: "displacement", Title: "a second client displaces the first with a typed outcome",
		Versions: []string{v711},
		Refs: []TestRef{
			etcpTest(`TestPeerAheadOnReconnectIsDisplacement`),
			etcpTest(`TestServerSequenceGoingBackwardsIsDisplacement`),
			lab(`TestInProcessAttachDisplacesFirstClientAgainstStock711`),
			lab(`TestInProcessOfflineVictimIsDisplacedAgainstStock711`),
		},
	},
	{
		ID: "resize", Title: "terminal resize reaches the shell",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestInProcessAgainstStockServers/etserver-.*`),
			sessionTest(`TestRunSendsInitialSizeAndResizes`),
		},
	},
	{
		ID: "scrollback", Title: "bulk output is delivered in order without loss (client scrollback source)",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestLabScrollbackKeepsAllOutput`),
			sessionTest(`TestRunOutputAndExitStatus`),
		},
	},
	{
		ID: "golden-terminal-output", Title: "scripted terminal bytes match a golden and stock et",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestLabGoldenTerminalOutput`),
			lab(`TestLabMatchesStockETOutput`),
		},
	},
	{
		ID: "error-paths", Title: "auth, handshake, version, and legacy errors are typed",
		Versions: []string{v711, v6210},
		Refs: []TestRef{
			lab(`TestInProcessWrongPasswordIsATypedAuthError/etserver-.*`),
			lab(`TestInProcessHandshakeFailureIsTyped/etserver-.*`),
			lab(`TestInProcessUnsupportedServerIsTyped/etserver-.*`),
			etcpTest(`TestDialErrors`),
			sessionTest(`TestStartRejectedTextIsQuotedAndBounded`),
		},
	},
}

// BehaviorsForVersion returns the behaviors required for a stock version.
func BehaviorsForVersion(version string) []Behavior {
	var out []Behavior
	for _, behavior := range Behaviors {
		if slices.Contains(behavior.Versions, version) {
			out = append(out, behavior)
		}
	}
	return out
}

// RefsForVersion returns the test references required for one version. A
// reference with no Versions applies to every version of its behavior.
func RefsForVersion(behavior Behavior, version string) []TestRef {
	var out []TestRef
	for _, ref := range behavior.Refs {
		if len(ref.Versions) == 0 || slices.Contains(ref.Versions, version) {
			out = append(out, ref)
		}
	}
	return out
}
