const test = require("node:test");
const assert = require("node:assert/strict");
const { buildUploadPlan } = require("./zmodemHelper.cjs");

const never = () => { throw new Error("resolver should not be called"); };

test("no conflicts: all indices offered, none removed, resolver untouched", async () => {
  const plan = await buildUploadPlan(["a.txt", "b.txt"], [], never);
  assert.deepEqual(plan, { offerIndices: [0, 1], removeIndices: [], aborted: false });
});

test("overwrite a conflict: index both removed and offered", async () => {
  const plan = await buildUploadPlan(["a.txt", "b.txt"], ["b.txt"], async () => ({ action: "overwrite" }));
  assert.deepEqual(plan, { offerIndices: [0, 1], removeIndices: [1], aborted: false });
});

test("skip a conflict: index omitted from offer and remove", async () => {
  const plan = await buildUploadPlan(["a.txt", "b.txt"], ["b.txt"], async () => ({ action: "skip" }));
  assert.deepEqual(plan, { offerIndices: [0], removeIndices: [], aborted: false });
});

test("cancel aborts the whole transfer", async () => {
  const plan = await buildUploadPlan(["a.txt", "b.txt"], ["b.txt"], async () => ({ action: "cancel" }));
  assert.deepEqual(plan, { offerIndices: [], removeIndices: [], aborted: true });
});

test("applyToRest reuses the action and stops prompting", async () => {
  let calls = 0;
  const plan = await buildUploadPlan(["a", "b", "c"], ["a", "b", "c"],
    async () => { calls++; return { action: "overwrite", applyToRest: true }; });
  assert.equal(calls, 1);
  assert.deepEqual(plan, { offerIndices: [0, 1, 2], removeIndices: [0, 1, 2], aborted: false });
});

test("only conflicting files invoke the resolver; order preserved", async () => {
  const seen = [];
  const plan = await buildUploadPlan(["a", "b", "c"], ["b"],
    async (n) => { seen.push(n); return { action: "skip" }; });
  assert.deepEqual(seen, ["b"]);
  assert.deepEqual(plan.offerIndices, [0, 2]);
});

test("duplicate basenames keep independent per-file decisions", async () => {
  // Two different local files share a basename; skip the first, overwrite the second.
  const actions = ["skip", "overwrite"];
  let i = 0;
  const plan = await buildUploadPlan(["x.txt", "x.txt"], ["x.txt"],
    async () => ({ action: actions[i++] }));
  assert.deepEqual(plan, { offerIndices: [1], removeIndices: [1], aborted: false });
});
