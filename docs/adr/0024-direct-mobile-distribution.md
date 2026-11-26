# Phone builds are distributed directly, under the desktop's identifier

Every platform presents one package identifier, `com.mslxl.natcatty`. MyGo has
exactly one identifier field, so the desktop bundle is re-identified once,
before the first phone milestone. The Android artifact is one universal APK
attached to a GitHub release, signed with a project-owned keystore supplied only
from repository secrets. It is installed from the file, not from a store, and a
newer release installs over the old one because `versionCode` increases
monotonically. The iOS artifact is an unsigned device package attached to each
release (ADR-0027); no simulator app or signed device package is produced by
CI. There is no Play Console, no App Store, no Play App Signing, and no
TestFlight.

**Status**: accepted

**Considered Options**:
- Store distribution: rejected for v1. It needs store accounts and review, and
  Play App Signing would take custody of the signing key for an app with no
  store listing.
- A per-platform identifier: rejected. MyGo has one identifier field, so this
  needs a different generated `mygo.json` per target and leaves two identities
  forever, to save a one-time migration.
- Keep `io.github.mslxl.natcatty`: rejected. The project wants its own
  reverse-DNS name, and the change never gets cheaper than while the installed
  base is a beta.
- Direct distribution under one identifier (chosen).

**Consequences**:
- Desktop users get one reset: quit the old build, re-grant permissions, and
  re-enter saved passwords, passphrases, and ET resume identities and keys.
  File-based state (Vault, Hosts, settings, Sessions, window layout) survives.
  The changelog and the first release body say so.
- Android upgrades are manual installs of the next APK. There is no store-level
  rollback or staged rollout.
- Signing material never enters the checkout. The release job decodes
  `ANDROID_KEYSTORE_BASE64` into `$RUNNER_TEMP`; `ANDROID_KEYSTORE_PASSWORD`,
  `ANDROID_KEY_ALIAS` (and optionally `ANDROID_KEY_PASSWORD`) are required, and
  the job fails closed when one is missing.
- The keystore is generated once and backed up outside the repository. Android
  identifies an installed app by its applicationId plus its signing key, so
  losing the key makes a newly signed APK uninstallable over the old one; the
  loss is unrecoverable.
- The release job verifies the APK's computed `versionCode`, every ABI in
  `scripts/mobile/android-abis.txt`, and 16 KB `LOAD` alignment before
  publishing.
- iOS device signing is not produced by CI. The unsigned package attached to
  every release is a re-signing input, not an installable app; ADR-0027 covers
  it. A signed package would need a paid Apple developer account plus
  `APPLE_CERTIFICATE_P12`, `APPLE_CERTIFICATE_PASSWORD` and
  `APPLE_PROVISIONING_PROFILE` in a job of its own.
