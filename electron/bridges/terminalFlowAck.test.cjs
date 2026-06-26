const test = require("node:test");
const assert = require("node:assert/strict");

const {
  FLOW_HIGH_WATER_MARK,
  FLOW_LOW_WATER_MARK,
  clearSessionFlowState,
  setRendererFlowPaused,
  trackAck,
  trackEmitted,
} = require("./terminalFlowAck.cjs");

function makeSession() {
  const calls = [];
  return {
    stream: {
      pause() {
        calls.push("pause");
      },
      resume() {
        calls.push("resume");
      },
    },
    _calls: calls,
  };
}

test("main-process watermarks match VS Code FlowControlConstants", () => {
  assert.equal(FLOW_HIGH_WATER_MARK, 100_000);
  assert.equal(FLOW_LOW_WATER_MARK, 5_000);
});

test("trackEmitted pauses once when unacked bytes cross the high watermark", () => {
  const session = makeSession();
  trackEmitted(session, FLOW_HIGH_WATER_MARK);
  assert.deepEqual(session._calls, ["pause"]);
  trackEmitted(session, 1024);
  assert.deepEqual(session._calls, ["pause"]);
});

test("trackAck resumes after draining to the low watermark", () => {
  const session = makeSession();
  trackEmitted(session, FLOW_HIGH_WATER_MARK + FLOW_LOW_WATER_MARK);
  trackAck(session, FLOW_LOW_WATER_MARK);
  assert.deepEqual(session._calls, ["pause"]);
  trackAck(session, FLOW_HIGH_WATER_MARK);
  assert.deepEqual(session._calls, ["pause", "resume"]);
});

test("renderer pause flag keeps the stream paused until cleared", () => {
  const session = makeSession();
  trackEmitted(session, FLOW_HIGH_WATER_MARK);
  setRendererFlowPaused(session, true);
  trackAck(session, FLOW_HIGH_WATER_MARK);
  assert.deepEqual(session._calls, ["pause"]);
  setRendererFlowPaused(session, false);
  assert.deepEqual(session._calls, ["pause", "resume"]);
});

test("clearSessionFlowState resumes and resets counters", () => {
  const session = makeSession();
  trackEmitted(session, FLOW_HIGH_WATER_MARK);
  clearSessionFlowState(session);
  assert.deepEqual(session._calls, ["pause", "resume"]);
  assert.equal(session.flowState.unackedBytes, 0);
  assert.equal(session.flowState.rendererPaused, false);
});