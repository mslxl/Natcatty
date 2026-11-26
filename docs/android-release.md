# Android APK releases

Natcatty's Android app is distributed as an APK attached to a GitHub release.
There is no store account and no store update channel: a user installs the APK
from the file (`adb install`, or opening it on the device with installs from
that source allowed) and updates by installing the next release's APK over the
installed app. The release notes say the same.

## What a release builds

`go tool mygo build -platform android` builds the Go library for every ABI in
`scripts/mobile/android-abis.txt` (`arm64-v8a`, `armeabi-v7a`, `x86_64`), then
Gradle packages and signs one universal APK. The release workflow deletes the
`.aab` Google Play bundle: the APK is the artifact this project hands out.

Before packaging, each library is linked with
`-Wl,-z,max-page-size=16384`. The release job verifies the output rather than
trusting the flag:

- `zipalign -c -P 16 4` accepts the APK, and every `lib/<abi>/libmygoapp.so`
  entry is stored uncompressed.
- Extracting each library and running `llvm-readelf -l` shows every `LOAD`
  segment aligned to at least 16384 bytes.
- The APK contains `lib/<abi>/libmygoapp.so` for every ABI in the shared list.

An APK is typically around 130–140 MB because it carries all three ABIs.

## Signing material and secrets

The checkout contains no keystore and `mygo.json` names none. The release job
decodes the keystore from a secret into `$RUNNER_TEMP`, outside the checkout,
and writes its path, alias and version code into the disposable CI manifest.
Required repository secrets:

| Secret | Meaning |
| --- | --- |
| `ANDROID_KEYSTORE_BASE64` | The keystore file, base64-encoded (no signing material in the repository). |
| `ANDROID_KEYSTORE_PASSWORD` | The store password. MyGo passes it as `MYGO_ANDROID_KEYSTORE_PASSWORD`. |
| `ANDROID_KEY_ALIAS` | The key alias inside the keystore. |
| `ANDROID_KEY_PASSWORD` | Optional. The key password; when unset MyGo uses the store password. Leave it unset for a PKCS12 keystore (PKCS12 uses one password). |

The keystore is generated outside the repository and backed up outside it. For
a new key:

```sh
keytool -genkeypair -v \
  -keystore natcatty-release.keystore -storetype PKCS12 \
  -alias natcatty -keyalg RSA -keysize 4096 -validity 10000 \
  -dname 'CN=Natcatty Release, O=Natcatty, C=US'

# base64 for the ANDROID_KEYSTORE_BASE64 secret (GNU coreutils):
base64 -w0 natcatty-release.keystore
# macOS:
base64 -i natcatty-release.keystore | tr -d '\n'
```

Store the keystore and both passwords in a password manager and on an offline
medium held by the project owner, never in the repository, a CI artifact, or a
release asset. A PKCS12 keystore ignores a separate `-keypass`; use the same
value for the store and key or use a JKS keystore and set
`ANDROID_KEY_PASSWORD`.

## The keystore cannot be replaced

Android identifies an installed app by its `applicationId`
(`com.mslxl.natcatty`) and refuses an update signed by a different key. If the
release keystore is lost:

- an APK signed with a new key cannot install over an existing install;
- users must uninstall the app, losing its local data, before installing the
  newly signed APK;
- there is no recovery, no key escrow, and no way to re-sign the old APK.

The backup is therefore the only copy that matters. It lives outside the
repository. Deleting the secret or losing the offline copy is unrecoverable.

## Version code

Android orders installs by the integer `versionCode`, not by the version name.
`internal/tools/androidversion` maps the SemVer manifest version to a strictly
increasing code:

```
MAJOR * 100000000 + MINOR * 1000000 + PATCH * 10000 + SUFFIX
```

`SUFFIX` is `9999` for a final release and `RANK * 1000 + N` for a prerelease
`<stage>.<N>`, with `alpha=0`, `beta=1`, `rc=2`. SemVer orders these stages,
and the mapping preserves that order numerically:

| Release | versionCode |
| --- | ---: |
| `0.1.0-beta.1` | 1001001 |
| `0.1.0-beta.2` | 1001002 |
| `0.1.0-rc.1` | 1002001 |
| `0.1.0` | 1009999 |
| `0.1.1-beta.1` | 1011001 |
| `0.1.1` | 1019999 |
| `0.2.0` | 2009999 |

MAJOR is capped at 20 so every code stays below Android's 2100000000 limit.
The validate job computes the code and compares it with the previous release
tag's manifest, so a release whose code would not increase fails before
anything is built. `internal/tools/androidversion` has a table-driven test
over these successive releases.

## Development builds

A developer build needs no account and no keystore:

```sh
nix develop .#android --command bash -c 'go tool mygo build -platform android -o dist'
```

With no `android.keystore` in `mygo.json`, MyGo signs with the Android debug
key (Gradle creates `~/.android/debug.keystore` if needed) and warns that the
APK is debug-signed. Such an APK installs only as a debug build; it is never
published. The repository's committed `mygo.json` is checked by the release
workflow: a committed `.jks`, `.keystore`, `.p12`, `.pfx`, or a committed
`android.keystore` path fails the release.

## What the workflow proves, and what only the runner can

Locally the same commands were run with a throwaway keystore generated under
`/tmp`:

- `apkanalyzer manifest version-code` reports the computed code;
- `zipalign -c -P 16` and the per-ABI `llvm-readelf` check pass;
- a debug build with no keystore is debug-signed and is never published.

A live GitHub Actions run is not possible on this host, so the workflow's YAML
is checked structurally and the commands above are run locally. The job builds
the three libghostty-vt archives from the pinned Ghostty commit and Zig 0.16.0
with `scripts/mobile/build-ghostty-vt.sh`; the local build used archives from
that same builder. The prebuilt release-asset download path was not used: the
release named in `ui/terminal/artifacts.json` is not published (its assets
return 404), which is why the job builds from source instead of downloading.
