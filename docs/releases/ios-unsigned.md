## iOS app (unsigned IPA)

The `natcatty-…-ios-unsigned.ipa` asset is the iOS app for a real device. It
is deliberately unsigned and carries no provisioning profile, because it is
not tied to any Apple developer account. Install it with a sideloading tool
(AltStore, Sideloadly, or Xcode) that signs it with your own Apple ID and
profile; the package cannot be installed as downloaded.

The IPA is an arm64 device build (`platform IOS`). It shares the Vault, Hosts
and Sessions with the desktop build. As with any sideloaded app, a free Apple
ID's signature expires after seven days and must be refreshed, and the device
limits how many sideloaded apps it keeps at once.
