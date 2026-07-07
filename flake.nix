{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    treefmt-nix = {
      url = "github:numtide/treefmt-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      treefmt-nix,
      ...
    }:
    let
      inherit (nixpkgs) lib;
      eachSystem =
        f: lib.genAttrs [ "x86_64-linux" "aarch64-linux" ] (system: f nixpkgs.legacyPackages.${system});

      treefmtEval = eachSystem (pkgs: treefmt-nix.lib.evalModule pkgs ./nix/treefmt.nix);
    in
    {
      # Build executables. See https://nixos.org/manual/nixpkgs/stable/#sec-language-go
      packages = eachSystem (pkgs: rec {
        default = pkgs.callPackage ./nix/package.nix { src = self.outPath; };
        image = pkgs.callPackage ./nix/image.nix { harbor-labeler = default; };
      });

      devShells = eachSystem (pkgs: {
        default = pkgs.callPackage ./nix/devshell.nix { };
      });

      formatter = eachSystem (pkgs: treefmtEval.${pkgs.stdenv.hostPlatform.system}.config.build.wrapper);

      checks = eachSystem (pkgs: {
        formatting = treefmtEval.${pkgs.stdenv.hostPlatform.system}.config.build.check self;
        # surface the package (buildGoModule check phase runs go tests) and
        # image in `nix flake check` so nixbot builds them on every push
        inherit (self.packages.${pkgs.stdenv.hostPlatform.system}) default image;
        chart = pkgs.runCommand "helm-lint" { nativeBuildInputs = [ pkgs.kubernetes-helm ]; } ''
          helm lint ${self.outPath}/chart --strict
          helm template test ${self.outPath}/chart > /dev/null
          touch $out
        '';
      });

      # nixbot scheduled effects
      effects = import ./nix/effects.nix { pkgs = nixpkgs.legacyPackages.x86_64-linux; };
    };
}
