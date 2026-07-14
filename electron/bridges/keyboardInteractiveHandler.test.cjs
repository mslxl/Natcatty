const test = require("node:test");
const assert = require("node:assert/strict");

const keyboardInteractiveHandler = require("./keyboardInteractiveHandler.cjs");

test("keyboard-interactive responses from the wrong renderer are rejected and kept pending", () => {
  const finishCalls = [];
  const requestId = keyboardInteractiveHandler.generateRequestId("test");
  keyboardInteractiveHandler.storeRequest(requestId, (responses) => {
    finishCalls.push(responses);
  }, 101, "session-1");

  const wrongResult = keyboardInteractiveHandler.handleResponse(
    { sender: { id: 202 } },
    { requestId, responses: ["wrong"] },
  );
  assert.deepEqual(wrongResult, { success: false, error: "Wrong sender" });
  assert.deepEqual(finishCalls, []);
  assert.equal(keyboardInteractiveHandler.getRequests().has(requestId), true);

  const correctResult = keyboardInteractiveHandler.handleResponse(
    { sender: { id: 101 } },
    { requestId, responses: ["correct"] },
  );
  assert.deepEqual(correctResult, { success: true });
  assert.deepEqual(finishCalls, [["correct"]]);
  assert.equal(keyboardInteractiveHandler.getRequests().has(requestId), false);
});

test("keyboard-interactive responses never write password or OTP contents to logs", (t) => {
  const logs = [];
  t.mock.method(console, "log", (...args) => logs.push(args));
  const finishCalls = [];
  const requestId = keyboardInteractiveHandler.generateRequestId("secret-log-test");
  const secrets = ["secondary-password-secret", "123456-otp-secret"];

  keyboardInteractiveHandler.storeRequest(requestId, (responses) => {
    finishCalls.push(responses);
  }, 303, "session-secret-log-test");

  const result = keyboardInteractiveHandler.handleResponse(
    { sender: { id: 303 } },
    { requestId, responses: secrets, cancelled: false },
  );

  assert.deepEqual(result, { success: true });
  assert.deepEqual(finishCalls, [secrets]);
  const serializedLogs = JSON.stringify(logs);
  for (const secret of secrets) {
    assert.equal(serializedLogs.includes(secret), false);
  }
  assert.equal(
    logs.some((entry) => entry.some((value) => value?.responsesCount === secrets.length)),
    true,
  );
});
