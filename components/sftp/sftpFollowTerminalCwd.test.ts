import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  mergeLatestFollowTerminalCwdHostSetting,
  resolveHostFollowTerminalCwd,
  resolveSftpFollowTerminalCwdTargetHost,
  runInitialFollowTerminalCwdSync,
  shouldApplyFollowTerminalCwdSyncResult,
  shouldClearBlockedFollowOnReach,
  shouldFollowTerminalCwdNavigate,
  shouldInvalidateFollowBookkeepingOnCwdChange,
  shouldLatchInitialFollowInterruption,
  shouldReleaseInitialFollowSyncAttempt,
  shouldResetInitialFollowTerminalCwdSync,
} from "./sftpFollowTerminalCwd";

const base = {
  followEnabled: true,
  isVisible: true,
  terminalCwd: "/home/user/project",
  currentPath: "/home/user",
  connectionId: "conn-1",
  hasActiveWork: false,
  isConnected: true,
};

const readComponentSource = (relativePath: string) => (
  readFileSync(new URL(relativePath, import.meta.url), "utf8")
);

test("shouldFollowTerminalCwdNavigate returns true when follow is on and paths differ", () => {
  assert.equal(shouldFollowTerminalCwdNavigate(base), true);
});

test("shouldFollowTerminalCwdNavigate returns false when paths already match", () => {
  assert.equal(
    shouldFollowTerminalCwdNavigate({ ...base, currentPath: "/home/user/project" }),
    false,
  );
});

test("shouldFollowTerminalCwdNavigate returns false when follow is disabled", () => {
  assert.equal(shouldFollowTerminalCwdNavigate({ ...base, followEnabled: false }), false);
});

test("shouldFollowTerminalCwdNavigate returns false while interactive work is active", () => {
  assert.equal(shouldFollowTerminalCwdNavigate({ ...base, hasActiveWork: true }), false);
});

test("shouldFollowTerminalCwdNavigate returns false without a known terminal cwd", () => {
  assert.equal(shouldFollowTerminalCwdNavigate({ ...base, terminalCwd: null }), false);
});

test("shouldFollowTerminalCwdNavigate returns false when cwd is blocked after a failed follow", () => {
  assert.equal(
    shouldFollowTerminalCwdNavigate({
      ...base,
      blockedFollow: { connectionId: "conn-1", terminalCwd: "/home/user/project" },
    }),
    false,
  );
});

test("shouldFollowTerminalCwdNavigate ignores blocked cwd for a different connection", () => {
  assert.equal(
    shouldFollowTerminalCwdNavigate({
      ...base,
      connectionId: "conn-2",
      blockedFollow: { connectionId: "conn-1", terminalCwd: "/home/user/project" },
    }),
    true,
  );
});

test("shouldFollowTerminalCwdNavigate ignores blocked cwd when terminal cwd changed", () => {
  assert.equal(
    shouldFollowTerminalCwdNavigate({
      ...base,
      terminalCwd: "/home/user/other",
      blockedFollow: { connectionId: "conn-1", terminalCwd: "/home/user/project" },
    }),
    true,
  );
});

test("shouldFollowTerminalCwdNavigate does not recapture manual navigation after the cwd was handled", () => {
  assert.equal(
    shouldFollowTerminalCwdNavigate({
      ...base,
      currentPath: "/srv/bookmark",
      handledFollow: { connectionId: "conn-1", terminalCwd: "/home/user/project" },
    }),
    false,
  );
});

test("shouldFollowTerminalCwdNavigate resumes when the terminal cwd changes", () => {
  assert.equal(
    shouldFollowTerminalCwdNavigate({
      ...base,
      terminalCwd: "/home/user/other",
      currentPath: "/srv/bookmark",
      handledFollow: { connectionId: "conn-1", terminalCwd: "/home/user/project" },
    }),
    true,
  );
});

test("resolveHostFollowTerminalCwd inherits the global setting until the host overrides it", () => {
  assert.equal(resolveHostFollowTerminalCwd(undefined, true), true);
  assert.equal(resolveHostFollowTerminalCwd(undefined, false), false);
  assert.equal(resolveHostFollowTerminalCwd(true, false), true);
  assert.equal(resolveHostFollowTerminalCwd(false, true), false);
});

