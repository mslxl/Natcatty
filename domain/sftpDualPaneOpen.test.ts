import test from "node:test";
import assert from "node:assert/strict";

import {
  applyDualPaneSftpOpen,
  canOpenDualPaneSftp,
  planDualPaneSftpOpen,
  type DualPaneSftpTab,
} from "./sftpDualPaneOpen.ts";

const tab = (
  id: string,
  options: Partial<DualPaneSftpTab> = {},
): DualPaneSftpTab => ({
  id,
  isLocal: false,
  hostId: null,
  hasConnection: true,
  ...options,
});

test("canOpenDualPaneSftp allows SSH-like hosts and rejects serial/plugin", () => {
  assert.equal(canOpenDualPaneSftp({}), true);
  assert.equal(canOpenDualPaneSftp({ protocol: "ssh" }), true);
  assert.equal(canOpenDualPaneSftp({ protocol: "mosh" }), true);
  assert.equal(canOpenDualPaneSftp({ protocol: "serial" }), false);
  assert.equal(canOpenDualPaneSftp({ protocol: "plugin:example.provider.ssh" }), false);
});

test("planDualPaneSftpOpen connects local left and host right on empty panes", () => {
  assert.deepEqual(
    planDualPaneSftpOpen({ leftTabs: [], rightTabs: [], hostId: "host-1" }),
    {
      selectLeftTabId: null,
      connectLeftLocal: true,
      addLeftTab: true,
      selectRightTabId: null,
      connectRightHost: true,
      addRightTab: true,
    },
  );
});

test("planDualPaneSftpOpen reuses an existing local left tab and matching right host", () => {
  const plan = planDualPaneSftpOpen({
    leftTabs: [tab("left-remote", { hostId: "other" }), tab("left-local", { isLocal: true, hostId: "local" })],
    rightTabs: [tab("right-host", { hostId: "host-1" })],
    hostId: "host-1",
  });
  assert.deepEqual(plan, {
    selectLeftTabId: "left-local",
    connectLeftLocal: false,
    addLeftTab: false,
    selectRightTabId: "right-host",
    connectRightHost: false,
    addRightTab: false,
  });
});

test("planDualPaneSftpOpen uses idle panes instead of adding tabs", () => {
  const plan = planDualPaneSftpOpen({
    leftTabs: [tab("left-idle", { hasConnection: false })],
    rightTabs: [tab("right-idle", { hasConnection: false })],
    hostId: "host-1",
  });
  assert.equal(plan.selectLeftTabId, "left-idle");
  assert.equal(plan.connectLeftLocal, true);
  assert.equal(plan.addLeftTab, false);
  assert.equal(plan.selectRightTabId, "right-idle");
  assert.equal(plan.connectRightHost, true);
  assert.equal(plan.addRightTab, false);
});

test("applyDualPaneSftpOpen selects existing tabs without reconnecting", () => {
  const calls: string[] = [];
  const plan = applyDualPaneSftpOpen(
    {
      leftTabs: [tab("left-local", { isLocal: true, hostId: "local" })],
      rightTabs: [tab("right-host", { hostId: "host-1" })],
      selectTab: (side, tabId) => calls.push(`select:${side}:${tabId}`),
      connect: (side, host, options) => {
        const target = host === "local" ? "local" : host.id;
        const mode = options?.forceNewTab ? "new" : (options?.tabId ? `tab:${options.tabId}` : "reuse");
        calls.push(`connect:${side}:${target}:${mode}`);
      },
    },
    { id: "host-1" },
  );
  assert.equal(plan.connectLeftLocal, false);
  assert.equal(plan.connectRightHost, false);
  assert.deepEqual(calls, ["select:left:left-local", "select:right:right-host"]);
});

test("applyDualPaneSftpOpen adds tabs when both sides are occupied by other hosts", () => {
  const calls: string[] = [];
  applyDualPaneSftpOpen(
    {
      leftTabs: [tab("left-remote", { hostId: "other" })],
      rightTabs: [tab("right-other", { hostId: "other" })],
      selectTab: (side, tabId) => calls.push(`select:${side}:${tabId}`),
      connect: (side, host, options) => {
        const target = host === "local" ? "local" : host.id;
        const mode = options?.forceNewTab ? "new" : (options?.tabId ? `tab:${options.tabId}` : "reuse");
        calls.push(`connect:${side}:${target}:${mode}`);
      },
    },
    { id: "host-1" },
  );
  assert.deepEqual(calls, [
    "connect:left:local:new",
    "connect:right:host-1:new",
  ]);
});
