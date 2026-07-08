{
  mkShell,
  go,
  gopls,
  gotools,
  go-tools,
  kind,
  kubectl,
  kubernetes-helm,
}:

mkShell {
  name = "harbor-labeler-dev";

  packages = [
    # Go development
    go
    gopls
    gotools
    go-tools
    # e2e tests (e2e/run.sh)
    kind
    kubectl
    kubernetes-helm
  ];

  shellHook = ''
      echo "harbor-labeler dev shell — go $(go version | cut -d' ' -f3)"
      echo "  go test ./...              # run tests (no cluster needed)"
      echo "  go build ./cmd/harbor-labeler"
      echo "  nix build                  # binary"
      echo "  nix build .#image          # OCI image (stream, pipe into docker load)"
    echo "  helm template test ./chart # render chart"
    echo "  ./e2e/run.sh               # full e2e (kind + Harbor, needs docker)"
      echo "  nix fmt"
  '';

  # Set Go environment variables
  GOOS = "linux";
  GOARCH = "amd64";
  CGO_ENABLED = "0";
}