test("resolveSftpFollowTerminalCwdTargetHost prefers the visible SFTP host", () => {
  const terminalHost = { id: "terminal-host" };
  const visibleHost = { id: "visible-sftp-host" };

  assert.equal(
    resolveSftpFollowTerminalCwdTargetHost(visibleHost, terminalHost),
    visibleHost,
  );
  assert.equal(
    resolveSftpFollowTerminalCwdTargetHost(null, terminalHost),
    terminalHost,
  );
});

test("visible SFTP host override can enable follow when terminal host inherits global off", () => {
  const terminalHost = { id: "terminal-host", sftpFollowTerminalCwd: undefined };
  const visibleHost = { id: "visible-sftp-host", sftpFollowTerminalCwd: true };
  const followHost = resolveSftpFollowTerminalCwdTargetHost(visibleHost, terminalHost);

  assert.equal(resolveHostFollowTerminalCwd(followHost?.sftpFollowTerminalCwd, false), true);
});

test("mergeLatestFollowTerminalCwdHostSetting refreshes the follow flag without losing display overrides", () => {
  const connectedHost = {
    id: "host-1",
    hostname: "session.example.com",
    sftpFollowTerminalCwd: false,
  };
  const latestHost = {
    id: "host-1",
    hostname: "vault.example.com",
    sftpFollowTerminalCwd: true,
  };

  assert.deepEqual(
    mergeLatestFollowTerminalCwdHostSetting(connectedHost, latestHost),
    {
      id: "host-1",
      hostname: "session.example.com",
      sftpFollowTerminalCwd: true,
    },
  );
});

test("mergeLatestFollowTerminalCwdHostSetting keeps optimistic session override until vault updates", () => {
  const connectedHost = {
    id: "host-1",
    hostname: "session.example.com",
    sftpFollowTerminalCwd: false,
  };
  const latestHost = {
    id: "host-1",
    hostname: "vault.example.com",
  };

  assert.deepEqual(
    mergeLatestFollowTerminalCwdHostSetting(connectedHost, latestHost, false),
    {
      id: "host-1",
      hostname: "session.example.com",
      sftpFollowTerminalCwd: false,
    },
  );
});

test("mergeLatestFollowTerminalCwdHostSetting drops stale session override when vault clears the follow flag", () => {
  const connectedHost = {
    id: "host-1",
    hostname: "session.example.com",
    sftpFollowTerminalCwd: true,
  };
  const latestHost = {
    id: "host-1",
    hostname: "vault.example.com",
  };

  assert.deepEqual(
    mergeLatestFollowTerminalCwdHostSetting(connectedHost, latestHost),
    {
      id: "host-1",
      hostname: "session.example.com",
      sftpFollowTerminalCwd: undefined,
    },
  );
});

test("shouldClearBlockedFollowOnReach clears when the active connection reaches the blocked cwd", () => {
  assert.equal(
    shouldClearBlockedFollowOnReach(
      { connectionId: "conn-1", terminalCwd: "/home/user/project" },
      "conn-1",
      "/home/user/project",
      false,
    ),
    true,
  );
});

test("shouldClearBlockedFollowOnReach keeps block while navigation is still loading", () => {
  assert.equal(
    shouldClearBlockedFollowOnReach(
      { connectionId: "conn-1", terminalCwd: "/home/user/project" },
      "conn-1",
      "/home/user/project",
      true,
    ),
    false,
  );
});

test("shouldApplyFollowTerminalCwdSyncResult rejects stale follow results after cwd changes", () => {
  let generation = 0;
  const followA = generation;

  generation += 1;
  const followB = generation;

  assert.equal(
    shouldApplyFollowTerminalCwdSyncResult({
      syncGeneration: followB,
      currentGeneration: generation,
      followEnabled: true,
      canFollow: true,
    }),
    true,
  );
  assert.equal(
    shouldApplyFollowTerminalCwdSyncResult({
      syncGeneration: followA,
      currentGeneration: generation,
      followEnabled: true,
      canFollow: true,
    }),
    false,
  );
});

