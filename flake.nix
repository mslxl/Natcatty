{
  description = "Natcatty Native";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  # The same MyGo release the third_party/mygo submodule pins. A submodule's
  # contents are not part of a git flake's source, so the package takes the
  # tree from this input and overlays it in place of the empty gitlink.
  inputs.mygo = {
    url = "github:egoist/mygo/v0.10.0";
    flake = false;
  };

  outputs =
    { self, nixpkgs, mygo }:
    let
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];

      forAllSystems = nixpkgs.lib.genAttrs systems;

      # nixpkgs still packages Go 1.27.3. This module needs 1.27.4: the
      # in-process Tailscale provider is pinned at v1.104.7, whose go.mod
      # requires Go >= 1.27.4, and 1.27.4 is the first release with the net/http
      # fix for GO-2026-6859. Build that patch release from the official source
      # until nixpkgs ships it, then this falls back to the packaged Go.
      goFor =
        pkgs:
        let
          packaged = if pkgs ? go_1_27 then pkgs.go_1_27 else pkgs.go;
        in
        if nixpkgs.lib.versionAtLeast packaged.version "1.27.4" then
          packaged
        else
          packaged.overrideAttrs (_: _: {
            version = "1.27.4";
            src = pkgs.fetchurl {
              url = "https://go.dev/dl/go1.27.4.src.tar.gz";
              hash = "sha256-c8+4VxrsDAEXdofbhxY9Rqog6XeWcncokaoXZU2aYJk=";
            };
          });

      mkPackage =
        pkgs:
        let
          buildGoModule =
            (if pkgs ? buildGo127Module then pkgs.buildGo127Module else pkgs.buildGoModule).override {
              go = goFor pkgs;
            };
        in
        buildGoModule {
          pname = "natcatty";
          version = "0.1.0";
          # The submodule is a gitlink here, not content: supply the pinned
          # MyGo tree from the flake input and apply the same patches the
          # checkout applies, so the package builds the phone host natcatty
          # actually ships.
          src = pkgs.runCommand "natcatty-src" { } ''
            cp -R ${./.} $out
            chmod -R u+w $out
            rm -rf $out/third_party/mygo
            cp -R ${mygo} $out/third_party/mygo
            chmod -R u+w $out/third_party/mygo
            for p in $out/patches/mygo/*.patch; do
              patch -p1 -d $out/third_party/mygo < "$p"
            done
          '';

          vendorHash = "sha256-g5V3GngaPq1/PGlYYY1aRA1coYVGRlX25UYs2NZme18=";
          subPackages = [ "." ];
          env.CGO_ENABLED = "0";
          ldflags = [
            "-s"
            "-w"
          ];

          postInstall = ''
            # MyGo resolves its manifest and transport clients beside the executable.
            cp mygo.json CHANGELOG.md "$out/bin/"
            cp -R resources "$out/bin/"
          '';

          meta = {
            description = "Native desktop terminal and connection manager";
            homepage = "https://github.com/mslxl/natcatty";
            mainProgram = "natcatty";
            platforms = systems;
          };
        };

      packages = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          package = mkPackage pkgs;
        in
        {
          default = package;
          natcatty = package;
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          go = goFor pkgs;

          # The SDK licenses are accepted globally for this shell only; the
          # default shell keeps unconfigured nixpkgs.
          androidPkgs = import nixpkgs {
            inherit system;
            config = {
              android_sdk.accept_license = true;
              allowUnfree = true;
            };
          };

          # A writable AVD home is resolved in the shell hook: the SDK itself
          # comes from the nix store and is read-only, but AVDs must be created
          # at runtime.

          androidPackages = androidPkgs.androidenv.composeAndroidPackages {
            platformVersions = [ "36" ];
            # AGP 8.12, which MyGo's Gradle host uses, asks for Build-Tools
            # 35.0.0 by default; keeping 37.0.0 as well leaves the newer
            # tools available without changing what Gradle resolves.
            buildToolsVersions = [ "35.0.0" "37.0.0" ];
            # Emulator ABIs. `armeabi-v7a` is a Go/NDK target name, not an
            # emulator image ABI, so only the emulator ABI is listed here.
            abiVersions = [ "arm64-v8a" ];
            systemImageTypes = [ "google_apis" ];
            includeSystemImages = true;
            includeEmulator = true;
            # cgo builds for Android need the NDK. androidenv exposes it as
            # `ndk-bundle` (which is why `androidPkgs.ndk` does not exist) and
            # links it into the SDK as both `ndk/<version>` and `ndk-bundle`.
            includeNDK = true;
            extraLicenses = [
              "android-sdk-license"
              "android-sdk-preview-license"
            ];
          };
          androidSdk = androidPackages.androidsdk;
        in
        {
          default = pkgs.mkShell {
            packages = [
              go
              pkgs.gopls
              pkgs.just
            ];
            env.CGO_ENABLED = "0";
          };

          # iOS needs no packaged SDK: Xcode is the toolchain, and nix only
          # supplies Go. DEVELOPER_DIR pins the Xcode the simulator tools must
          # come from.
          ios = pkgs.mkShell {
            packages = [
              go
              pkgs.just
            ];
            env = {
              CGO_ENABLED = "1";
            };
            shellHook = ''
              # nixpkgs' xcbuild setup hook points DEVELOPER_DIR and SDKROOT at
              # the packaged macOS SDK, which makes xcrun unable to find the
              # iphonesimulator SDK. Phone work must use the host Xcode, so
              # override both after the setup hook has run.
              unset SDKROOT
              export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
              echo "iOS dev shell — Xcode: $(xcodebuild -version 2>/dev/null | head -1)"
              echo "boot a simulator: scripts/dev/ios-boot.sh   stop: scripts/dev/ios-stop.sh"
            '';
          };

          # Android SDK, emulator and a system image come from nix so every
          # machine gets the same toolchain. AVDs live outside the store.
          android = pkgs.mkShell {
            packages = [
              go
              pkgs.just
              # MyGo's Gradle host asks for a Java 17 toolchain in every
              # module (jvmToolchain(17)); Gradle matches that request by
              # language version, so a newer JDK alone cannot satisfy it.
              androidPkgs.jdk17
              androidSdk
            ];
            env = {
              CGO_ENABLED = "1";
              ANDROID_HOME = "${androidSdk}/libexec/android-sdk";
              ANDROID_SDK_ROOT = "${androidSdk}/libexec/android-sdk";
              # Point cgo at the NDK linked into the SDK above; the version is
              # resolved from the same derivation, so it never drifts.
              ANDROID_NDK_HOME = "${androidSdk}/libexec/android-sdk/ndk/${androidPackages.ndk-bundle.version}";
              ANDROID_NDK_ROOT = "${androidSdk}/libexec/android-sdk/ndk/${androidPackages.ndk-bundle.version}";
            };
            shellHook = ''
              # AVDs and avdmanager's config live in the checkout, not the
              # read-only nix store, so resolve them from wherever the shell
              # was entered.
              export ANDROID_AVD_HOME="$PWD/.dev/avd"
              export ANDROID_USER_HOME="$PWD/.dev/android-home"
              mkdir -p "$ANDROID_AVD_HOME" "$ANDROID_USER_HOME"
              echo "Android dev shell — $(adb version | head -1)"
              echo "boot an emulator: scripts/dev/android-boot.sh   stop: scripts/dev/android-stop.sh"
            '';
          };
        }
      );

      programModule =
        {
          config,
          lib,
          pkgs,
          ...
        }:
        let
          cfg = config.programs.natcatty;
        in
        {
          options.programs.natcatty = {
            enable = lib.mkEnableOption "Natcatty";
            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.default;
              defaultText = lib.literalExpression "self.packages.\${pkgs.system}.default";
              description = "Natcatty package to install.";
            };
          };

          config = lib.mkIf cfg.enable {
            environment.systemPackages = [ cfg.package ];
          };
        };
    in
    {
      inherit packages devShells;

      nixosModules = {
        default = programModule;
        natcatty = programModule;
      };

      darwinModules = {
        default = programModule;
        natcatty = programModule;
      };
    };
}
