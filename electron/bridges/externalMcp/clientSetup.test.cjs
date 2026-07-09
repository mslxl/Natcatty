"use strict";

const { describe, it } = require("node:test");
const assert = require("node:assert/strict");

const {
  EXTERNAL_MCP_CODEX_NAME,
  classifyCodexExternalMcpStatus,
  parseCodexMcpList,
} = require("./codexSetup.cjs");
const {
  EXTERNAL_MCP_CLAUDE_NAME,
  classifyClaudeExternalMcpStatus,
} = require("./claudeSetup.cjs");
const {
  EXTERNAL_MCP_GROK_NAME,
  classifyGrokExternalMcpStatus,
  parseGrokMcpList,
} = require("./grokSetup.cjs");

describe("external MCP client setup classifiers", () => {
  it("parses Codex MCP list and detects configured launcher", () => {
    const entries = parseCodexMcpList(JSON.stringify([
      {
        name: EXTERNAL_MCP_CODEX_NAME,
        enabled: true,
        transport: {
          type: "stdio",
          command: "/path/to/netcatty-external-mcp",
          args: [],
          env: { NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE: "/tmp/discovery.json" },
        },
      },
    ]));
    const status = classifyCodexExternalMcpStatus({
      entries,
      launcherPath: "/path/to/netcatty-external-mcp",
      codexPath: "/usr/bin/codex",
      discoveryEnv: { NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE: "/tmp/discovery.json" },
    });
    assert.equal(status.state, "configured");
  });

  it("treats Codex launcher without discovery env as not_configured", () => {
    const status = classifyCodexExternalMcpStatus({
      entries: [{
        name: EXTERNAL_MCP_CODEX_NAME,
        transport: { type: "stdio", command: "/path/to/netcatty-external-mcp", args: [], env: null },
      }],
      launcherPath: "/path/to/netcatty-external-mcp",
      codexPath: "/usr/bin/codex",
      discoveryEnv: { NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE: "/tmp/discovery.json" },
    });
    assert.equal(status.state, "not_configured");
  });

  it("flags Codex conflict when command differs", () => {
    const status = classifyCodexExternalMcpStatus({
      entries: [{
        name: EXTERNAL_MCP_CODEX_NAME,
        transport: { type: "stdio", command: "/other/path", args: [] },
      }],
      launcherPath: "/path/to/netcatty-external-mcp",
      codexPath: "/usr/bin/codex",
    });
    assert.equal(status.state, "conflict");
  });

  it("classifies Claude configured and missing states", () => {
    const configured = classifyClaudeExternalMcpStatus({
      getResult: { exitCode: 0, stdout: `${EXTERNAL_MCP_CLAUDE_NAME}: /path/to/netcatty-external-mcp - connected`, stderr: "" },
      launcherPath: "/path/to/netcatty-external-mcp",
      claudePath: "/usr/bin/claude",
    });
    assert.equal(configured.state, "configured");

    const quoted = classifyClaudeExternalMcpStatus({
      getResult: {
        exitCode: 0,
        stdout: `Command: "/path/to/netcatty-external-mcp"\nStatus: connected`,
        stderr: "",
      },
      launcherPath: "/path/to/netcatty-external-mcp",
      claudePath: "/usr/bin/claude",
    });
    assert.equal(quoted.state, "configured");

    const withEnvFlags = classifyClaudeExternalMcpStatus({
      getResult: {
        exitCode: 0,
        stdout: `Command: -e NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE=/tmp/d.json -- /path/to/netcatty-external-mcp`,
        stderr: "",
      },
      launcherPath: "/path/to/netcatty-external-mcp",
      claudePath: "/usr/bin/claude",
      discoveryEnv: { NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE: "/tmp/d.json" },
    });
    assert.equal(withEnvFlags.state, "configured");

    const missingEnv = classifyClaudeExternalMcpStatus({
      getResult: {
        exitCode: 0,
        stdout: `Command: /path/to/netcatty-external-mcp`,
        stderr: "",
      },
      launcherPath: "/path/to/netcatty-external-mcp",
      claudePath: "/usr/bin/claude",
      discoveryEnv: { NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE: "/tmp/d.json" },
    });
    assert.equal(missingEnv.state, "not_configured");

    const withExtraArgs = classifyClaudeExternalMcpStatus({
      getResult: {
        exitCode: 0,
        stdout: `Command: /path/to/netcatty-external-mcp --evil`,
        stderr: "",
      },
      launcherPath: "/path/to/netcatty-external-mcp",
      claudePath: "/usr/bin/claude",
    });
    assert.equal(withExtraArgs.state, "conflict");

    const missing = classifyClaudeExternalMcpStatus({
      getResult: {
        exitCode: 1,
        stdout: "",
        stderr: `No MCP server found with name: "${EXTERNAL_MCP_CLAUDE_NAME}"`,
      },
      launcherPath: "/path/to/netcatty-external-mcp",
      claudePath: "/usr/bin/claude",
    });
    assert.equal(missing.state, "not_configured");

    const missingNamed = classifyClaudeExternalMcpStatus({
      getResult: {
        exitCode: 1,
        stdout: "",
        stderr: `No MCP server named ${EXTERNAL_MCP_CLAUDE_NAME}`,
      },
      launcherPath: "/path/to/netcatty-external-mcp",
      claudePath: "/usr/bin/claude",
    });
    assert.equal(missingNamed.state, "not_configured");
  });

  it("parses Grok MCP list and detects configured launcher", () => {
    const entries = parseGrokMcpList(JSON.stringify([
      {
        name: EXTERNAL_MCP_GROK_NAME,
        enabled: true,
        transport: { type: "stdio", command: "/path/to/netcatty-external-mcp", args: [] },
        env: { NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE: "/tmp/discovery.json" },
      },
    ]));
    const status = classifyGrokExternalMcpStatus({
      entries,
      launcherPath: "/path/to/netcatty-external-mcp",
      grokPath: "/usr/bin/grok",
      discoveryEnv: { NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE: "/tmp/discovery.json" },
    });
    assert.equal(status.state, "configured");
  });

  it("flags Grok conflict when command differs", () => {
    const status = classifyGrokExternalMcpStatus({
      entries: [{
        name: EXTERNAL_MCP_GROK_NAME,
        transport: { type: "stdio", command: "/other/path", args: [] },
      }],
      launcherPath: "/path/to/netcatty-external-mcp",
      grokPath: "/usr/bin/grok",
    });
    assert.equal(status.state, "conflict");
  });

  it("flags Grok conflict when launcher has extra args", () => {
    const status = classifyGrokExternalMcpStatus({
      entries: [{
        name: EXTERNAL_MCP_GROK_NAME,
        transport: { type: "stdio", command: "/path/to/netcatty-external-mcp", args: ["--evil"] },
      }],
      launcherPath: "/path/to/netcatty-external-mcp",
      grokPath: "/usr/bin/grok",
    });
    assert.equal(status.state, "conflict");
  });

  it("classifies Grok missing when CLI is absent", () => {
    const status = classifyGrokExternalMcpStatus({
      entries: [],
      launcherPath: "/path/to/netcatty-external-mcp",
      grokPath: null,
    });
    assert.equal(status.state, "grok_not_found");
  });
});
