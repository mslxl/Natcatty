import test from "node:test";
import assert from "node:assert/strict";
import {
  createSudoPasswordAutofill,
  getSingleBracketedPasteLine,
  isSudoPasswordPrompt,
  shouldArmSudoPasswordAutofill,
} from "./terminalSudoAutofill";

const TEST_PROMPT = "[sudo] password for alice: ";
const TEST_MARKER = "__NETCATTY_SUDO_test__";
const TEST_MARKED_PROMPT = `[sudo] password for alice: ${TEST_MARKER}`;

const markedCommand = (commandTail: string) =>
  `sudo -p '[sudo] password for %p: ${TEST_MARKER}'${commandTail}`;

test("isSudoPasswordPrompt detects the standard sudo password prompt", () => {
  assert.equal(isSudoPasswordPrompt("[sudo] password for alice: "), true);
});

test("isSudoPasswordPrompt ignores ordinary output mentioning sudo and password", () => {
  assert.equal(isSudoPasswordPrompt("try sudo if the password is required\n"), false);
  assert.equal(isSudoPasswordPrompt("password for alice: "), false);
});

test("isSudoPasswordPrompt requires the expected prompt marker when provided", () => {
  assert.equal(isSudoPasswordPrompt(TEST_MARKED_PROMPT, TEST_MARKER), true);
  assert.equal(isSudoPasswordPrompt("[sudo] password for alice: ", TEST_MARKER), false);
});

test("sudo autofill handles marked prompts split across chunks", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    now: () => 1_000,
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo apt update"), markedCommand(" apt update"));
  assert.equal(autofill.handleOutput("[sudo] password for alice: "), "[sudo] password for alice: ");
  assert.equal(autofill.handleOutput(TEST_MARKER), "");

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
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo -i"), markedCommand(" -i"));
  autofill.handleOutput(TEST_MARKED_PROMPT);
  now += 500;
  autofill.handleOutput(TEST_MARKED_PROMPT);
  now += 5_000;
  autofill.handleOutput(TEST_MARKED_PROMPT);

  assert.deepEqual(writes, ["secret\n"]);
});

test("sudo autofill allows target command prompt-like options", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo ssh -p 22 host"), markedCommand(" ssh -p 22 host"));
  assert.equal(autofill.prepareCommand("sudo useradd -p hash alice"), markedCommand(" useradd -p hash alice"));
  assert.deepEqual(writes, []);
});

test("sudo autofill handles sudo short options with attached arguments", () => {
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: () => {},
  });

  assert.equal(autofill.prepareCommand("sudo -upostgres whoami"), markedCommand(" -upostgres whoami"));
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
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo whoami"), markedCommand(" whoami"));
  now += 31_000;
  autofill.handleOutput(TEST_MARKED_PROMPT);

  assert.deepEqual(writes, []);
});

test("sudo autofill ignores default sudo-looking output after a submitted command", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo ./program"), markedCommand(" ./program"));
  autofill.handleOutput("[sudo] password for alice: ");

  assert.deepEqual(writes, []);
});

test("sudo autofill ignores prompt-shaped command output after a submitted command", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(
    autofill.prepareCommand("sudo printf '[sudo] password for alice: '"),
    markedCommand(" printf '[sudo] password for alice: '"),
  );
  autofill.handleOutput("[sudo] password for alice: ");

  assert.deepEqual(writes, []);
});

test("sudo autofill stays armed after ordinary output before the password prompt", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo whoami"), markedCommand(" whoami"));
  assert.equal(autofill.handleOutput("sudo: this is the first time notice"), "sudo: this is the first time notice");
  assert.equal(autofill.handleOutput(TEST_MARKED_PROMPT), "[sudo] password for alice: ");

  assert.deepEqual(writes, ["secret\n"]);
});

test("sudo autofill releases prompt-shaped warm sudo output without sending a password", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(
    autofill.prepareCommand("sudo printf '[sudo] password for alice: '"),
    markedCommand(" printf '[sudo] password for alice: '"),
  );
  assert.equal(autofill.handleOutput("[sudo] password for alice: "), "[sudo] password for alice: ");

  assert.deepEqual(writes, []);
});

test("sudo autofill hides the prepared command and marker from output", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  const prepared = autofill.prepareCommand("sudo whoami");
  assert.equal(prepared, markedCommand(" whoami"));

  assert.equal(autofill.handleOutput(`${prepared ?? ""}\r\n`), "sudo whoami\r\n");
  assert.equal(autofill.handleOutput(TEST_MARKED_PROMPT), "[sudo] password for alice: ");
  assert.deepEqual(writes, ["secret\n"]);
});

test("sudo autofill hides split prepared command and marker output", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  const prepared = autofill.prepareCommand("sudo whoami");
  assert.equal(prepared, markedCommand(" whoami"));
  const preparedText = prepared ?? "";
  const preparedSplitIndex = Math.floor(preparedText.length / 2);
  assert.equal(autofill.handleOutput(preparedText.slice(0, preparedSplitIndex)), "");
  assert.equal(
    autofill.handleOutput(`${preparedText.slice(preparedSplitIndex)}\r\n`),
    "sudo whoami\r\n",
  );

  const promptSplitIndex = TEST_MARKED_PROMPT.length - 6;
  assert.equal(autofill.handleOutput(TEST_MARKED_PROMPT.slice(0, promptSplitIndex)), "");
  assert.equal(
    autofill.handleOutput(TEST_MARKED_PROMPT.slice(promptSplitIndex)),
    "[sudo] password for alice: ",
  );
  assert.deepEqual(writes, ["secret\n"]);
});

test("sudo autofill keeps sanitizing later shell-history echoes", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  const prepared = autofill.prepareCommand("sudo whoami");
  assert.equal(prepared, markedCommand(" whoami"));
  assert.equal(autofill.handleOutput(TEST_MARKED_PROMPT), "[sudo] password for alice: ");
  assert.equal(autofill.handleOutput(`${prepared ?? ""}\r\n`), "sudo whoami\r\n");

  assert.deepEqual(writes, ["secret\n"]);
});

test("sudo autofill does not hide completed output when sudo timestamp is warm", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  const prepared = autofill.prepareCommand("sudo true");
  assert.equal(prepared, markedCommand(" true"));

  assert.equal(autofill.handleOutput(`${prepared}\r\n`), "sudo true\r\n");
  assert.deepEqual(writes, []);
});

test("sudo autofill releases non-prompt output when sudo timestamp is warm", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  const prepared = autofill.prepareCommand("sudo printf ok");
  assert.equal(prepared, markedCommand(" printf ok"));

  assert.equal(autofill.handleOutput(`${prepared}\r\n`), "sudo printf ok\r\n");
  assert.equal(autofill.handleOutput("ok"), "ok");
  assert.deepEqual(writes, []);
});

test("sudo autofill ignores hidden control-sequence prompt text", () => {
  const writes: string[] = [];
  const autofill = createSudoPasswordAutofill({
    password: "secret",
    createPromptMarker: () => TEST_MARKER,
    write: (data) => writes.push(data),
  });

  assert.equal(autofill.prepareCommand("sudo whoami"), markedCommand(" whoami"));
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
