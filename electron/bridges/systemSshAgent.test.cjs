const test = require("node:test");
const assert = require("node:assert/strict");

const { utils } = require("ssh2");
const {
  prepareSystemSshAgent,
} = require("./systemSshAgent.cjs");

function makePublicKey() {
  return utils.generateKeyPairSync("ed25519").public;
}

function fakeAgent(publicKeys) {
  const identities = publicKeys.map((key) => utils.parseKey(key));
  return {
    getIdentities(callback) {
      callback(null, identities);
    },
    sign(_key, _data, _options, callback) {
      callback(null, Buffer.from("signature"));
    },
  };
}

function getIdentities(agent) {
  return new Promise((resolve, reject) => {
    agent.getIdentities((error, identities) => {
      if (error) reject(error);
      else resolve(identities);
    });
  });
}

test("prepareSystemSshAgent prioritizes the identity selected by IdentityFile", async () => {
  const unrelated = makePublicKey();
  const selected = makePublicKey();
  const agent = await prepareSystemSshAgent({
    socketPath: "/tmp/agent.sock",
    identityFilePaths: ["/Users/alice/.ssh/aws_root"],
    identitiesOnly: false,
  }, {
    createAgent: () => fakeAgent([unrelated, selected]),
    readFile: async () => `${selected} alice@mac\n`,
    platform: "linux",
  });

  const identities = await getIdentities(agent);
  assert.deepEqual(
    identities.map((key) => key.getPublicSSH().toString("base64")),
    [selected, unrelated].map((key) => utils.parseKey(key).getPublicSSH().toString("base64")),
  );
});

test("prepareSystemSshAgent excludes unrelated identities for IdentitiesOnly", async () => {
  const unrelated = makePublicKey();
  const selected = makePublicKey();
  const agent = await prepareSystemSshAgent({
    socketPath: "/tmp/agent.sock",
    identityFilePaths: ["/Users/alice/.ssh/aws_root"],
    identitiesOnly: true,
  }, {
    createAgent: () => fakeAgent([unrelated, selected]),
    readFile: async () => selected,
    platform: "linux",
  });

  const identities = await getIdentities(agent);
  assert.deepEqual(
    identities.map((key) => key.getPublicSSH().toString("base64")),
    [utils.parseKey(selected).getPublicSSH().toString("base64")],
  );
});

test("prepareSystemSshAgent asks macOS to load a missing configured identity from Keychain", async () => {
  const selected = makePublicKey();
  const sshAddCalls = [];

  await prepareSystemSshAgent({
    socketPath: "/private/tmp/agent.sock",
    identityFilePaths: ["/Users/alice/.ssh/aws_root"],
    identitiesOnly: true,
    useKeychain: true,
    addKeysToAgent: "yes",
  }, {
    createAgent: () => fakeAgent([]),
    readFile: async () => selected,
    runSshAdd: async (args) => sshAddCalls.push(args),
    platform: "darwin",
  });

  assert.deepEqual(sshAddCalls, [[
    "--apple-load-keychain",
    "/Users/alice/.ssh/aws_root",
  ]]);
});

test("prepareSystemSshAgent does not invoke macOS Keychain loading when the identity is already present", async () => {
  const selected = makePublicKey();
  const sshAddCalls = [];

  await prepareSystemSshAgent({
    socketPath: "/private/tmp/agent.sock",
    identityFilePaths: ["/Users/alice/.ssh/aws_root"],
    identitiesOnly: true,
    useKeychain: true,
    addKeysToAgent: "yes",
  }, {
    createAgent: () => fakeAgent([selected]),
    readFile: async () => selected,
    runSshAdd: async (args) => sshAddCalls.push(args),
    platform: "darwin",
  });

  assert.deepEqual(sshAddCalls, []);
});

test("prepareSystemSshAgent does not bypass AddKeysToAgent confirmation policies", async () => {
  const selected = makePublicKey();
  const sshAddCalls = [];

  await prepareSystemSshAgent({
    socketPath: "/private/tmp/agent.sock",
    identityFilePaths: ["/Users/alice/.ssh/aws_root"],
    identitiesOnly: true,
    useKeychain: true,
    addKeysToAgent: "confirm",
  }, {
    createAgent: () => fakeAgent([]),
    readFile: async () => selected,
    runSshAdd: async (args) => sshAddCalls.push(args),
    platform: "darwin",
  });

  assert.deepEqual(sshAddCalls, []);
});
