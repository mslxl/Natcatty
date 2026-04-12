const fs = require("node:fs");
const fsPromises = require("node:fs/promises");
const path = require("node:path");

const USER_SKILLS_DIR_NAME = "Skills";
const EXAMPLE_SKILL_DIR_NAME = "Example Skill";
const MAX_SKILL_BYTES = 24 * 1024;
const MAX_DESCRIPTION_LENGTH = 280;
const MAX_INDEX_SKILLS = 8;
const MAX_MATCHED_SKILLS = 2;
const MAX_MATCHED_SKILL_CHARS = 6000;

const STOPWORDS = new Set([
  "the", "and", "for", "with", "that", "this", "from", "into", "when", "then",
  "only", "your", "will", "should", "have", "has", "had", "using", "use",
  "agent", "skill", "skills", "task", "file", "files", "user", "into", "about",
]);

function stripQuotes(value) {
  const trimmed = String(value || "").trim();
  if ((trimmed.startsWith('"') && trimmed.endsWith('"')) || (trimmed.startsWith("'") && trimmed.endsWith("'"))) {
    return trimmed.slice(1, -1);
  }
  return trimmed;
}

function slugifySkill(value) {
  return String(value || "")
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function tokenize(value) {
  return String(value || "")
    .toLowerCase()
    .split(/[^a-z0-9]+/i)
    .map((token) => token.trim())
    .filter((token) => token.length >= 3 && !STOPWORDS.has(token));
}

function parseFrontmatter(content) {
  const match = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?/.exec(content);
  if (!match) {
    return { attributes: {}, body: content, hasFrontmatter: false };
  }

  const attributes = {};
  for (const rawLine of match[1].split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const colonIndex = line.indexOf(":");
    if (colonIndex <= 0) continue;
    const key = line.slice(0, colonIndex).trim();
    const value = stripQuotes(line.slice(colonIndex + 1).trim());
    if (key) attributes[key] = value;
  }

  return {
    attributes,
    body: content.slice(match[0].length),
    hasFrontmatter: true,
  };
}

function getUserSkillsDir(electronApp) {
  const userDataDir = electronApp?.getPath?.("userData");
  if (!userDataDir) {
    throw new Error("Electron app userData path is unavailable.");
  }
  return path.join(userDataDir, USER_SKILLS_DIR_NAME);
}

function getBundledExampleSkillDir() {
  return path.resolve(__dirname, "../../../skills/example-user-skill");
}

async function ensureUserSkillsDir(electronApp) {
  const skillsDir = getUserSkillsDir(electronApp);
  await fsPromises.mkdir(skillsDir, { recursive: true });
  return skillsDir;
}

async function ensureExampleSkill(electronApp) {
  const skillsDir = await ensureUserSkillsDir(electronApp);
  const dirEntries = await fsPromises.readdir(skillsDir, { withFileTypes: true });
  if (dirEntries.length > 0) return skillsDir;

  const sourceDir = getBundledExampleSkillDir();
  const targetDir = path.join(skillsDir, EXAMPLE_SKILL_DIR_NAME);
  try {
    await fsPromises.access(sourceDir);
    // fs.cp is experimental in some node versions, using synchronous version for stability in bridge context
    // or we can use async if node version is guaranteed
    await fsPromises.cp(sourceDir, targetDir, { recursive: true, force: false, errorOnExist: false });
  } catch (err) {
    // sourceDir not found, skip
  }
  return skillsDir;
}

async function scanUserSkills(electronApp) {
  const skillsDir = await ensureExampleSkill(electronApp);
  const dirEntries = await fsPromises.readdir(skillsDir, { withFileTypes: true });
  const skills = [];
  const warnings = [];

  for (const entry of dirEntries) {
    if (!entry.isDirectory()) continue;

    const dirName = entry.name;
    const skillDir = path.join(skillsDir, dirName);
    const skillPath = path.join(skillDir, "SKILL.md");
    const baseItem = {
      id: dirName,
      slug: slugifySkill(dirName),
      directoryName: dirName,
      directoryPath: skillDir,
      skillPath,
      name: dirName,
      description: "",
      status: "warning",
      warnings: [],
    };

    try {
      await fsPromises.access(skillPath);
    } catch {
      baseItem.warnings.push("Missing SKILL.md");
      warnings.push(`${dirName}: Missing SKILL.md`);
      skills.push(baseItem);
      continue;
    }

    const stat = await fsPromises.stat(skillPath);
    if (stat.size > MAX_SKILL_BYTES) {
      baseItem.warnings.push(`SKILL.md is too large (${stat.size} bytes > ${MAX_SKILL_BYTES} bytes).`);
      warnings.push(`${dirName}: SKILL.md is too large.`);
      skills.push(baseItem);
      continue;
    }

    const content = await fsPromises.readFile(skillPath, "utf8");
    const { attributes, body, hasFrontmatter } = parseFrontmatter(content);
    const name = stripQuotes(attributes.name || "").trim();
    const description = stripQuotes(attributes.description || "").trim();

    if (!hasFrontmatter) {
      baseItem.warnings.push("Missing YAML frontmatter.");
    }
    if (!name) {
      baseItem.warnings.push("Missing frontmatter field: name.");
    }
    if (!description) {
      baseItem.warnings.push("Missing frontmatter field: description.");
    } else if (description.length > MAX_DESCRIPTION_LENGTH) {
      baseItem.warnings.push(`Description is too long (${description.length} chars > ${MAX_DESCRIPTION_LENGTH}).`);
    }

    if (baseItem.warnings.length > 0) {
      warnings.push(...baseItem.warnings.map((warning) => `${dirName}: ${warning}`));
      skills.push({
        ...baseItem,
        name: name || dirName,
        description,
      });
      continue;
    }

    skills.push({
      ...baseItem,
      slug: slugifySkill(name || dirName),
      name,
      description,
      status: "ready",
      warnings: [],
      body,
      mtimeMs: stat.mtimeMs,
    });
  }

  const readyCount = skills.filter((skill) => skill.status === "ready").length;
  const warningCount = skills.filter((skill) => skill.status === "warning").length;

  return {
    directoryPath: skillsDir,
    readyCount,
    warningCount,
    skills: skills.map((skill) => ({
      id: skill.id,
      slug: skill.slug,
      directoryName: skill.directoryName,
      directoryPath: skill.directoryPath,
      skillPath: skill.skillPath,
      name: skill.name,
      description: skill.description,
      status: skill.status,
      warnings: skill.warnings,
    })),
    warnings,
    _readySkills: skills.filter((skill) => skill.status === "ready"),
  };
}

function scoreSkillMatch(prompt, skill) {
  const lowerPrompt = String(prompt || "").toLowerCase();
  const lowerName = String(skill.name || "").toLowerCase();
  const lowerDirName = String(skill.directoryName || "").toLowerCase();
  const lowerSlug = String(skill.slug || "").toLowerCase();
  if (
    (lowerName && lowerPrompt.includes(lowerName)) ||
    (lowerDirName && lowerPrompt.includes(lowerDirName)) ||
    (lowerSlug && lowerPrompt.includes(`/${lowerSlug}`))
  ) {
    return 100;
  }

  const promptTokens = new Set(tokenize(prompt));
  const skillTokens = new Set(tokenize(`${skill.name} ${skill.description}`));
  let overlap = 0;
  for (const token of skillTokens) {
    if (promptTokens.has(token)) overlap += 1;
  }
  return overlap;
}

async function buildUserSkillsContext(electronApp, prompt, selectedSkillSlugs = []) {
  const status = await scanUserSkills(electronApp);
  const readySkills = status._readySkills || [];
  const trimmedPrompt = String(prompt || "").trim();
  if (readySkills.length === 0) {
    return { context: "", status };
  }

  const indexSkills = readySkills.slice(0, MAX_INDEX_SKILLS);
  const remainingCount = Math.max(readySkills.length - indexSkills.length, 0);

  const indexLine = indexSkills
    .map((skill) => `${skill.name}: ${skill.description}`)
    .join("; ");

  const explicitSlugs = new Set(
    (Array.isArray(selectedSkillSlugs) ? selectedSkillSlugs : [])
      .map((slug) => slugifySkill(slug))
      .filter(Boolean),
  );

  const explicitSkills = readySkills.filter((skill) => explicitSlugs.has(skill.slug));

  const matchedSkills = readySkills
    .filter((skill) => !explicitSlugs.has(skill.slug))
    .map((skill) => ({ skill, score: scoreSkillMatch(trimmedPrompt, skill) }))
    .filter((entry) => entry.score >= 2)
    .sort((left, right) => right.score - left.score)
    .slice(0, MAX_MATCHED_SKILLS)
    .map((entry) => entry.skill);

  const finalSkills = [...explicitSkills, ...matchedSkills].slice(0, MAX_MATCHED_SKILLS);

  const parts = [
    "User-managed skills are installed in Netcatty.",
    `Available user skills: ${indexLine}${remainingCount > 0 ? `; and ${remainingCount} more.` : "."}`,
    "Use a user-managed skill only when it clearly matches the current request.",
  ];

  if (finalSkills.length > 0) {
    parts.push("Matched user-managed skills for this request:");
    for (const skill of finalSkills) {
      const body = String(skill.body || "").trim().slice(0, MAX_MATCHED_SKILL_CHARS);
      parts.push(`### ${skill.name}\n${body}`);
    }
  }

  return {
    context: parts.join("\n\n"),
    status,
  };
}

module.exports = {
  USER_SKILLS_DIR_NAME,
  getUserSkillsDir,
  ensureUserSkillsDir,
  ensureExampleSkill,
  scanUserSkills,
  buildUserSkillsContext,
};
