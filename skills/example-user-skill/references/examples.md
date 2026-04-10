# Example Reference

Use this file to store detailed examples and edge cases that would make the main `SKILL.md` noisy.

## Example: Short task

If the task is simple and the required tool is obvious:

1. confirm the target
2. run the single required command or tool
3. report the result directly

Do not add extra planning steps when the shortest path is already clear.

## Example: Long-running task

If the task may take a long time or stream output:

1. start the task
2. poll or watch progress
3. stop it only if the user asks or the workflow requires it

Document this split clearly in the main skill so the agent does not use the short path by mistake.

## Example: File task

If the task is about files:

1. decide whether the path is local or remote
2. choose the file tool that matches that location
3. avoid using a shell command when a dedicated file tool is available

Make path semantics explicit. If your environment uses both local and remote files, define that distinction in plain language.

## Example: What not to do

- Do not write long tutorials in the main `SKILL.md`.
- Do not assume the agent will infer hidden constraints.
- Do not mix unrelated workflows into one reference file.
- Do not leave TODO markers in shipped skill content.
