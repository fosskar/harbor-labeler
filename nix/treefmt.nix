{
  projectRootFile = "flake.nix";

  # See https://github.com/numtide/treefmt-nix#supported-programs
  # go
  programs.gofmt.enable = true;

  # nix: format + lint (dead code, anti-patterns)
  programs.nixfmt.enable = true;
  programs.deadnix.enable = true;
  programs.statix.enable = true;

  # yaml
  programs.yamlfmt.enable = true;
  # Helm templates are not valid YAML before rendering.
  settings.formatter.yamlfmt.excludes = [ "chart/templates/*" ];

  # markdown
  programs.mdformat.enable = true;
}
