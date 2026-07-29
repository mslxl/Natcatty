# Clone-tab, Split-inherits-cwd, Local-terminal Quick Icon — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make tab-clone and pane-split inherit the source terminal's current working directory, and add a one-click "new local terminal" icon to the top tab bar.

**Architecture:** A pure domain helper resolves a `cd`-injection intent for remote clones; the clone factory applies an `inheritedCwd` (local → `localStartDir`, remote → a new transient `pendingInitialCwd` field consumed by the terminal's existing restore-cwd injection path). App-level handlers capture the source cwd (from `lastCwd`, else an SSH `/proc` probe) before cloning. Feature 3 is pure UI wiring reusing the existing `createLocalTerminalWithCurrentShell`.

**Tech Stack:** TypeScript/React (renderer, ESM), node:test + tsx for tests, lucide-react icons, i18n via `application/i18n/locales/*`.

---

## File structure

- `domain/models/terminal.ts` — add transient `pendingInitialCwd?: string` field (modify)
- `domain/sessionRestore.ts` — extract shared cwd eligibility + add `resolveInheritedCwdIntent` (modify)
- `domain/sessionRestore.test.ts` — tests for new helper (modify/create alongside existing)
- `application/state/terminalConnectionReuse.ts` — `inheritedCwd` clone option (modify)
- `application/state/terminalConnectionReuse.test.ts` — clone-cwd tests (create if absent)
- `application/state/inheritedCwd.ts` — `captureInheritedCwd` async util (create)
- `application/state/inheritedCwd.test.ts` — capture tests (create)
- `application/state/useSessionState.ts` — thread `inheritedCwd` through `copySession`/`splitSession` (modify)
- `application/app/AppHandlers.ts` — make copy/split impls async + capture cwd (modify)
- `App.tsx` — pass `sessions`/`netcattyBridge` to copy/split ctx; expose `createLocalTerminalWithCurrentShell` to AppView (modify)
- `components/Terminal.tsx` — `pendingInitialCwd` prop + `prepareInitialCwdIntent` (modify)
- `components/terminal/useTerminalEffects.ts` — call `prepareInitialCwdIntent` on fresh connect (modify)
- `components/terminal/runtime/createTerminalSessionStarters.types.ts` — add `prepareInitialCwdIntent?` to ctx type (modify)
- `components/terminalLayer/TerminalLayerSupport.tsx` — pass `pendingInitialCwd={session.pendingInitialCwd}` (modify)
- `components/TopTabs.tsx` — local-terminal icon + `onCreateLocalTerminal` prop + memo (modify)
- `application/app/AppView.tsx` — pass `onCreateLocalTerminal` to TopTabs (modify)
- `application/i18n/locales/{en,zh-CN,zh-TW}/core.ts` — `topTabs.newLocalTerminal` key (modify)

Test runner: `node --test --import tsx <file>`.

---

## Task 1: Add transient `pendingInitialCwd` field to the session model

**Files:**
- Modify: `domain/models/terminal.ts` (near `lastCwd`, line ~571)

- [ ] **Step 1: Add the field**

In `interface TerminalSession`, immediately after the `lastCwd?: string;` line, add:

```ts
  /**
   * Transient one-shot: a directory a freshly-cloned/split REMOTE session
   * should `cd` into on its first connect. Set by the clone factory when a
   * copy/split inherits the source pane's cwd; consumed once by the terminal's
   * restore-cwd injection path. Not persisted across relaunch. Local clones use
   * `localStartDir` instead of this field.
   */
  pendingInitialCwd?: string;
```

- [ ] **Step 2: Typecheck**

Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | head -20`
Expected: no new errors referencing `terminal.ts`.

- [ ] **Step 3: Commit**

```bash
git add domain/models/terminal.ts
git commit -m "feat(terminal): add transient pendingInitialCwd session field"
```

---

## Task 2: Domain helper `resolveInheritedCwdIntent`

Reuse the existing cwd eligibility logic (`shouldAttemptRestoreCwd`) but decouple it from `restoreState`/`enabled` so a fresh clone can inject `cd`.

**Files:**
- Modify: `domain/sessionRestore.ts` (near `shouldAttemptRestoreCwd`, lines 214-266)
- Modify/create: `domain/sessionRestore.test.ts`

- [ ] **Step 1: Write the failing test**

Add to `domain/sessionRestore.test.ts` (create the file with these imports if it does not exist):

```ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { resolveInheritedCwdIntent } from "./sessionRestore";

test("resolveInheritedCwdIntent: ssh clone gets a cd command", () => {
  const intent = resolveInheritedCwdIntent({
    session: { protocol: "ssh", shellType: "posix", cwd: "/var/log" },
    isNetworkDevice: false,
  });
  assert.deepEqual(intent, { cwd: "/var/log", command: "cd -- '/var/log'" });
});

test("resolveInheritedCwdIntent: fires without restoreState or enabled flag", () => {
  // Unlike resolveRestoreCwdIntent, no `enabled` / restored-disconnected gate.
  const intent = resolveInheritedCwdIntent({
    session: { protocol: "ssh", cwd: "/home/me/proj" },
    isNetworkDevice: false,
  });
  assert.equal(intent?.command, "cd -- '/home/me/proj'");
});

test("resolveInheritedCwdIntent: skips mosh/et and network devices and bad paths", () => {
  assert.equal(resolveInheritedCwdIntent({ session: { protocol: "ssh", etEnabled: true, cwd: "/x" }, isNetworkDevice: false }), null);
  assert.equal(resolveInheritedCwdIntent({ session: { protocol: "ssh", cwd: "/x" }, isNetworkDevice: true }), null);
  assert.equal(resolveInheritedCwdIntent({ session: { protocol: "ssh", cwd: "C:\\Users" }, isNetworkDevice: false }), null);
  assert.equal(resolveInheritedCwdIntent({ session: { protocol: "ssh", cwd: "   " }, isNetworkDevice: false }), null);
});

test("resolveInheritedCwdIntent: skips windows local shells", () => {
  assert.equal(resolveInheritedCwdIntent({ session: { protocol: "local", shellType: "powershell", cwd: "/x" }, isNetworkDevice: false }), null);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node --test --import tsx domain/sessionRestore.test.ts`
Expected: FAIL — `resolveInheritedCwdIntent is not a function` / undefined export.

- [ ] **Step 3: Implement**

In `domain/sessionRestore.ts`, refactor the eligibility check out of `shouldAttemptRestoreCwd` and add the new export. Replace the body of `shouldAttemptRestoreCwd` (lines 222-240) and add the new function after `resolveRestoreCwdIntent` (after line 266):

```ts
export function isCwdInjectionEligible({
  session,
  isNetworkDevice,
}: {
  session: RestoreCwdSession & { cwd?: string };
  isNetworkDevice: boolean;
}): boolean {
  if (isNetworkDevice) return false;
  if (!isRestoreCwdPathEligible(session.cwd)) return false;
  if (session.moshEnabled || session.etEnabled) return false;
  const protocol = session.protocol ?? "ssh";
  if (protocol === "local" && (session.shellType === "powershell" || session.shellType === "cmd")) {
    return false;
  }
  return protocol === "ssh" || protocol === "local" || protocol === undefined;
}

export function resolveInheritedCwdIntent(options: {
  session: Pick<TerminalSession, "protocol" | "shellType" | "moshEnabled" | "etEnabled"> & { cwd?: string };
  isNetworkDevice: boolean;
}): { cwd: string; command: string } | null {
  if (!isCwdInjectionEligible({
    session: {
      status: "connecting",
      protocol: options.session.protocol,
      shellType: options.session.shellType,
      moshEnabled: options.session.moshEnabled,
      etEnabled: options.session.etEnabled,
      cwd: options.session.cwd,
    },
    isNetworkDevice: options.isNetworkDevice,
  })) {
    return null;
  }
  const cwd = options.session.cwd!.trim();
  return { cwd, command: `cd -- ${quoteRestoreCwdArgument(cwd)}` };
}
```

Then rewrite `shouldAttemptRestoreCwd` to reuse the extracted predicate (keeping its extra `restoreState` gate):

```ts
export function shouldAttemptRestoreCwd({
  enabled,
  session,
  isNetworkDevice,
}: {
  enabled: boolean;
  session: RestoreCwdSession;
  isNetworkDevice: boolean;
}): boolean {
  if (!enabled) return false;
  if (!isRestoredDisconnectedSession(session)) return false;
  return isCwdInjectionEligible({ session: { ...session, cwd: session.lastCwd }, isNetworkDevice });
}
```

Note: `RestoreCwdSession` already includes `status`; `isCwdInjectionEligible` ignores `status`. The `cwd` on `RestoreCwdSession` maps from `lastCwd`.

- [ ] **Step 4: Run tests to verify pass (new + existing)**

Run: `node --test --import tsx domain/sessionRestore.test.ts`
Expected: PASS. Also run any pre-existing restore tests to confirm no regression:
Run: `node --test --import tsx domain/sessionRestore.test.ts 2>&1 | tail -5`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add domain/sessionRestore.ts domain/sessionRestore.test.ts
git commit -m "feat(terminal): add resolveInheritedCwdIntent for clone/split cwd inheritance"
```

---

## Task 3: Clone factory applies `inheritedCwd`

**Files:**
- Modify: `application/state/terminalConnectionReuse.ts`
- Create: `application/state/terminalConnectionReuse.test.ts`

- [ ] **Step 1: Write the failing test**

Create `application/state/terminalConnectionReuse.test.ts`:

```ts
import { test } from "node:test";
import assert from "node:assert/strict";
import type { TerminalSession } from "../../domain/models";
import { createSplitTerminalSessionClone, createCopiedTerminalSessionClone } from "./terminalConnectionReuse";

const base = (over: Partial<TerminalSession>): TerminalSession => ({
  id: "src", hostId: "h", hostLabel: "H", hostname: "h", username: "u",
  status: "connected", protocol: "ssh", ...over,
});

test("split clone: remote inheritedCwd -> pendingInitialCwd, localStartDir untouched", () => {
  const clone = createSplitTerminalSessionClone(base({ protocol: "ssh" }), { id: "new", inheritedCwd: "/var/log" });
  assert.equal(clone.pendingInitialCwd, "/var/log");
  assert.equal(clone.localStartDir, undefined);
});

test("split clone: local inheritedCwd -> localStartDir, no pendingInitialCwd", () => {
  const clone = createSplitTerminalSessionClone(
    base({ protocol: "local", localStartDir: "/home/u", status: "connecting" }),
    { id: "new", localShellType: "posix", inheritedCwd: "/tmp/work" },
  );
  assert.equal(clone.localStartDir, "/tmp/work");
  assert.equal(clone.pendingInitialCwd, undefined);
});

test("copy clone: no inheritedCwd -> no pendingInitialCwd, keeps localStartDir", () => {
  const clone = createCopiedTerminalSessionClone(base({ protocol: "local", localStartDir: "/home/u", status: "connecting" }), { id: "new", localShellType: "posix" });
  assert.equal(clone.localStartDir, "/home/u");
  assert.equal(clone.pendingInitialCwd, undefined);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node --test --import tsx application/state/terminalConnectionReuse.test.ts`
Expected: FAIL — `inheritedCwd` not applied (`pendingInitialCwd`/`localStartDir` undefined/wrong).

- [ ] **Step 3: Implement**

In `application/state/terminalConnectionReuse.ts`, extend `CloneSessionOptions` (line 12) and `createTerminalSessionClone` (lines 25-61):

```ts
type CloneSessionOptions = {
  id: string;
  localShellType?: TerminalSession["shellType"];
  workspaceId?: string;
  inheritedCwd?: string;
};
```

Inside `createTerminalSessionClone`, compute the cwd application. Replace the `localStartDir: session.localStartDir,` line (49) and the closing of the object literal / return with:

```ts
  const isLocal = session.protocol === "local";
  const clonedSession: TerminalSession = {
    id: options.id,
    hostId: session.hostId,
    hostLabel: session.hostLabel,
    hostname: session.hostname,
    username: session.username,
    status: "connecting",
    protocol: session.protocol,
    pluginConnection: session.pluginConnection == null
      ? undefined
      : structuredClone(session.pluginConnection),
    port: session.port,
    moshEnabled: session.moshEnabled,
    etEnabled: session.etEnabled,
    shellType: getClonedShellType(session, options.localShellType),
    charset: session.charset,
    localShell: session.localShell,
    localShellArgs: session.localShellArgs,
    localShellName: session.localShellName,
    localShellIcon: session.localShellIcon,
    localStartDir: isLocal && options.inheritedCwd ? options.inheritedCwd : session.localStartDir,
    fontSize: session.fontSize,
    fontSizeOverride: session.fontSizeOverride,
    ...(session.ephemeralHost ? { ephemeralHost: true } : {}),
    ...(!isLocal && options.inheritedCwd ? { pendingInitialCwd: options.inheritedCwd } : {}),
    reuseConnectionFromSessionId: canReuseTerminalConnection(session) ? session.id : undefined,
  };

  if (options.workspaceId) {
    clonedSession.workspaceId = options.workspaceId;
  }

  return clonedSession;
```

- [ ] **Step 4: Run test to verify it passes**

Run: `node --test --import tsx application/state/terminalConnectionReuse.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add application/state/terminalConnectionReuse.ts application/state/terminalConnectionReuse.test.ts
git commit -m "feat(terminal): clone factory applies inheritedCwd (local startDir / remote cd intent)"
```

---

## Task 4: Capture util `captureInheritedCwd`

**Files:**
- Create: `application/state/inheritedCwd.ts`
- Create: `application/state/inheritedCwd.test.ts`

- [ ] **Step 1: Write the failing test**

Create `application/state/inheritedCwd.test.ts`:

```ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { captureInheritedCwd } from "./inheritedCwd";

const neverProbe = async () => { throw new Error("should not probe"); };

test("prefers lastCwd when present", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected", lastCwd: "/a" },
    neverProbe,
  );
  assert.equal(cwd, "/a");
});

test("ssh connected with empty lastCwd probes getSessionPwd", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected" },
    async () => ({ success: true, cwd: "/probed" }),
  );
  assert.equal(cwd, "/probed");
});

test("ssh probe failure -> undefined", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected" },
    async () => ({ success: false }),
  );
  assert.equal(cwd, undefined);
});

test("local with empty lastCwd falls back to localStartDir (no probe)", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "local", status: "connected", localStartDir: "/home/u" },
    neverProbe,
  );
  assert.equal(cwd, "/home/u");
});

test("disconnected ssh does not probe", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "disconnected" },
    neverProbe,
  );
  assert.equal(cwd, undefined);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node --test --import tsx application/state/inheritedCwd.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement**

Create `application/state/inheritedCwd.ts`:

```ts
import type { TerminalSession } from "../../domain/models";

export type SessionPwdProbe = (
  sessionId: string,
  options?: { allowHomeFallback?: boolean },
) => Promise<{ success: boolean; cwd?: string }>;

type CaptureSession = Pick<TerminalSession, "id" | "protocol" | "status" | "lastCwd" | "localStartDir">;

/**
 * Resolve the working directory a clone/split should inherit from its source.
 * Priority: tracked lastCwd -> live SSH /proc probe -> local startDir. Returns
 * undefined when nothing is known (caller then behaves as before: login dir).
 */
export async function captureInheritedCwd(
  session: CaptureSession,
  getSessionPwd: SessionPwdProbe,
): Promise<string | undefined> {
  const tracked = session.lastCwd?.trim();
  if (tracked) return tracked;

  const protocol = session.protocol ?? "ssh";
  const isRemoteSsh = protocol === "ssh" || protocol === undefined;
  if (isRemoteSsh && session.status === "connected") {
    try {
      const res = await getSessionPwd(session.id, { allowHomeFallback: false });
      const probed = res?.success ? res.cwd?.trim() : undefined;
      if (probed) return probed;
    } catch {
      /* probe failed — fall through to undefined */
    }
  }

  if (protocol === "local") return session.localStartDir;
  return undefined;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `node --test --import tsx application/state/inheritedCwd.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add application/state/inheritedCwd.ts application/state/inheritedCwd.test.ts
git commit -m "feat(terminal): add captureInheritedCwd (lastCwd -> ssh probe -> localStartDir)"
```

---

## Task 5: Thread `inheritedCwd` through `copySession` / `splitSession`

**Files:**
- Modify: `application/state/useSessionState.ts` (`copySession` ~922, `splitSession` ~724)

These are React hooks; verify by typecheck + downstream tests rather than a new unit test (state hooks are covered indirectly).

- [ ] **Step 1: Extend `copySession` options**

At `useSessionState.ts:922`, change the options type and the clone call:

```ts
  const copySession = useCallback((sessionId: string, options?: {
    localShellType?: TerminalSession['shellType'];
    inheritedCwd?: string;
  }) => {
```

and where it calls `createCopiedTerminalSessionClone(session, { id: newSessionId, localShellType: options?.localShellType })` (line ~935), add `inheritedCwd: options?.inheritedCwd`:

```ts
      const newSession = createCopiedTerminalSessionClone(session, {
        id: newSessionId,
        localShellType: options?.localShellType,
        inheritedCwd: options?.inheritedCwd,
      });
```

- [ ] **Step 2: Extend `splitSession` options**

At `useSessionState.ts:724`, add `inheritedCwd?: string` to its `options?` type, and pass it into both `createSplitTerminalSessionClone(...)` calls (the workspace-existing path ~line 745 and the new-workspace path ~line 766):

```ts
      // both clone calls gain:
      inheritedCwd: options?.inheritedCwd,
```

(Locate each `createSplitTerminalSessionClone(session, { ... })` and add the field to its options object.)

- [ ] **Step 3: Typecheck**

Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep -i useSessionState | head`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add application/state/useSessionState.ts
git commit -m "feat(terminal): thread inheritedCwd through copySession/splitSession"
```

---

## Task 6: Capture cwd in App-level copy/split handlers (async)

**Files:**
- Modify: `application/app/AppHandlers.ts` (`splitSessionWithCurrentShellImpl` 463, `copySessionWithCurrentShellImpl` 473)
- Modify: `App.tsx` (ctx at lines 888, 890)
- Modify: `application/app/AppHandlers.test.ts` (or create) for the capture wiring

- [ ] **Step 1: Write the failing test**

Add to `application/app/AppHandlers.test.ts` (create with imports if absent):

```ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { copySessionWithCurrentShellImpl, splitSessionWithCurrentShellImpl } from "./AppHandlers";

function ctxFactory(overrides: Record<string, any>) {
  const calls: any = {};
  const base = {
    classifyLocalShellType: () => "posix",
    discoveredShells: [],
    resolveShellSetting: () => ({ command: "/bin/bash", args: [] }),
    terminalSettings: { localShell: "bash" },
    sessions: [{ id: "src", protocol: "ssh", status: "connected", lastCwd: "/var/log" }],
    netcattyBridge: { get: () => ({ getSessionPwd: async () => ({ success: false }) }) },
    copySession: (id: string, opts: any) => { calls.copy = { id, opts }; },
    splitSession: (id: string, dir: any, opts: any) => { calls.split = { id, dir, opts }; },
    ...overrides,
  };
  return { getCtx: () => base, calls };
}

test("copySessionWithCurrentShell passes inheritedCwd from lastCwd", async () => {
  const { getCtx, calls } = ctxFactory({});
  await copySessionWithCurrentShellImpl(getCtx, "src");
  assert.equal(calls.copy.opts.inheritedCwd, "/var/log");
});

test("splitSessionWithCurrentShell passes inheritedCwd from lastCwd", async () => {
  const { getCtx, calls } = ctxFactory({});
  await splitSessionWithCurrentShellImpl(getCtx, "src", "horizontal");
  assert.equal(calls.split.opts.inheritedCwd, "/var/log");
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node --test --import tsx application/app/AppHandlers.test.ts`
Expected: FAIL — `inheritedCwd` undefined (handlers don't capture yet).

- [ ] **Step 3: Implement handlers**

In `application/app/AppHandlers.ts`, add an import at the top:

```ts
import { captureInheritedCwd } from "../state/inheritedCwd";
```

Add a small shared helper above the two impls:

```ts
async function captureCtxInheritedCwd(getCtx: AppContextGetter, sessionId: string): Promise<string | undefined> {
  const { sessions, netcattyBridge } = getCtx();
  const source = sessions?.find((s: { id: string }) => s.id === sessionId);
  if (!source) return undefined;
  const bridge = netcattyBridge?.get?.();
  const probe = async (id: string, options?: { allowHomeFallback?: boolean }) =>
    (await bridge?.getSessionPwd?.(id, options)) ?? { success: false };
  return captureInheritedCwd(source, probe);
}
```

Rewrite `splitSessionWithCurrentShellImpl` (463) and `copySessionWithCurrentShellImpl` (473) as async:

```ts
export async function splitSessionWithCurrentShellImpl(getCtx: AppContextGetter, sessionId: string, direction: 'horizontal' | 'vertical') {
  const { classifyLocalShellType, discoveredShells, resolveShellSetting, splitSession, terminalSettings } = getCtx();
  const resolved = resolveShellSetting(terminalSettings.localShell, discoveredShells);
  const inheritedCwd = await captureCtxInheritedCwd(getCtx, sessionId);
  return splitSession(sessionId, direction, {
    localShellType: classifyLocalShellType(resolved?.command || terminalSettings.localShell, navigator.userAgent),
    inheritedCwd,
  });
}

export async function copySessionWithCurrentShellImpl(getCtx: AppContextGetter, sessionId: string) {
  const { classifyLocalShellType, copySession, discoveredShells, resolveShellSetting, terminalSettings } = getCtx();
  const resolved = resolveShellSetting(terminalSettings.localShell, discoveredShells);
  const inheritedCwd = await captureCtxInheritedCwd(getCtx, sessionId);
  return copySession(sessionId, {
    localShellType: classifyLocalShellType(resolved?.command || terminalSettings.localShell, navigator.userAgent),
    inheritedCwd,
  });
}
```

- [ ] **Step 4: Wire ctx in App.tsx**

At `App.tsx:888` add `netcattyBridge, sessions` to the split ctx object and at `App.tsx:890` add them to the copy ctx object. Also add `sessions` to each `useCallback` dependency array:

```ts
  const splitSessionWithCurrentShell = useCallback((sessionId: string, direction: 'horizontal' | 'vertical') => { return splitSessionWithCurrentShellImpl(() => ({ classifyLocalShellType, direction, discoveredShells, netcattyBridge, resolveShellSetting, sessionId, sessions, splitSession, terminalSettings }), sessionId, direction); }, [splitSession, terminalSettings, discoveredShells, sessions]);

  const copySessionWithCurrentShell = useCallback((sessionId: string) => { return copySessionWithCurrentShellImpl(() => ({ classifyLocalShellType, copySession, discoveredShells, netcattyBridge, resolveShellSetting, sessionId, sessions, terminalSettings }), sessionId); }, [copySession, terminalSettings, discoveredShells, sessions]);
```

(Both callbacks now return a Promise; the callers — context-menu handlers — ignore the return value, so no caller change is needed. `netcattyBridge` is already in scope, used at line 892.)

- [ ] **Step 5: Run test + typecheck**

Run: `node --test --import tsx application/app/AppHandlers.test.ts`
Expected: PASS.
Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep -iE "AppHandlers|App.tsx" | head`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add application/app/AppHandlers.ts application/app/AppHandlers.test.ts App.tsx
git commit -m "feat(terminal): capture source cwd when cloning/splitting a session"
```

---

## Task 7: Terminal injects `cd` for a remote clone on first connect

**Files:**
- Modify: `components/Terminal.tsx` (props ~259, callbacks near `prepareRestoredReconnect` ~1285, effects ctx ~3644)
- Modify: `components/terminal/useTerminalEffects.ts` (fresh-connect branch ~512-516, ctx destructure ~174)
- Modify: `components/terminal/runtime/createTerminalSessionStarters.types.ts` (ctx type ~193)
- Modify: `components/terminalLayer/TerminalLayerSupport.tsx` (pass prop ~1340)

This is React/Electron wiring; verify by lint + build + manual run (see Task 10). No new unit test — the pure logic it depends on (`resolveInheritedCwdIntent`) is already tested in Task 2, and the injection sink (`consumeRestoreCwdIntent`) has existing coverage.

- [ ] **Step 1: Add the prop to Terminal**

In `components/Terminal.tsx`, add `pendingInitialCwd` to the destructured props alongside `restoreState` (line 259):

```ts
  restoreState,
  pendingInitialCwd,
```

Add it to the props type/interface for the component (find the `restoreState?:` entry in the props type and add `pendingInitialCwd?: string;` next to it).

- [ ] **Step 2: Add `prepareInitialCwdIntent`**

In `components/Terminal.tsx`, near `prepareRestoredReconnect` (after line 1316), add:

```ts
  const initialCwdConsumedRef = useRef(false);
  const prepareInitialCwdIntent = useCallback(() => {
    if (initialCwdConsumedRef.current) return;
    if (!pendingInitialCwd) return;
    const intent = resolveInheritedCwdIntent({
      session: {
        protocol: host.protocol,
        shellType,
        moshEnabled: host.moshEnabled,
        etEnabled: host.etEnabled,
        cwd: pendingInitialCwd,
      },
      isNetworkDevice,
    });
    if (!intent) return;
    restoreCwdIntentRef.current = intent;
    initialCwdConsumedRef.current = true;
  }, [pendingInitialCwd, host.protocol, host.moshEnabled, host.etEnabled, shellType, isNetworkDevice]);
```

Add the import at the top of `Terminal.tsx` (it already imports from `../domain/sessionRestore` at line 36 — extend it):

```ts
import { resolveRestoreCwdIntent, resolveInheritedCwdIntent } from "../domain/sessionRestore";
```

- [ ] **Step 3: Pass `prepareInitialCwdIntent` into the effects ctx**

In `components/Terminal.tsx`, the `useTerminalEffects({ ... })` call (line 3644) already passes `prepareRestoredReconnect`. Add `prepareInitialCwdIntent` to that object.

- [ ] **Step 4: Type the effects ctx field**

In `components/terminal/runtime/createTerminalSessionStarters.types.ts`, near the ctx type that includes `prepareRestoredReconnect` (or add to the effects ctx type used by `useTerminalEffects`), add:

```ts
  prepareInitialCwdIntent?: () => void;
```

If `prepareRestoredReconnect` is typed in `useTerminalEffects.ts`'s own ctx type rather than this file, add it there instead — grep for `prepareRestoredReconnect?:` to find the exact type location:
Run: `grep -rn "prepareRestoredReconnect?:" components/`

- [ ] **Step 5: Call it on the fresh-connect branch**

In `components/terminal/useTerminalEffects.ts`, add `prepareInitialCwdIntent` to the big ctx destructure (line 174, alongside `prepareRestoredReconnect`), then change the branch at lines 512-516:

```ts
        const restoredReconnect = restoreState === "restored-disconnected";
        if (restoredReconnect) {
          prepareRestoredReconnect?.();
        } else {
          prepareInitialCwdIntent?.();
        }
```

- [ ] **Step 6: Pass the session field down**

In `components/terminalLayer/TerminalLayerSupport.tsx`, at the `<Terminal ... restoreState={session.restoreState}` render (line ~1340), add:

```tsx
        pendingInitialCwd={session.pendingInitialCwd}
```

- [ ] **Step 7: Typecheck + lint**

Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep -iE "Terminal.tsx|useTerminalEffects|TerminalLayerSupport|createTerminalSessionStarters" | head`
Expected: no errors.
Run: `npm run lint 2>&1 | tail -15`
Expected: no new errors in touched files.

- [ ] **Step 8: Commit**

```bash
git add components/Terminal.tsx components/terminal/useTerminalEffects.ts components/terminal/runtime/createTerminalSessionStarters.types.ts components/terminalLayer/TerminalLayerSupport.tsx
git commit -m "feat(terminal): inject cd into remote clone/split pane on first connect"
```

---

## Task 8: Local-terminal quick icon in the top tab bar

**Files:**
- Modify: `components/TopTabs.tsx` (imports 1, props type ~139, destructure ~184, render ~1095, memo ~1166)
- Modify: `application/app/AppView.tsx` (TopTabs render ~241-260, ctx destructure ~102)
- Modify: `App.tsx` (AppView ctx ~1581)

- [ ] **Step 1: Add the icon import**

In `components/TopTabs.tsx` line 1, add `SquareTerminal`:

```ts
import { Folder, FolderLock, Menu, MoreHorizontal, Plus, Settings, Sparkles, SquareTerminal } from 'lucide-react';
```

- [ ] **Step 2: Add the prop**

Add to the props type near `onOpenQuickSwitcher: () => void;` (line 139):

```ts
  onCreateLocalTerminal: () => void;
```

Add to the destructured props near `onOpenQuickSwitcher,` (line 184):

```ts
  onCreateLocalTerminal,
```

- [ ] **Step 3: Render the button left of the transfer center**

In `components/TopTabs.tsx`, in the fixed-right-controls cluster (line 1091), insert immediately **before** `<GlobalSftpTransferCenter />` (line 1095):

```tsx
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                data-section="top-tabs-new-local-terminal"
                className="h-7 w-7 shrink-0 app-no-drag top-tab-utility-btn"
                style={{ color: 'var(--top-tabs-muted, hsl(var(--muted-foreground)))' }}
                onClick={onCreateLocalTerminal}
              >
                <SquareTerminal size={16} />
              </Button>
            </TooltipTrigger>
            <TooltipContent>{t('topTabs.newLocalTerminal')}</TooltipContent>
          </Tooltip>
          <GlobalSftpTransferCenter />
```

- [ ] **Step 4: Add to memo comparison**

In `topTabsAreEqual` (line ~1166), add a line next to `prev.onCopySession === next.onCopySession &&`:

```ts
    prev.onCreateLocalTerminal === next.onCreateLocalTerminal &&
```

- [ ] **Step 5: Pass the prop from AppView**

In `application/app/AppView.tsx`, add `createLocalTerminalWithCurrentShell` to the ctx destructure near line 102 (where `copySessionWithCurrentShell` is destructured), then add the prop to `<TopTabs>` near line 260 (next to `onOpenQuickSwitcher={handleOpenQuickSwitcher}`):

```tsx
        onCreateLocalTerminal={createLocalTerminalWithCurrentShell}
```

- [ ] **Step 6: Expose the handler from App to AppView**

In `App.tsx`, the `<AppView ctx={{ ... }} />` object (line 1581) currently lists `splitSessionWithCurrentShell` and `copySessionWithCurrentShell` but not `createLocalTerminalWithCurrentShell`. Add `createLocalTerminalWithCurrentShell,` to that ctx object.

- [ ] **Step 7: Typecheck + lint**

Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep -iE "TopTabs|AppView|App.tsx" | head`
Expected: no errors.
Run: `npm run lint 2>&1 | tail -15`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add components/TopTabs.tsx application/app/AppView.tsx App.tsx
git commit -m "feat(tabs): add one-click new-local-terminal icon to the top tab bar"
```

---

## Task 9: i18n key `topTabs.newLocalTerminal`

**Files:**
- Modify: `application/i18n/locales/en/core.ts`
- Modify: `application/i18n/locales/zh-CN/core.ts`
- Modify: `application/i18n/locales/zh-TW/core.ts`

- [ ] **Step 1: Find the existing `topTabs.*` block**

Run: `grep -rn "topTabs.openQuickSwitcher\|topTabs.aiAssistant" application/i18n/locales/en/core.ts`
Expected: a line to anchor next to.

- [ ] **Step 2: Add the key in all three locales**

en/core.ts (next to `topTabs.aiAssistant`):

```ts
  'topTabs.newLocalTerminal': 'New Local Terminal',
```

zh-CN/core.ts:

```ts
  'topTabs.newLocalTerminal': '新建本地终端',
```

zh-TW/core.ts:

```ts
  'topTabs.newLocalTerminal': '新增本機終端',
```

- [ ] **Step 3: Verify key presence + typecheck**

Run: `grep -rn "topTabs.newLocalTerminal" application/i18n/locales/`
Expected: three hits (en, zh-CN, zh-TW).
Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep -i core.ts | head`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add application/i18n/locales/en/core.ts application/i18n/locales/zh-CN/core.ts application/i18n/locales/zh-TW/core.ts
git commit -m "i18n: add topTabs.newLocalTerminal in en/zh-CN/zh-TW"
```

---

## Task 10: Full verification + manual run

**Files:** none (verification only)

- [ ] **Step 1: Full test suite**

Run: `npm test 2>&1 | tail -30`
Expected: all pass (or only pre-existing unrelated failures — confirm against a clean `main` run if in doubt).

- [ ] **Step 2: Lint**

Run: `npm run lint 2>&1 | tail -15`
Expected: clean.

- [ ] **Step 3: Build renderer**

Run: `npm run build 2>&1 | tail -15`
Expected: build succeeds.

- [ ] **Step 4: Manual verification (via superpowers:verify / run skill)**

Drive the real app and confirm:
1. Open a **local** terminal, `cd` into a subdir, right-click its tab → **Copy Tab** → the new tab's shell starts in that subdir.
2. Open an **SSH** session, `cd` into a subdir, split it (context-menu Split Horizontal/Vertical) → the new pane runs `cd -- <dir>` and lands there. Test once with OSC 7 configured and once without (relies on the `/proc` probe).
3. Click the new **terminal icon** in the top tab bar (left of the file-transfer button) → a new local terminal opens.
4. Non-inheriting fallbacks: split a serial/telnet session → no `cd` injected, opens normally.

- [ ] **Step 5: Update the plan checkboxes and finish**

Mark all steps complete. Proceed to `superpowers:finishing-a-development-branch` to open the PR.

---

## Self-review notes

- **Spec coverage:** Feature 1 → Tasks 3-6 (copy path). Feature 2 → Tasks 2,3,5,6,7 (split + cd injection). Feature 3 → Tasks 8-9. cwd capture w/ SSH probe → Task 4. "cd always fires, decoupled from restore setting" → `resolveInheritedCwdIntent` (Task 2) has no `enabled`/`restoreState` gate. Edge cases (serial/mosh/et/win-shell) → Task 2 eligibility + Task 10 manual.
- **Type consistency:** option field `inheritedCwd` used identically in Tasks 3/5/6; session field `pendingInitialCwd` defined Task 1, applied Task 3, consumed Task 7; `resolveInheritedCwdIntent` signature identical in Tasks 2 and 7; `captureInheritedCwd` signature identical in Tasks 4 and 6.
- **No placeholders:** every code step shows concrete code and exact run commands.