test("shouldApplyFollowTerminalCwdSyncResult rejects results after follow is unavailable", () => {
  assert.equal(
    shouldApplyFollowTerminalCwdSyncResult({
      syncGeneration: 2,
      currentGeneration: 2,
      followEnabled: false,
      canFollow: true,
    }),
    false,
  );
  assert.equal(
    shouldApplyFollowTerminalCwdSyncResult({
      syncGeneration: 2,
      currentGeneration: 2,
      followEnabled: true,
      canFollow: false,
    }),
    false,
  );
});

test("shouldApplyFollowTerminalCwdSyncResult rejects results for an old connection", () => {
  assert.equal(
    shouldApplyFollowTerminalCwdSyncResult({
      syncGeneration: 2,
      currentGeneration: 2,
      followEnabled: true,
      canFollow: true,
      expectedConnectionId: "conn-1",
      liveConnectionId: "conn-2",
      paneConnectionId: "conn-2",
    }),
    false,
  );
});

test("shouldApplyFollowTerminalCwdSyncResult rejects results for an old terminal cwd", () => {
  assert.equal(
    shouldApplyFollowTerminalCwdSyncResult({
      syncGeneration: 2,
      currentGeneration: 2,
      followEnabled: true,
      canFollow: true,
      expectedConnectionId: "conn-1",
      liveConnectionId: "conn-1",
      paneConnectionId: "conn-1",
      expectedTerminalCwd: "/srv/old",
      liveTerminalCwd: "/srv/new",
    }),
    false,
  );
});

test("shouldApplyFollowTerminalCwdSyncResult rejects missing live cwd when required", () => {
  assert.equal(
    shouldApplyFollowTerminalCwdSyncResult({
      syncGeneration: 2,
      currentGeneration: 2,
      followEnabled: true,
      canFollow: true,
      expectedConnectionId: "conn-1",
      liveConnectionId: "conn-1",
      paneConnectionId: "conn-1",
      expectedTerminalCwd: "/srv/project",
      liveTerminalCwd: null,
      requireLiveTerminalCwd: true,
    }),
    false,
  );
});

test("shouldApplyFollowTerminalCwdSyncResult allows missing live cwd when target was fetched fresh", () => {
  assert.equal(
    shouldApplyFollowTerminalCwdSyncResult({
      syncGeneration: 2,
      currentGeneration: 2,
      followEnabled: true,
      canFollow: true,
      expectedConnectionId: "conn-1",
      liveConnectionId: "conn-1",
      paneConnectionId: "conn-1",
      expectedTerminalCwd: "/srv/project",
      liveTerminalCwd: null,
    }),
    true,
  );
});

test("SftpSidePanel follow effect is not keyed by SFTP path changes", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");
  const followEffect = source.match(
    /useEffect\(\(\) => \{\n\s+if \(!effectiveFollowTerminalCwd[\s\S]*?void syncFollowToTerminalCwd\(\);\n\s+\}, \[\n(?<deps>[\s\S]*?)\n\s+\]\);/,
  );

  assert.ok(followEffect?.groups?.deps);
  assert.match(followEffect.groups.deps, /activeTerminalCwd/);
  assert.match(followEffect.groups.deps, /connectionId/);
  assert.doesNotMatch(followEffect.groups.deps, /currentPath|connectionPath/);
});

test("SftpSidePanel passes a stale-result guard into automatic follow navigation", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");

  assert.match(
    source,
    /navigateTo\("left", terminalCwd, \{\n\s+shouldApply: shouldApplyCurrentFollowSync,\n\s+\}\)/,
  );
});

