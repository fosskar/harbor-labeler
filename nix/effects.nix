# nixbot effect pipeline; same plumbing as nixfiles modules/flake-parts/effects.nix.
{ pkgs }:
let
  forgeHost = "github.com";
  repo = "fosskar/harbor-labeler";

  # Shared plumbing for every repo-mutating scheduled effect: request
  # nixbot's forge token (GitToken), clone with it, then run command. git
  # redacts credentials from URLs in its output, so the token stays out of
  # the public effect log.
  mkRepoEffect =
    name: command:
    pkgs.runCommand "effect-${name}"
      {
        nativeBuildInputs = [
          pkgs.cacert
          pkgs.git
          pkgs.jq
          pkgs.nix
        ];
        # The GitToken is already a github (app installation) token, so the
        # nixfiles "github-api" secret is not needed here.
        secretsMap = builtins.toJSON { git.type = "GitToken"; };
        HOME = "/build";
      }
      ''
        set -euo pipefail
        token=$(jq -re '.git.data.token' "$HERCULES_CI_SECRETS_JSON")
        export FORGE_TOKEN="$token"
        export GITHUB_TOKEN="$token"
        export NIX_CONFIG="experimental-features = nix-command flakes
        access-tokens = github.com=$token"

        git config --global user.name 'fosskar[bot]'
        git config --global user.email '300917551+fosskar[bot]@users.noreply.github.com'
        git config --global safe.directory '*'

        git clone --depth 1 --progress "https://oauth2:$token@${forgeHost}/${repo}.git" repo
        cd repo

        ${command}
      '';
in
_args: {
  onSchedule.update-flake-inputs = {
    when = {
      hour = 4;
      minute = 0;
    };
    outputs.effects.update-flake-inputs = mkRepoEffect "update-flake-inputs" ''
      nix run "github:fosskar/nixfiles#updater-flake-inputs"
    '';
  };
}
