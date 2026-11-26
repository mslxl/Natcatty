# The release ships an unsigned iOS device package

Every release attaches `natcatty-<version>-ios-unsigned.ipa`: the arm64 device
app built with MyGo's `-unsigned` mode (patch 0009), carrying no signature and
no provisioning profile. The installer signs it on their own machine with
AltStore, Sideloadly or Xcode and their own Apple ID, which is the only
no-account path to running Natcatty on an iPhone. The release workflow verifies
that the package is device-targeted and unsigned before attaching it, and the
release body says the file must be signed before it can be installed.

The simulator app can still be built locally for someone who only wants to
look at the app. A signed device package would need a paid Apple developer
account and a job of its own; CI produces neither.

**Status**: accepted

**Considered Options**:
- Simulator app only: rejected as the sole iOS artifact. It cannot run on a
  phone, and the users who ask for an iOS build have a device.
- A paid Apple developer account only: keeps the store-free signing model but
  leaves everyone without an account unable to install anything.
- Unsigned IPA for a sideloading tool (chosen).

**Consequences**:
- ADR-0024's rule against publishing an unsigned package is about an
  installable package. This asset is a build input for the installer's signing
  tool, not something anyone installs as downloaded, and the release body
  describes it that way.
- A free Apple ID's signature expires after seven days; the project cannot
  refresh it.
- The IPA is a release asset like any other: it is collected and checksummed
  with the installers, so `scripts/release.nu collect` recognizes `.ipa`.
