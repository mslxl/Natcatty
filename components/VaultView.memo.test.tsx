import test from "node:test";
import assert from "node:assert/strict";

import { syncVaultViewMemoSection, vaultViewAreEqual } from "./VaultView.tsx";

const baseProps = {
  hosts: [],
  keys: [],
  identities: [],
  proxyProfiles: [],
  snippets: [],
  snippetPackages: [],
  notes: [],
  noteGroups: [],
  customGroups: [],
  knownHosts: [],
  connectionLogs: [],
  sessions: [],
  managedSources: [],
  groupConfigs: {},
  terminalThemeId: "default",
  terminalFontSize: 14,
  navigateToSection: null,
};

test("VaultView re-renders when an external section navigation request changes", () => {
  assert.equal(
    vaultViewAreEqual(
      baseProps as never,
      { ...baseProps, navigateToSection: "snippets" } as never,
    ),
    false,
  );
});

test("VaultView memo does not depend on shellHistory prop identity", () => {
  assert.equal(
    vaultViewAreEqual(baseProps as never, { ...baseProps } as never),
    true,
  );
  assert.equal(
    Object.prototype.hasOwnProperty.call(baseProps, "shellHistory"),
    false,
  );
});

test("VaultView re-renders when proxy profiles change", () => {
  assert.equal(
    vaultViewAreEqual(
      baseProps as never,
      {
        ...baseProps,
        proxyProfiles: [
          {
            id: "proxy-1",
            label: "Proxy",
            config: { type: "http", host: "proxy.example.com", port: 3128 },
            createdAt: 1,
          },
        ],
      } as never,
    ),
    false,
  );
});

test("VaultView re-renders when host-key verification setting changes", () => {
  assert.equal(
    vaultViewAreEqual(
      baseProps as never,
      {
        ...baseProps,
        terminalSettings: {
          verifyHostKeys: false,
        },
      } as never,
    ),
    false,
  );
});

test("VaultView ignores connectionLogs identity when not on logs section", () => {
  syncVaultViewMemoSection("hosts");
  const nextLogs = [{ id: "log-1" }];
  assert.equal(
    vaultViewAreEqual(
      baseProps as never,
      { ...baseProps, connectionLogs: nextLogs } as never,
    ),
    true,
  );
});

test("VaultView compares connectionLogs identity when on logs section", () => {
  syncVaultViewMemoSection("logs");
  const nextLogs = [{ id: "log-2" }];
  assert.equal(
    vaultViewAreEqual(
      baseProps as never,
      { ...baseProps, connectionLogs: nextLogs } as never,
    ),
    false,
  );
  assert.equal(
    vaultViewAreEqual(
      { ...baseProps, connectionLogs: nextLogs } as never,
      { ...baseProps, connectionLogs: nextLogs } as never,
    ),
    true,
  );
  syncVaultViewMemoSection("hosts");
});
