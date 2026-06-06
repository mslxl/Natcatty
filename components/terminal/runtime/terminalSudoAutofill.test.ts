import test from "node:test";
import assert from "node:assert/strict";
import {
  createSudoPasswordAutofill,
  getSingleBracketedPasteLine,
  isSudoPasswordPrompt,
  shouldArmSudoPasswordAutofill,
} from "./terminalSudoAutofill";

const TEST_PROMPT = "[sudo] password for alice: ";

test("isSudoPasswordPrompt detects the standard sudo password prompt", () => {
  assert.equal(isSudoPasswordPrompt("[sudo] password for alice: "), true);
});

test("isSudoPasswordPrompt ignores ordinary output mentioning sudo and password", () => {
  assert.equal(isSudoPasswordPrompt("try sudo if the password is required\n"), false);
  assert.equal(isSudoPasswordPrompt("password for alice: "), false);
});

test("sudo autofill handles prompts split across chunks without changing the command", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    now: () => 1_000,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo apt update"), "sudo apt update");
  autofill.handleOutput("[sudo] password");
  autofill.handleOutput(" for alice: ");

  assert.deepEqual(writes, ["secret\n"]);
});

test("sudo autofill ignores sudo-looking output until a sudo command is submitted", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    write: (data) => writes.push(data),
  });

  autofill.handleOutput("[sudo] password for alice: ");

  assert.deepEqual(writes, []);
});

test("sudo autofill sends the password once for a submitted sudo command", () => {
  const writes: string[] = [];
  let now = 1_000;
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    now: () => now,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo -i"), "sudo -i");
  autofill.handleOutput(TEST_PROMPT);
  now += 500;
  autofill.handleOutput(TEST_PROMPT);
  now += 5_000;
  autofill.handleOutput(TEST_PROMPT);

  assert.deepEqual(writes, ["secret\n"]);
});

test("sudo autofill allows target command prompt-like options", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo ssh -p 22 host"), "sudo ssh -p 22 host");
  assert.equal(autofill.prepareCommand("sudo useradd -p hash alice"), "sudo useradd -p hash alice");
  assert.deepEqual(writes, []);
});

test("sudo autofill handles sudo short options with attached arguments", () => {
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    write: () => {},
  });

  assert.equal(autofill.prepareCommand("sudo -upostgres whoami"), "sudo -upostgres whoami");
});

test("sudo autofill leaves commands with explicit sudo prompts unchanged", () => {
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    write: () => {},
  });

  assert.equal(autofill.prepareCommand("sudo -p custom whoami"), null);
  assert.equal(autofill.prepareCommand("sudo --prompt=custom whoami"), null);
});

test("sudo autofill extracts single-line bracketed paste content", () => {
  assert.equal(getSingleBracketedPasteLine("\x1b[200~sudo whoami\x1b[201~"), "sudo whoami");
  assert.equal(getSingleBracketedPasteLine("\x1b[200~sudo whoami\rpwd\x1b[201~"), null);
});

test("sudo autofill ignores expired sudo command arms", () => {
  const writes: string[] = [];
  let now = 1_000;
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    now: () => now,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo whoami"), "sudo whoami");
  now += 31_000;
  autofill.handleOutput(TEST_PROMPT);

  assert.deepEqual(writes, []);
});

test("sudo autofill handles default sudo-looking output after a submitted command", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo ./program"), "sudo ./program");
  autofill.handleOutput("[sudo] password for alice: ");

  assert.deepEqual(writes, ["secret\n"]);
});

test("sudo autofill ignores hidden control-sequence prompt text", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo whoami"), "sudo whoami");
  autofill.handleOutput(`\x1b[8m${TEST_PROMPT}\x1b[0m`);

  assert.deepEqual(writes, []);
});

test("sudo autofill does nothing without a saved password", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "",
    write: (data) => writes.push(data),
  });

  autofill.handleOutput("[sudo] password for alice: ");

  assert.deepEqual(writes, []);
});

test("shouldArmSudoPasswordAutofill only arms direct sudo commands", () => {
  assert.equal(shouldArmSudoPasswordAutofill("sudo whoami"), true);
  assert.equal(shouldArmSudoPasswordAutofill("command sudo whoami"), true);
  assert.equal(shouldArmSudoPasswordAutofill("builtin sudo whoami"), true);
  assert.equal(shouldArmSudoPasswordAutofill("echo '[sudo] password for alice:'"), false);
  assert.equal(shouldArmSudoPasswordAutofill("cat sudo.log"), false);
});
