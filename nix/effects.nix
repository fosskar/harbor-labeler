# nixbot effect pipeline; same plumbing as nixfiles modules/flake-parts/effects.nix.
{ pkgs, nixbot }:
let
  inherit (nixbot.lib.effects { inherit pkgs; }) mkEffect;
in
_args: {
  onSchedule.update-flake-inputs = {
    when = {
      hour = 0;
      minute = 0;
    };
    # nixbot mounts a pushable clone of the effect's commit at
    # $NIXBOT_EFFECT_CHECKOUT (also the working directory) with an
    # authenticated `origin`. The GitToken is a github app installation token,
    # so it serves the direct github API calls too.
    outputs.effects.update-flake-inputs = mkEffect {
      name = "effect-update-flake-inputs";
      checkout = true;
      inputs = [
        pkgs.git
        pkgs.nix
      ];
      secretsMap.git.type = "GitToken";
      effectScript = ''
        set -euo pipefail
        token=$(jq -re '.git.data.token' "$HERCULES_CI_SECRETS_JSON")
        export FORGE_TOKEN="$token"
        export GITHUB_TOKEN="$token"
        export NIX_CONFIG="experimental-features = nix-command flakes
        access-tokens = github.com=$token"

        git config --global user.name 'fosskar[bot]'
        git config --global user.email '300917551+fosskar[bot]@users.noreply.github.com'

        nix run "github:fosskar/nixfiles#updater-flake-inputs"
      '';
    };
  };
}
