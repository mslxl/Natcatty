// SPDX-License-Identifier: GPL-3.0-or-later

package main

import "slices"

// A required behavior is a gate item from ticket 32's acceptance list. Each
// behavior maps to one or more tests. Every reference must be a test that
// actually ran and passed in the named suite; a missing or skipped test fails
// the behavior, so renaming or deleting a test cannot silently shrink the
// gate. References in the lab suite exercise a live stock mosh-server over
// real UDP (bootstrap, encryption, SSP, resize, shutdown, locale, roaming);
// client-model behavior (prediction, EchoAck, terminal goldens) is proven
// against the stock oracle and the committed goldens. The stock-client cell
// requires the byte-for-byte display differential.
type Behavior struct {
	ID    string
	Title string
	Refs  []TestRef
}

// TestRef names one test result to require.
type TestRef struct {
	// Suite is "unit" (hermetic protocol, terminal, and adapter tests), "lab"
	// (NATCATTY_MOSH_INTEGRATION=1, real SSH and stock mosh-server),
	// "stock" (NATCATTY_MOSH_PARITY=1, stock mosh-client in Docker), or
	// "fuzz" (a bounded native fuzz run).
	Suite string
	// Package is the full Go import path.
	Package string
	// Name is an anchored regular expression for the test name. Subtests use
	// their slash-separated name.
	Name string
	// Versions, when non-empty, limits the reference to those stock server
	// versions.
	Versions []string
	// RequiresBundleTarget marks a test that drives the in-process Factory,
	// which resolves the host's bundled mosh-client before it can open a
	// Session. A harness host adapters/bundle has no target for (FreeBSD)
	// cannot run the test at all, so the reference is recorded as skipped
	// there instead of failing the behavior; the behavior's other references
	// must still pass on that host.
	RequiresBundleTarget bool
}

const (
	suiteUnit  = "unit"
	suiteLab   = "lab"
	suiteStock = "stock"
	suiteFuzz  = "fuzz"

	modulePath = "github.com/mslxl/natcatty"

	internalMosh     = modulePath + "/internal/mosh"
	internalTerminal = modulePath + "/internal/mosh/terminal"
	domainMosh       = modulePath + "/domain/model/mosh"
	adapterMosh      = modulePath + "/adapters/mosh"
)

func unit(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: adapterMosh, Name: name}
}

// unitWithBundle is a unit test that opens a Session through the in-process
// Factory and therefore needs a bundled mosh-client target on the host.
func unitWithBundle(name string) TestRef {
	ref := unit(name)
	ref.RequiresBundleTarget = true
	return ref
}

func core(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: internalMosh, Name: name}
}

func terminal(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: internalTerminal, Name: name}
}

func model(name string) TestRef {
	return TestRef{Suite: suiteUnit, Package: domainMosh, Name: name}
}

func lab(name string) TestRef   { return TestRef{Suite: suiteLab, Package: adapterMosh, Name: name} }
func stock(name string) TestRef { return TestRef{Suite: suiteStock, Package: adapterMosh, Name: name} }

func stockTerminal(name string) TestRef {
	return TestRef{Suite: suiteStock, Package: internalTerminal, Name: name}
}

func fuzz(name string) TestRef {
	return TestRef{Suite: suiteFuzz, Package: internalMosh, Name: name}
}

func terminalFuzz(name string) TestRef {
	return TestRef{Suite: suiteFuzz, Package: internalTerminal, Name: name}
}

func modelFuzz(name string) TestRef {
	return TestRef{Suite: suiteFuzz, Package: domainMosh, Name: name}
}

