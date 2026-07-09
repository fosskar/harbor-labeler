# Repository Guidelines

## Project Overview

harbor-labeler is a single-shot Go binary, deployed as a Kubernetes CronJob,
that marks container images in a Harbor registry with a global
`running-<CLUSTER_NAME>` label while they run in the cluster and detaches the
label once they don't. Purpose: make in-use artifacts visible as a guard for
Harbor retention/GC policies. Multi-cluster safe — each cluster only touches
its own label.

## Architecture & Data Flow

Two packages, linear pipeline, no daemon state:

```
cmd/harbor-labeler/main.go        wiring only
  LoadConfig()                    env -> Config (RegistryHost = HARBOR_URL host)
  NewKubeClient()                 in-cluster SA, kubeconfig fallback
  KubeDiscovery.RunningImages()   list all pods -> digest refs, host-filtered
  Reconcile()                     ensure label, attach running, detach stale
```

- `internal/labeler` holds all logic; `main.go` never contains any.
- `Reconcile` depends on the consumer-defined `HarborAPI` interface
  (`reconcile.go`), not the concrete HTTP `*Client` (`harbor.go`) — this is
  the unit-test seam. Keep new Harbor calls behind it. (Matches official Go
  guidance: interfaces belong in the consuming code, not the implementor —
  go.dev/wiki/CodeReviewComments#interfaces.)
- `Run` likewise depends on the consumer-defined `ImageDiscovery` interface
  (`run.go`); `KubeDiscovery` (`kubernetes.go`) is the production adapter.
  Run tests fake discovery with artifact lists — no fake clientset needed.
- Load-bearing invariants:
  - Matching is **by digest, never tag**: discovery reads
    `status.containerStatuses[].imageID` (+ init containers), and only refs
    whose host equals `HARBOR_URL`'s host (port included) count.
  - **Zero-images safety guard**: empty running set aborts before touching
    Harbor (broken discovery must not strip all labels).
  - Per-artifact failures are logged as warnings, aggregated via
    `errors.Join`, and the run continues; the job still exits non-zero.
  - Idempotency at the API layer: `AddLabel` treats 409 as success,
    `RemoveLabel` treats 404 as success, `EnsureGlobalLabel` re-looks-up on
    409 create races.
  - `Client.do` retries 3x on transport errors/5xx only (never 4xx); nested
    repo names are double-encoded (`sub/app` -> `sub%252Fapp`); listings
    paginate with `pageSize = 100`.

## Key Directories

- `cmd/harbor-labeler/` — entry point (wiring only)
- `internal/labeler/` — config, k8s discovery, Harbor client, reconcile; unit
  tests live next to each file
- `e2e/` — real-infrastructure suite (`run.sh` provisions, `e2e_test.go`
  asserts); read `e2e/README.md` for topology and coverage gaps
- `chart/` — Helm chart (batch/v1 CronJob + RBAC + ServiceAccount +
  NetworkPolicy + optional custom-CA ConfigMap)
- `nix/` — package, OCI image, devshell, treefmt, nixbot effects

## Development Commands

```bash
nix develop                 # go, gopls, kind, kubectl, helm (direnv: use flake)
go test ./...               # unit tests, no cluster/Harbor needed
go build ./cmd/harbor-labeler
nix fmt                     # treefmt: gofmt, nixfmt, deadnix, statix, yamlfmt, mdformat
nix build                   # static binary
nix build .#image           # streamLayeredImage; ./result | docker load
helm template test ./chart  # render chart
./e2e/run.sh                # full e2e; docker required, Linux only
```

## Code Conventions & Common Patterns

- Error wrapping: `fmt.Errorf("<gerund context>: %w", err)` — lowercase, no
  trailing punctuation (`"listing pods: %w"`, `"labeling %s: %w"`).
- Tolerated failures: `log.Printf("warning: <action> %s failed: %v", ...)`;
  fatal wiring errors in `main.go`: `log.Fatalf("<stage>: %v", err)`. Plain
  stdlib `log`, no structured logging.
- JSON response shapes are anonymous inline structs local to each client
  method; exported surface stays minimal, helpers unexported.
- Comments: doc comments on declarations follow Go's convention
  (go.dev/doc/comment) — full sentences starting with the identifier, as the
  code already does. Inline comments: lowercase, only non-obvious "why".
- No new dependencies without asking — direct deps are exactly
  `k8s.io/{api,apimachinery,client-go}`; the Harbor client is deliberately
  raw `net/http`, no SDK.
- Config is env-only (`LoadConfig`); new knobs follow the `POD_PHASES`
  pattern: optional env var, parsed + validated in `config.go`, error message
  names the variable.

## Important Files

- `internal/labeler/reconcile.go` — `HarborAPI` seam + core reconcile logic
- `internal/labeler/harbor.go` — Harbor v2 client (retry, pagination,
  encoding quirks live here)
- `nix/package.nix` — `version = "..."` is the **single source of truth** for
  releases; CI reads `nix eval --raw .#default.version`; bumping it on main
  triggers image+chart release
- `chart/templates/cronjob.yaml` — note `suspend` is a **top-level** value
  (`.Values.suspend`, default false), not under `harborLabeler`
- `e2e/run.sh` — e2e env contract (`LABELER_BIN`, `E2E_IMAGE_*`,
  `E2E_CRONJOB*`); tests assume it

## Runtime/Tooling Preferences

- Nix-first: build/format/develop through the flake; missing tools via
  `nix shell nixpkgs#<pkg>`. Static `CGO_ENABLED=0` linux build.
- Go version from `go.mod`; no Makefile — the flake and `go` commands are the
  interface.
- CI split: nixbot runs `nix flake check` (formatting, package/image builds,
  helm lint) on every push; GitHub Actions `publish.yml` (main) gates
  releases via skopeo tag-existence check; `e2e.yml` runs the e2e suite on
  PRs only (docker unavailable in the nix sandbox).
- Prefer `jj` over git in colocated repos; atomic commits, kernel-style
  messages.

## Testing & QA

- Frameworks: stdlib `testing` only — no testify. Table-driven where natural;
  subtest names are behavior sentences ("conflict means already labeled, not
  an error"); helpers use `t.Helper()` + `t.Cleanup`.
- One fake per seam (keep it that way):
  - `config_test.go` — `t.Setenv` via `setAll(t)` helper
  - `kubernetes_test.go` — client-go `fake.NewSimpleClientset`
  - `harbor_test.go` — `httptest.Server` fake Harbor, `newTestClient` sets
    `retryDelay = 0`
  - `reconcile_test.go` — in-memory `fakeHarbor` implementing `HarborAPI`
- E2E (`./e2e/run.sh`): kind + real Harbor + real binary + the chart running
  in-cluster; ordered subtests attach → detach → safety guard → chart run.
  `//go:build e2e` tag; skips without env, so bare `go test -tags e2e ./e2e`
  stays green. `KEEP_CLUSTER=1` keeps the cluster for debugging.
- Known e2e gaps (documented in `e2e/README.md`, don't claim coverage): TLS /
  custom CAs, cron schedule firing, same-digest-multi-repo dedup limitation,
  pagination scale.
- Unit tests run in `buildGoModule`'s check phase, so `nix build` failing can
  mean a test failure. Behavioral changes need the covering unit test; chart
  or discovery changes warrant an e2e run.
