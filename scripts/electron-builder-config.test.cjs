const test = require("node:test");
const assert = require("node:assert/strict");

const config = require("../electron-builder.config.cjs");

test("unpacked MCP server includes its shared CommonJS dependencies", () => {
  assert.ok(
    config.asarUnpack.includes("electron/mcp/**/*"),
    "MCP server must stay unpacked so Codex can launch it as a child process",
  );
  assert.ok(
    config.asarUnpack.includes("lib/**/*.cjs"),
    "MCP server requires ../../lib/commandBlocklist.cjs from the unpacked runtime path",
  );
  assert.ok(
    config.asarUnpack.includes("lib/**/*.json"),
    "unpacked lib CommonJS modules require sibling JSON data files at runtime",
  );
});

test("build.files excludes per-platform agent binaries", () => {
  const files = config.files;
  const expectExclusions = [
    "!**/@anthropic-ai/claude-agent-sdk-*/**/*",
    "!node_modules/@anthropic-ai/claude-code-*/**/*",
    "!node_modules/@openai/codex-{darwin,linux,linuxmusl,win32}-*/**/*",
    "!node_modules/@github/copilot-{darwin,linux,linuxmusl,win32}-*/**/*",
    "!node_modules/@github/copilot/**/*",
  ];
  for (const glob of expectExclusions) {
    assert.ok(
      files.includes(glob),
      `build.files must exclude platform binary glob: ${glob}`,
    );
  }
});

test("asarUnpack no longer references removed legacy agent packages", () => {
  const unpack = config.asarUnpack.join("\n");
  for (const stale of [
    "@agentclientprotocol/claude-agent-acp",
    "@agentclientprotocol/sdk",
    "@zed-industries/codex-acp",
  ]) {
    assert.ok(
      !unpack.includes(stale),
      `asarUnpack must not reference removed package: ${stale}`,
    );
  }
});

test("asarUnpack keeps MCP server runtime deps unpacked", () => {
  // @modelcontextprotocol/sdk is now a direct dep and the MCP server hard-requires it.
  assert.ok(config.asarUnpack.includes("node_modules/@modelcontextprotocol/sdk/**/*"));
});

test("linux packaging uses multi-size build/icons instead of a single 1024px override", async () => {
  assert.equal(
    config.linux.icon,
    "icons",
    "linux.icon must point at build/icons so electron-builder installs hicolor/* sizes",
  );
  assert.equal(config.directories.buildResources, "build");

  const fs = require("node:fs");
  const path = require("node:path");
  const iconsDir = path.join(__dirname, "..", "build", "icons");
  for (const size of [16, 32, 48, 64, 128, 256, 512]) {
    const file = path.join(iconsDir, `${size}x${size}.png`);
    assert.ok(fs.existsSync(file), `expected Linux icon: build/icons/${size}x${size}.png`);
  }

  const { convertIcon } = require("app-builder-lib/out/util/iconConverter");
  const projectDir = path.join(__dirname, "..");
  const buildResources = path.join(projectDir, config.directories.buildResources);
  const sources = [config.linux.icon, config.mac?.icon ?? config.icon].filter(Boolean);
  const result = await convertIcon({
    sources,
    fallbackSources: [buildResources],
    roots: [buildResources, projectDir],
    format: "set",
    outDir: path.join(projectDir, "release", ".icon-config-test"),
  });
  const sizes = result.icons.map((icon) => icon.size);
  assert.ok(
    sizes.includes(48) && sizes.includes(256) && !sizes.every((size) => size === 1024),
    `expected standard hicolor sizes, got: ${sizes.join(", ")}`,
  );
});
