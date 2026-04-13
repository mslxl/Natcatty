const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");

const { buildUserSkillsContext, scanUserSkills } = require("./userSkills.cjs");

async function withUserSkills(skillDefinitions, run) {
  const rootDir = await fs.mkdtemp(path.join(os.tmpdir(), "netcatty-user-skills-"));
  const userDataDir = path.join(rootDir, "userData");
  const skillsDir = path.join(userDataDir, "Skills");
  await fs.mkdir(skillsDir, { recursive: true });

  for (const skill of skillDefinitions) {
    const skillDir = path.join(skillsDir, skill.directoryName);
    await fs.mkdir(skillDir, { recursive: true });
    const content = [
      "---",
      `name: ${skill.name}`,
      `description: ${skill.description}`,
      "---",
      "",
      skill.body,
      "",
    ].join("\n");
    await fs.writeFile(path.join(skillDir, "SKILL.md"), content, "utf8");
  }

  const electronApp = {
    getPath(key) {
      return key === "userData" ? userDataDir : "";
    },
  };

  try {
    await run(electronApp);
  } finally {
    await fs.rm(rootDir, { recursive: true, force: true });
  }
}

test("does not auto-match a user skill from an absolute path segment", async () => {
  await withUserSkills(
    [
      {
        directoryName: "Tmp Helper",
        name: "tmp",
        description: "Helper for scratch space workflows.",
        body: "Body for tmp",
      },
    ],
    async (electronApp) => {
      const result = await buildUserSkillsContext(
        electronApp,
        "please inspect /tmp/netcatty.log",
        [],
      );

      assert.equal(result.context.includes("Matched user-managed skills for this request:"), false);
      assert.equal(result.context.includes("Body for tmp"), false);
    },
  );
});

test("keeps every explicitly selected skill in the built context", async () => {
  await withUserSkills(
    [
      {
        directoryName: "Alpha One",
        name: "Alpha One",
        description: "Alpha helper.",
        body: "Body for Alpha One",
      },
      {
        directoryName: "Beta Two",
        name: "Beta Two",
        description: "Beta helper.",
        body: "Body for Beta Two",
      },
      {
        directoryName: "Gamma Three",
        name: "Gamma Three",
        description: "Gamma helper.",
        body: "Body for Gamma Three",
      },
    ],
    async (electronApp) => {
      const result = await buildUserSkillsContext(
        electronApp,
        "plain prompt",
        ["alpha-one", "beta-two", "gamma-three"],
      );

      assert.equal(result.context.includes("Body for Alpha One"), true);
      assert.equal(result.context.includes("Body for Beta Two"), true);
      assert.equal(result.context.includes("Body for Gamma Three"), true);
    },
  );
});

test("initializing an empty skills directory creates only an instructions file", async () => {
  await withUserSkills([], async (electronApp) => {
    const status = await scanUserSkills(electronApp);
    const entries = await fs.readdir(status.directoryPath);

    assert.deepEqual(status.skills, []);
    assert.equal(status.readyCount, 0);
    assert.equal(status.warningCount, 0);
    assert.deepEqual(entries.sort(), ["README.txt"]);
  });
});
