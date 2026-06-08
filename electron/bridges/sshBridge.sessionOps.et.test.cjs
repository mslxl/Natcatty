const test = require("node:test");
const assert = require("node:assert/strict");

const { createSessionOpsApi } = require("./sshBridge/sessionOps.cjs");

function makeApi(session, execOnEtSession) {
  const sessions = new Map([["et-1", session]]);
  return createSessionOpsApi({
    sessions,
    console,
    setTimeout,
    clearTimeout,
    execOnEtSession,
    iconv: { encodingExists: () => true },
    sessionEncodings: new Map(),
    resetSessionDecoders: () => {},
  });
}

test("getSessionDistroInfo probes ET sessions through execOnEtSession", async () => {
  let command = "";
  const api = makeApi(
    { type: "et", sshUserHost: "alice@example.test", sshOptions: [], sshEnv: {} },
    async (_session, cmd) => {
      command = cmd;
      return { success: true, stdout: "NAME=Ubuntu\n", stderr: "" };
    },
  );

  const result = await api.getSessionDistroInfo(null, { sessionId: "et-1" });

  assert.equal(result.success, true);
  assert.equal(result.stdout, "NAME=Ubuntu\n");
  assert.match(command, /os-release/);
});

test("getServerStats explicitly skips ET sessions instead of treating them as missing", async () => {
  const api = makeApi(
    { type: "et", sshUserHost: "alice@example.test", sshOptions: [], sshEnv: {} },
    async () => ({ success: true, stdout: "", stderr: "" }),
  );

  const result = await api.getServerStats(null, { sessionId: "et-1" });

  assert.equal(result.success, false);
  assert.match(result.error, /not supported for EternalTerminal/i);
});
