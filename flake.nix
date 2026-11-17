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
          src = ./.;

          vendorHash = "sha256-ppz3enfI1XVqZLeWjktZTeUTKjfwcrQHyZX67E0NIFM=";
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
