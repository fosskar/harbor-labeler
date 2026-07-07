{
  projectRootFile = "treefmt.nix";

  # See https://github.com/numtide/treefmt-nix#supported-programs
  programs.gofmt.enable = true;
  programs.yamlfmt.enable = true;
  # Helm templates are not valid YAML before rendering.
  settings.formatter.yamlfmt.excludes = [ "chart/templates/*" ];
  programs.mdformat.enable = true;
  programs.nixfmt.enable = true;
}