// ServerBehaviors is the full required set for every mosh-server matrix cell.
var ServerBehaviors = []Behavior{
	{
		ID: "server-version", Title: "cell runs the pinned stock mosh-server version",
		Refs: []TestRef{lab(`TestInProcessAdapterLabServerVersion`)},
	},
	{
		ID: "bootstrap-announcement", Title: "SSH bootstrap and MOSH CONNECT announcement parsing",
		Refs: []TestRef{
			model(`TestParseConnectLine`),
			model(`TestParseIPLine`),
			model(`TestDecodeKey`),
			model(`TestSnifferChunkBoundaries`),
			model(`TestSnifferFlushUnterminated`),
			model(`TestSnifferInvalidAnnouncement`),
			unit(`TestInProcessAdapterAnnouncementErrors`),
			unit(`TestInProcessAdapterFallsBackFromAnnouncedAddress`),
			lab(`TestInProcessAdapterRealMoshServer/mosh-server_.*`),
		},
	},
	{
		ID: "ocb3", Title: "OCB3 authenticated encryption, RFC 7253 vectors, and tamper rejection",
		Refs: []TestRef{
			core(`TestOCB3RFC7253Vectors`),
			core(`TestOCB3RejectsTampering`),
			core(`TestOCB3CloseZeroizes`),
			core(`TestNewOCBRejectsKeySizes`),
			core(`TestTransportIgnoresReplayAndTampering`),
			lab(`TestInProcessAdapterRealMoshServer/mosh-server_.*`),
		},
	},
	{
		ID: "fragmentation", Title: "fragment marshalling, reassembly, reordering, and bounds",
		Refs: []TestRef{
			core(`TestFragmentMarshalRoundTrip`),
			core(`TestFragmentRejectsShortHeader`),
			core(`TestFragmentAssembler`),
			core(`TestFragmentAssemblerResetsOnNewID`),
			core(`TestFragmentAssemblerRejectsHugeFragmentNumber`),
			core(`TestTransportLargeHostOutputFragments`),
			core(`TestTransportReorderedFragmentDatagramsReassemble`),
			fuzz(`FuzzFragmentReassembly`),
		},
	},
	{
		ID: "loss-reorder-duplication", Title: "sustained loss, reordering, and duplication converge",
		Refs: []TestRef{
			core(`TestTransportSustainedLossConverges`),
			core(`TestTransportReorderedFragmentDatagramsReassemble`),
			core(`TestTransportDuplicateWireDatagramIsIdempotent`),
			unit(`TestInProcessSustainedLossConverges`),
			unit(`TestInProcessReorderingConverges`),
			unit(`TestInProcessDuplicatesAreIdempotent`),
		},
	},
	{
		ID: "ssp-diff-ack-throwaway", Title: "SSP state diff, acknowledgement, and throwaway",
		Refs: []TestRef{
			core(`TestTransportInitialSend`),
			core(`TestTransportHostStateThrowaway`),
			core(`TestTransportReceivesOutputAndSendsInput`),
			core(`TestTransportDelaysDataAckAndSendsItImmediatelyForShutdown`),
			core(`TestTransportStaleAckResendsFromAssumedBase`),
			core(`TestTransportShutdownRetransmitsAtFrameRate`),
			core(`TestSentStateNumbersTrackTheSendQueue`),
			core(`TestTransportOutOfOrderStates`),
			lab(`TestInProcessAdapterRealMoshServer/mosh-server_.*`),
		},
	},
	{
		ID: "prediction-confirmation-correction", Title: "prediction confirmation, correction, and modes",
		Refs: []TestRef{
			terminal(`TestPredictionDefaultsAndModes`),
			terminal(`TestPredictionConfirmationCorrectionAndEchoAck`),
			terminal(`TestPredictionEchoAckGatesMatchingAndWrongCells`),
			terminal(`TestPredictionOverwriteInsertion`),
			terminal(`TestPredictionNewlineAndUnknownPrint`),
			terminal(`TestPredictionNeverChangesServerFramebuffer`),
			terminal(`TestPredictionResizeResets`),
			unit(`TestInProcessPredictionDisplaysBeforeTheServerEcho`),
			unit(`TestInProcessPredictionNeverModeWithoutEcho`),
			unit(`TestInProcessPredictionAdaptiveFollowsSendInterval`),
			unit(`TestInProcessPredictionAdaptiveOnDelayedLink`),
		},
	},
	{
		ID: "echo-ack", Title: "EchoAck recording and gating",
		Refs: []TestRef{
			terminal(`TestCompleteEchoAck`),
			terminal(`TestPredictionConfirmationCorrectionAndEchoAck`),
			terminal(`TestPredictionEchoAckGatesMatchingAndWrongCells`),
		},
	},
	{
		ID: "resize", Title: "server-authoritative resize reaches the remote PTY and renderer",
		Refs: []TestRef{
			terminal(`TestCompleteResize`),
			unit(`TestRendererResizeBeforeOutput`),
			lab(`TestInProcessAdapterLabResizeAndShutdown`),
			lab(`TestInProcessAdapterRealMoshServer/mosh-server_.*`),
		},
	},
	{
		ID: "terminal-goldens", Title: "committed stock terminal golden screens",
		Refs: []TestRef{
			terminal(`TestGoldenTerminalScreens`),
			terminal(`TestWcwidthTableIsWellFormed`),
			unit(`TestRendererMatchesStockDisplayGoldens`),
		},
	},
	{
		ID: "roaming", Title: "roaming by client address change keeps the same session",
		Refs: []TestRef{
			unit(`TestInProcessAddressChangeContinuesSameSession`),
			unit(`TestInProcessAcceptsRepliesFromNewServerAddress`),
			{Suite: suiteLab, Package: adapterMosh, Name: `TestInProcessAdapterRealMoshRoaming`, Versions: []string{v140}},
		},
	},
	{
		ID: "timeout", Title: "probe timeout, RTO retransmission, and timeout classification",
		Refs: []TestRef{
			core(`TestTransportRetransmitsAfterRTO`),
			unit(`TestInProcessTimeoutClassification`),
			unit(`TestInProcessServerShutdownDuringProbe`),
			unit(`TestRendererConnectingTimeoutNotification`),
		},
	},
	{
		ID: "shutdown", Title: "shutdown sentinel, retransmission, and end classification",
		Refs: []TestRef{
			core(`TestTransportRemoteShutdown`),
			core(`TestTransportLocalShutdown`),
			core(`TestTransportCloseZeroizesAndRejectsUse`),
			unit(`TestInProcessServerShutdownSurvivesLoss`),
			unit(`TestInProcessUserShutdownSurvivesLostSentinel`),
			unit(`TestInProcessAdapterRemoteExit`),
			lab(`TestInProcessAdapterLabResizeAndShutdown`),
		},
	},
	{
		ID: "locale-environment", Title: "locale and environment propagation to mosh-server",
		Refs: []TestRef{
			unitWithBundle(`TestLocaleFromEnvironment`),
			model(`TestClientEnv`),
			model(`TestRemoteCommandDefaultsAndQuoting`),
			lab(`TestInProcessAdapterLabLocalePropagation`),
		},
	},
	{
		ID: "route-errors", Title: "route and bootstrap errors are typed and prompt",
		Refs: []TestRef{
			unit(`TestCheckRoute`),
			unit(`TestInProcessAdapterBootstrapRouteError`),
			unit(`TestInProcessAdapterAnnouncementErrors`),
			unit(`TestRendererNetworkErrorNotification`),
			unit(`TestTransportWriteErrorShowsNetworkNotification`),
		},
	},
	{
		ID: "fuzz-targets", Title: "bounded fuzzing of datagrams, fragments, instructions, and parsers",
		Refs: []TestRef{
			fuzz(`FuzzDatagramReceive`),
			fuzz(`FuzzFragmentReassembly`),
			fuzz(`FuzzTransportInstruction`),
			terminalFuzz(`FuzzTerminalParser`),
			modelFuzz(`FuzzAnnouncementParser`),
		},
	},
}

