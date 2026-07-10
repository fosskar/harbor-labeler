{
  lib,
  buildGoModule,
  src,
}:

buildGoModule {
  pname = "harbor-labeler";
  # Single source of truth for the release version; CI reads it via
  # `nix eval --raw .#default.version` and gates publishing on it.
  version = "0.2.0";

  inherit src;

  vendorHash = "sha256-UCvwf2MGpF7PjF5gReWgR1k1Opk0FXyywS0OQvKgWys=";

  subPackages = [ "cmd/harbor-labeler" ];

  # subPackages scopes the check phase too, and cmd/ has no tests — unset it
  # there so `go test ./...` covers internal/labeler (nix build gates on it).
  preCheck = ''
    unset subPackages
  '';

  # Build flags for optimization
  ldflags = [
    "-s"
    "-w"
    "-extldflags=-static"
  ];

  # Disable CGO for static binary
  env.CGO_ENABLED = "0";

  meta = {
    description = "Kubernetes CronJob that labels running container images in Harbor";
    longDescription = ''
      harbor-labeler marks container images in a Harbor registry with a
      running-<cluster> label while they are running in a Kubernetes cluster
      and removes the label once they are not.
    '';
    homepage = "https://github.com/fosskar/harbor-labeler";
    license = lib.licenses.mit;
    platforms = lib.platforms.linux;
    mainProgram = "harbor-labeler";
  };
}