test("SftpSidePanel invalidates follow state whenever the follow toggle changes", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");
  const toggleHandler = source.match(
    /const handleToggleFollowTerminalCwd = useCallback\(\(\) => \{[\s\S]*?\}, \[effectiveFollowTerminalCwd/,
  );

  assert.ok(toggleHandler);
  assert.match(toggleHandler[0], /invalidateInFlightFollowSync\(\);/);
  assert.doesNotMatch(toggleHandler[0], /if \(!nextEnabled\)/);
});

test("first-open sync navigates from stale home to a backend-confirmed cwd", async () => {
  let handled = null;
  let blocked = null;
  let navigatedTo = null;
  const connection = { id: "conn-1", currentPath: "/home/alice", status: "connected" };

  const completed = await runInitialFollowTerminalCwdSync({
    expectedConnectionId: "conn-1",
    staleTerminalCwd: "/home/alice",
    getFreshTerminalCwd: async () => "/srv/project",
    isEligible: () => true,
    getConnection: () => connection,
    navigate: async (cwd, shouldApply) => {
      assert.equal(shouldApply(), true);
      navigatedTo = cwd;
      connection.currentPath = cwd;
      return "reached";
    },
    setHandled: (value) => { handled = value; },
    setBlocked: (value) => { blocked = value; },
  });

  assert.equal(completed, true);
  assert.equal(navigatedTo, "/srv/project");
  assert.deepEqual(handled, { connectionId: "conn-1", terminalCwd: "/home/alice" });
  assert.equal(blocked, null);
});

test("first-open sync can retry a failed probe and cancels stale results", async () => {
  let attempts = 0;
  let eligible = true;
  let navigations = 0;
  const connection = { id: "conn-1", currentPath: "/home/alice", status: "connected" };
  const run = () => runInitialFollowTerminalCwdSync({
    expectedConnectionId: "conn-1",
    staleTerminalCwd: "/home/alice",
    getFreshTerminalCwd: async () => (++attempts === 1 ? null : "/srv/project"),
    isEligible: () => eligible,
    getConnection: () => connection,
    navigate: async () => { navigations += 1; return "reached"; },
    setHandled: () => {},
    setBlocked: () => {},
  });

  assert.equal(await run(), false);
  assert.equal(await run(), true);
  assert.equal(attempts, 2);
  assert.equal(navigations, 1);

  eligible = false;
  assert.equal(await run(), false);
  assert.equal(navigations, 1);
});

test("SftpSidePanel bounds first-open retries and disables cached fallback", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");

  assert.match(
    source,
    /preferFreshBackend: true,\n\s+allowRendererFallback: false/,
  );
  assert.match(source, /initialFollowRetryRef\.current\.attempts >= 3/);
  assert.match(source, /setInitialFollowRetryNonce\(\(value\) => value \+ 1\)/);
});

test("SftpSidePanel binds first-open probe eligibility to the linked terminal session", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");

  // Two connected same-endpoint sessions can report an equal cached cwd, so no
  // cwd-change invalidation fires while the source-session rebind is still
  // deferred through requestAnimationFrame. The probe must still be bound to
  // the session it was armed for, or it navigates the newly focused session's
  // pane to the previous session's cwd.
  assert.match(source, /const expectedSessionId = activeSessionId \?\? null;/);
  assert.match(
    source,
    /expectedSessionId === null \|\| activeSessionIdRef\.current === expectedSessionId/,
  );
});

test("SftpSidePanel keeps a queued first-open retry consumed when the surface is hidden", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");
  const retryTimer = source.match(
    /initialFollowRetryTimerRef\.current = setTimeout\(\(\) => \{[\s\S]*?\n\s+\}, 250\);/,
  );

  assert.ok(retryTimer);
  // While the retry delay is pending, the one-shot slot stays consumed, and a
  // retry expiring while the surface is hidden (terminal-tab switch with the
  // owner panel still open) is consumed instead of re-arming on return.
  assert.match(retryTimer[0], /shouldReleaseInitialFollowSyncAttempt/);
});

test("SftpSidePanel consumes a queued first-open retry on a hide mid-delay", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");

  // Hiding with the owner panel still open while the 250 ms retry is queued
  // must cancel it: the timer's current-state-only visibility check cannot
  // observe a hide restored before expiry, and would release the one-shot
  // slot on return, re-arming the first-open sync over the preserved pane.
  assert.match(
    source,
    /if \(isVisible \|\| !ownerPanelOpen\) return;\s*\n\s*if \(!initialFollowRetryTimerRef\.current\) return;\s*\n\s*clearTimeout\(initialFollowRetryTimerRef\.current\);\s*\n\s*initialFollowRetryTimerRef\.current = null;/,
  );
});

