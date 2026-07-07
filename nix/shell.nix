{
  lib,
  mkShell,
  go,
  gopls,
  gotools,
  go-tools,
}:

mkShell {
  name = "harbor-labeler-dev";

  packages = [
    # Go development
    go
    gopls
    gotools
    go-tools
  ];

  shellHook = ''
    echo "harbor-labeler dev shell — go $(go version | cut -d' ' -f3)"
    echo "  go test ./...              # run tests (no cluster needed)"
    echo "  go build ./cmd/harbor-labeler"
    echo "  nix build                  # binary"
    echo "  nix build .#image          # OCI image (stream, pipe into docker load)"
    echo "  helm template test ./chart # render chart"
    echo "  nix fmt"
  '';

  # Set Go environment variables
  GOOS = "linux";
  GOARCH = "amd64";
  CGO_ENABLED = "0";
}
