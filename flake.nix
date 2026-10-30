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

          vendorHash = "sha256-gneMnxYUjfOZd4doKA+NZ7NK+fM7GOKv5kNSqHSPBoI=";
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
