{
  description = "kubectl-schedule — kubectl plugin that simulates Pod scheduling onto a specific Node";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } (
      let
        version = "0.1.1";
      in
      {
        systems = [
          "x86_64-linux"
          "aarch64-linux"
          "x86_64-darwin"
          "aarch64-darwin"
        ];

        flake.overlays.default = final: _prev: {
          kubectl-schedule = final.callPackage ./package.nix { inherit version; };
        };

        perSystem =
          { pkgs, config, ... }:
          {
            packages.kubectl-schedule = pkgs.callPackage ./package.nix { inherit version; };
            packages.default = config.packages.kubectl-schedule;

            devShells.default = pkgs.mkShell {
              packages = [
                pkgs.go_1_26
                pkgs.gopls
                pkgs.gotools
              ];
            };

            formatter = pkgs.nixfmt-rfc-style;
          };
      }
    );
}
