{
  description = "Natcatty Native";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];

      forAllSystems = nixpkgs.lib.genAttrs systems;

      mkPackage =
        pkgs:
        let
          buildGoModule =
            if pkgs ? buildGo127Module then pkgs.buildGo127Module else pkgs.buildGoModule;
        in
        buildGoModule {
          pname = "natcatty";
          version = "0.1.0";
          src = ./.;

          vendorHash = "sha256-Xargyu4VctmWLM2SDAq5kMenUtpd95rMU5boscDHwwk=";
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
          go = if pkgs ? go_1_27 then pkgs.go_1_27 else pkgs.go;
          # Plugin language toolchains. The flake.lock pins nixpkgs and these
          # attributes pin the exact majors the SDK supports:
          #   TinyGo 0.43.0, Zig 0.16.0, clang 21.1.8, wasm-ld/LLD 21.1.8,
          #   binaryen 133 (wasm-opt). Do not replace these with host binaries.
          languageToolchains = [
            pkgs.tinygo
            pkgs.zig_0_16
            pkgs.clang
            pkgs.lld
            pkgs.binaryen
          ];
          # The cc-wrapper injects macOS hardening flags such as
          # -fzero-call-used-regs, which wasm32 rejects. The samples use the
          # same pinned LLVM without the wrapper; the wrapper still serves
          # native builds.
          clangUnwrapped = pkgs.llvmPackages.clang-unwrapped;
        in
        {
          default = pkgs.mkShell {
            packages = [
              go
              pkgs.gopls
              pkgs.just
            ] ++ languageToolchains;
            env = {
              CGO_ENABLED = "0";
              NATCATTY_CLANG = "${clangUnwrapped}/bin/clang";
              NATCATTY_WASM_LD = "${pkgs.lld}/bin/wasm-ld";
              # A plain `nix develop` is the documented place to run the
              # language-SDK build test, so a missing pinned tool fails it.
              NATCATTY_REQUIRE_LANGUAGE_SDKS = "1";
            };
            # The language samples build with the pinned tools above. These
            # versions are part of the SDK contract documented in sdk/README.md.
            shellHook = ''
              echo "natcatty plugin toolchains: $(tinygo version | cut -d' ' -f3), zig $(zig version), $(clang --version | head -n1)"
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
