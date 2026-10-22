---
name: implement
description: "Implement a piece of work based on a spec or set of tickets."
disable-model-invocation: false
---

Implement the work described by the user in the spec or tickets.

If the user passes a ticket reference, fetch it from the issue tracker and state its title before starting. If the reference is ambiguous, ask.

Call the Skill tool with "tdd" where possible, at pre-agreed seams.

Run typechecking regularly, single test files regularly, and the full test suite once at the end.

Once done, call the Skill tool with "code-review" to review the work.

Commit your work to the current branch.

## GUI verification conventions

- Never use `sleep` / `Start-Sleep` (or any fixed delay) to wait for the app to settle. Wait on an observable condition with a hard timeout: non-zero `MainWindowHandle` / `WaitForInputIdle`, a result JSON or output file, a UI state or log line, a listening port, or `gh run watch`.
- A poll loop is fine only when every iteration checks a real condition and the loop has a hard deadline; the poll interval is a cadence, not a synchronization assumption.
- In verification notes, state which condition was awaited and its timeout.