// StockBehaviors is the required set for the stock mosh-client cell. Display
// output is compared byte-for-byte for scripted Sessions; the terminal model
// and random streams are compared against the stock oracle.
var StockBehaviors = []Behavior{
	{
		ID: "display-byte-parity", Title: "Session display bytes equal stock mosh-client for scripted Sessions",
		Refs: []TestRef{
			stock(`TestSessionMatchesStockMoshClient`),
			stock(`TestSessionMatchesStockMoshClientWidthEdges`),
		},
	},
	{
		ID: "stock-terminal-model", Title: "terminal model frames and screens equal the stock oracle",
		Refs: []TestRef{
			stockTerminal(`TestStockParityTerminalModel`),
			stockTerminal(`TestStockParityRandom`),
		},
	},
}

// BehaviorsForCell returns the required behaviors for a cell's kind.
func BehaviorsForCell(cell Cell) []Behavior {
	if cell.Kind == kindStock {
		return StockBehaviors
	}
	return ServerBehaviors
}

// RefsForVersion filters a behavior's references to the cell's stock version.
// A reference with no matching version is not a waiver: a behavior whose
// applicable references all disappear fails evaluation.
func RefsForVersion(behavior Behavior, version string) []TestRef {
	refs := make([]TestRef, 0, len(behavior.Refs))
	for _, ref := range behavior.Refs {
		if len(ref.Versions) == 0 || slices.Contains(ref.Versions, version) {
			refs = append(refs, ref)
		}
	}
	return refs
}
