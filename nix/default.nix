{
  pkgs ? import <nixpkgs> { },
}:

let
  src = ../.;
in
{
  package = pkgs.callPackage ./package.nix { inherit src; };

  image = pkgs.callPackage ./image.nix {
    harbor-labeler = pkgs.callPackage ./package.nix { inherit src; };
  };

  shell = pkgs.callPackage ./shell.nix { };
}