test("follow bookkeeping keeps handled state across hidden-panel null cwd transitions", () => {
  // Hidden panels receive activeTerminalCwd={null} and get the last live value
  // back on reshow: the visibility transitions may not drop handled follow
  // bookkeeping when the cwd did not actually change.
  assert.equal(
    shouldInvalidateFollowBookkeepingOnCwdChange({
      nextCwd: null,
      lastCwd: "/home/user/project",
      isVisible: false,
    }),
    false,
  );
  assert.equal(
    shouldInvalidateFollowBookkeepingOnCwdChange({
      nextCwd: "/home/user/project",
      lastCwd: "/home/user/project",
      isVisible: false,
    }),
    false,
  );
  assert.equal(
    shouldInvalidateFollowBookkeepingOnCwdChange({
      nextCwd: "/home/user/project",
      lastCwd: "/home/user/project",
      isVisible: true,
    }),
    false,
  );
  assert.equal(
    shouldInvalidateFollowBookkeepingOnCwdChange({
      nextCwd: null,
      lastCwd: null,
      isVisible: false,
    }),
    false,
  );
});

test("follow bookkeeping invalidates on a real terminal cwd change", () => {
  assert.equal(
    shouldInvalidateFollowBookkeepingOnCwdChange({
      nextCwd: "/home/user/other",
      lastCwd: "/home/user/project",
      isVisible: true,
    }),
    true,
  );
  // A cwd that changed while the panel was hidden still invalidates on reshow.
  assert.equal(
    shouldInvalidateFollowBookkeepingOnCwdChange({
      nextCwd: "/home/user/other",
      lastCwd: null,
      isVisible: true,
    }),
    true,
  );
  // A `null` while the surface is visible means the linked terminal session
  // changed (or closed) and its cwd cache is empty: in-flight follow results
  // of the previous session must be invalidated.
  assert.equal(
    shouldInvalidateFollowBookkeepingOnCwdChange({
      nextCwd: null,
      lastCwd: "/home/user/project",
      isVisible: true,
    }),
    true,
  );
});

