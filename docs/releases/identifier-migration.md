## Application identifier change

This release presents Natcatty as `com.mslxl.natcatty` on every platform. The
operating system treats it as a different app, so the upgrade resets a few
things once:

- Quit the previous build before installing.
- Grant permissions again. On macOS, notifications are asked again.
- Re-enter every orphaned credential: saved passwords and passphrases, and
  each EternalTerminal resume identity and key. They remain in the system
  credential store under the old name, but this build cannot read them.
- Remove the stale entries the old identity left behind. On macOS, delete the
  old Start at login entry if System Settings shows two. On Windows, remove the
  old Apps & Features entry and the old Run value under
  `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`.

The Vault, Hosts, settings, Sessions and window layout live under the unchanged
application name and survive untouched.
