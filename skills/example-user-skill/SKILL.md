---
name: example-user-skill
description: Use when you want a small template for writing a reusable skill with a short main guide and optional references.
---

# Example Skill

This is a template skill for users who want to add their own skills to Netcatty.

Copy this directory, rename it, and then replace the example rules with your own domain-specific instructions.

## Router

1. Classify the task before acting.
2. Use the shortest reliable path for routine work.
3. Read a reference file only when the main rules are not enough.
4. Prefer concrete commands, tools, or steps over vague advice.

## Core Rules

- Keep this file short. Put only the rules that should be read every time here.
- Put heavy detail, examples, edge cases, or long checklists in `references/`.
- State what to do before stating exceptions.
- Prefer explicit inputs and outputs.
- If a task has both a short path and a long-running path, say when to use each one.
- If a tool or command is required, name it exactly.
- If something is forbidden, say so directly.

## Template Pattern

Use a pattern like this when adapting the skill:

- Scope: what this skill is for.
- Router: how to classify the request.
- Core rules: the default path, plus hard constraints.
- References: deeper instructions for specific task types.

## References

- Worked examples: `references/examples.md`