test("SftpSidePanel guards cwd invalidation behind the visibility-safe helper", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");

  assert.match(
    source,
    /shouldInvalidateFollowBookkeepingOnCwdChange\(\{\s*\n\s*nextCwd: activeTerminalCwd,\s*\n\s*lastCwd: lastLiveTerminalCwdRef\.current,/,
  );
});

test("first-open sync reset re-arms on a replaced connection", () => {
  assert.equal(
    shouldResetInitialFollowTerminalCwdSync({
      isVisible: true,
      ownerPanelOpen: true,
      connectionId: "conn-2",
      trackedConnectionId: "conn-1",
    }),
    true,
  );
});

test("first-open sync reset re-arms after a fresh open on the same connection", () => {
  assert.equal(
    shouldResetInitialFollowTerminalCwdSync({
      isVisible: false,
      ownerPanelOpen: false,
      connectionId: "conn-1",
      trackedConnectionId: "conn-1",
    }),
    true,
  );
});

test("first-open sync reset survives hiding the surface while the owner panel stays open", () => {
  // Terminal tab switches / side-panel tool switches keep the panel mounted
  // and open: the user's browsed directory and filename filter must survive.
  assert.equal(
    shouldResetInitialFollowTerminalCwdSync({
      isVisible: false,
      ownerPanelOpen: true,
      connectionId: "conn-1",
      trackedConnectionId: "conn-1",
    }),
    false,
  );
  assert.equal(
    shouldResetInitialFollowTerminalCwdSync({
      isVisible: true,
      ownerPanelOpen: true,
      connectionId: "conn-1",
      trackedConnectionId: "conn-1",
    }),
    false,
  );
});

test("SftpSidePanel feeds ownerPanelOpen into the first-open reset guard", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");

  assert.match(
    source,
    /shouldResetInitialFollowTerminalCwdSync\(\{\s*\n\s*isVisible,\s*\n\s*ownerPanelOpen,/,
  );
  assert.match(source, /\[connectionId, isVisible, ownerPanelOpen\]/);
});

test("first-open sync attempt is released while visible or after the owner panel closed", () => {
  assert.equal(shouldReleaseInitialFollowSyncAttempt({ isVisible: true, ownerPanelOpen: true }), true);
  assert.equal(shouldReleaseInitialFollowSyncAttempt({ isVisible: true, ownerPanelOpen: false }), true);
  assert.equal(shouldReleaseInitialFollowSyncAttempt({ isVisible: false, ownerPanelOpen: false }), true);
});

test("first-open sync attempt stays consumed while hidden with the owner panel open", () => {
  // Terminal tab switch while the fresh-CWD probe is still pending: the
  // interrupted attempt must not re-arm, or returning to the tab re-runs the
  // first-open sync and navigates away from the browsed directory.
  assert.equal(shouldReleaseInitialFollowSyncAttempt({ isVisible: false, ownerPanelOpen: true }), false);
});

test("SftpSidePanel keeps interrupted initial sync consumed across tab switches", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");

  assert.match(
    source,
    /!shouldReleaseInitialFollowSyncAttempt\(\{\s*\n\s*isVisible: isVisibleRef\.current,\s*\n\s*ownerPanelOpen: ownerPanelOpenRef\.current,/,
  );
  assert.match(source, /const ownerPanelOpenRef = useRef\(ownerPanelOpen\);/);
  assert.match(source, /ownerPanelOpenRef\.current = ownerPanelOpen;/);
});

test("first-open probe is latched as interrupted on a hide with the owner panel open", () => {
  // Terminal-tab switch while the fresh-CWD probe is pending: the probe must be
  // latched as interrupted so it cannot navigate once the tab is reshown.
  assert.equal(
    shouldLatchInitialFollowInterruption({ isVisible: false, ownerPanelOpen: true }),
    true,
  );
  assert.equal(
    shouldLatchInitialFollowInterruption({ isVisible: true, ownerPanelOpen: true }),
    false,
  );
  // A panel close is not an interruption latch: the reset guard re-arms there.
  assert.equal(
    shouldLatchInitialFollowInterruption({ isVisible: false, ownerPanelOpen: false }),
    false,
  );
});

test("SftpSidePanel latch blocks the restored-tab stale probe and keeps the slot consumed", () => {
  const source = readComponentSource("../SftpSidePanel.tsx");

  assert.match(source, /const initialFollowInterruptedRef = useRef\(false\);/);
  // The latch only arms while a first-open probe is actually in flight:
  // hiding during `connecting` (before any probe started) must not consume
  // the one-shot resync permanently. The pending marker stores the identity
  // of the in-flight attempt, not a shared boolean: a superseded probe
  // resolving must not clear the marker while a newer probe is still
  // running, or the latch would be skipped for that newer probe.
  assert.match(source, /const initialFollowProbeSeqRef = useRef\(0\);/);
  assert.match(source, /const initialFollowProbeAttemptRef = useRef<number \| null>\(null\);/);
  assert.match(
    source,
    /if \(initialFollowProbeAttemptRef\.current === null\) return;\s*\n\s*if \(!shouldLatchInitialFollowInterruption\(\{ isVisible, ownerPanelOpen \}\)\) return;\s*\n\s*initialFollowInterruptedRef\.current = true;/,
  );
  assert.match(
    source,
    /initialFollowProbeAttemptRef\.current = probeAttempt;\s*\n\s*void runInitialFollowTerminalCwdSync\(/,
  );
  assert.match(
    source,
    /const isCurrentAttempt = initialFollowProbeAttemptRef\.current === probeAttempt;\s*\n\s*if \(isCurrentAttempt\) initialFollowProbeAttemptRef\.current = null;\s*\n\s*if \(!completed && isCurrentAttempt\) clearAttemptAndRetry\(\);/,
  );
  // The latch resets only when the first-open sync re-arms.
  assert.match(
    source,
    /initialFollowSyncedConnRef\.current = null;\s*\n\s*initialFollowInterruptedRef\.current = false;/,
  );
  // The connection reset must also drop a stale pending-probe marker: while
  // connection A's probe is still outstanding and the pane starts connecting
  // to B, hiding the tab must not treat A's marker as a current pending probe
  // and latch the interruption (which would keep B's fresh first-open sync
  // ineligible until the owner panel closes).
  assert.match(
    source,
    /followSyncGenerationRef\.current \+= 1;\s*\n(\s*\/\/[^\n]*\n)*\s*initialFollowProbeAttemptRef\.current = null;/,
  );
  // Eligibility and retry must both consult the latch.
  assert.match(source, /&& !initialFollowInterruptedRef\.current/);
  assert.match(source, /if \(initialFollowInterruptedRef\.current\) return;/);
});
