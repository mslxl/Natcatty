## Android app (APK)

The Android app is the `natcatty-…-android.apk` asset attached to this
release. It is installed from the file, not from a store: open the APK on the
device and allow installs from this source, or run
`adb install natcatty-…-android.apk`. No Google Play account is involved, and
there is no store update channel; install the next release's APK over the
installed app to update it in place.

The APK is universal: it carries the `arm64-v8a`, `armeabi-v7a` and `x86_64`
native libraries, and every library is built for 16 KB pages. It is signed
with natcatty's own release key.
