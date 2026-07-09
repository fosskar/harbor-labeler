# Repository Guidelines

## Project Overview

harbor-labeler is a single-shot Go binary, deployed as a Kubernetes CronJob,
that marks container images in a Harbor registry with a global
`running-<CLUSTER_NAME>` label while they run in the cluster and detaches the
label once they don't. Purpose: make in-use artifacts visible as a guard for
Harbor retention/GC policies. Multi-cluster safe — each cluster only touches
its own label.

## Architecture & Data Flow

Two packages, linear pipeline, no daemon state, no goroutines:

```
cmd/harbor-labeler/main.go        wiring only
  LoadConfig()                    env -> Config (RegistryHost = HARBOR_URL host)
  NewKubeClient()                 in-cluster SA, kubeconfig fallback
  NewClient()                     Harbor v2 HTTP client
  NewKubeDiscovery()              clientset + registry host + pod phases
  Run()                           orchestration (run.go):
    ImageDiscovery.RunningImages()  list all pods -> digest refs, host-filtered
    Reconcile()                     ensure label, attach running, detach stale
```

- `internal/labeler` holds all logic; `main.go` never contains any.
- Two consumer-defined interfaces are the unit-test seams (per official Go
  guidance — interfaces belong in the consuming code, not the implementor:
  go.dev/wiki/CodeReviewComments#interfaces):
  - `Reconcile` depends on `HarborAPI` (`reconcile.go`); the concrete HTTP
    `*Client` (`harbor.go`) is the production adapter. Keep new Harbor calls
    behind it.
  - `Run` depends on `ImageDiscovery` (`run.go`); `KubeDiscovery`
    (`kubernetes.go`) is the production adapter. Run tests fake discovery
    with artifact lists — no fake clientset needed.
- Load-bearing invariants:
  - Matching is **by digest, never tag**: the digest always comes from the
    kubelet-attested `status.containerStatuses[].imageID` (+ init
    containers). Each running digest is attributed to two repositories —
    the one the imageID reports and the one the matching container's
    `spec.image` declares (containerd dedupes pulls by digest, so the
    kubelet may name a different repo holding the same digest than the one
    the workload references). Both refs count only when their host equals
    `HARBOR_URL`'s host (port included).
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
  NetworkPolicy + optional custom-CA ConfigMap; an existing CA Secret can be
  referenced by name, no Secret template)
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
nix develop -c ./e2e/run.sh # full e2e; docker required, Linux only
```

e2e MUST run through the devshell — kind/kubectl/helm are flake-provided,
not on bare PATH. ~3–4 min warm, longer on first run (Harbor images).

## Code Conventions & Common Patterns

- Error wrapping: `fmt.Errorf("<gerund context>: %w", err)` — lowercase, no
  trailing punctuation (`"listing pods: %w"`, `"labeling %s: %w"`).
- Tolerated failures: `log.Printf("warning: <action> %s failed: %v", ...)`;
  fatal wiring errors in `main.go`: `log.Fatalf("<stage>: %v", err)`. Plain
  stdlib `log`, no structured logging (deliberate for a single-shot job).
- JSON response shapes are anonymous inline structs local to each client
  method; request bodies are `map[string]...` literals; exported surface
  stays minimal, helpers unexported.
- Comments: doc comments on declarations follow Go's convention
  (go.dev/doc/comment) — full sentences starting with the identifier, as the
  code already does. Inline comments: lowercase, only non-obvious "why".
- No new dependencies without asking — direct deps are exactly
  `k8s.io/{api,apimachinery,client-go}`; the Harbor client is deliberately
  raw `net/http`, no SDK ("a little copying is better than a little
  dependency").
- Config is env-only (`LoadConfig`): `HARBOR_URL`, `HARBOR_USERNAME`,
  `HARBOR_PASSWORD`, `CLUSTER_NAME` required; new knobs follow the
  `POD_PHASES` pattern — optional env var, parsed + validated in
  `config.go`, error message names the variable.

## Important Files

- `internal/labeler/reconcile.go` — `HarborAPI` seam + core reconcile logic
- `internal/labeler/run.go` — `ImageDiscovery` seam + orchestration
- `internal/labeler/harbor.go` — Harbor v2 client (retry, pagination,
  encoding quirks live here)
- `nix/package.nix` — `version = "..."` is the **single source of truth** for
  releases; CI reads `nix eval --raw .#default.version`; `Chart.yaml` stays
  0.1.0 and is overridden at package time; bumping the version on main
  triggers image+chart release
- `chart/templates/cronjob.yaml` — `suspend` and `completions` are
  **top-level** values read only by the template with `| default`
  (`.Values.suspend`, `.Values.completions`); they are not declared in
  `values.yaml`
- `e2e/run.sh` — e2e env contract (`HARBOR_URL/USERNAME/PASSWORD`,
  `CLUSTER_NAME`, `LABELER_BIN`, `E2E_IMAGE_A/B`, `E2E_IMAGE_PROMOTED`,
  `E2E_CRONJOB`,
  `E2E_CRONJOB_NAMESPACE`); tests assume it

## Runtime/Tooling Preferences

- Nix-first: build/format/develop through the flake; missing tools via
  `nix shell nixpkgs#<pkg>`. Static `CGO_ENABLED=0` linux build, runs as
  uid 3000.
- Go version from `go.mod`; no Makefile — the flake and `go` commands are
  the interface.
- Plain git repo (not jj-colocated). Atomic commits, kernel-style messages
  with area prefixes as observed in history (`labeler:`, `chart:`, `ci:`,
  `nix:`, `e2e:`, `docs:`, `publish:`, `effects:`, `flake:`, `package:`);
  no tags/trailers.
- CI split: nixbot runs `nix flake check` (formatting, go tests via
  buildGoModule check phase, package/image builds, helm lint) on every
  push; `e2e.yml` runs the suite on PRs and code pushes to main
  (`e2e-skip.yml` mirrors its path filter so the required check never hangs
  on docs-only changes); `publish.yml` triggers via workflow_run on e2e
  success on main and gates release via skopeo tag-existence check
  (`CONTAINERS_REGISTRIES_CONF=/dev/null` works around nixpkgs skopeo on
  ubuntu-latest).

## Testing & QA

- Frameworks: stdlib `testing` only — no testify. Table-driven where natural;
  subtest names are behavior sentences ("conflict means already labeled, not
  an error"); helpers use `t.Helper()` + `t.Cleanup`.
- One fake per seam (keep it that way):
  - `config_test.go` — `t.Setenv` via `setAll(t)` helper
  - `kubernetes_test.go` — client-go `fake.NewSimpleClientset` through
    `KubeDiscovery`
  - `harbor_test.go` — `httptest.Server` fake Harbor, `newTestClient` sets
    `retryDelay = 0`
  - `reconcile_test.go` — in-memory `fakeHarbor` implementing `HarborAPI`
  - `run_test.go` — `fakeDiscovery` implementing `ImageDiscovery`
- E2E (`nix develop -c ./e2e/run.sh`): kind + real Harbor + real binary +
  the chart running in-cluster; ordered subtests attach → detach → safety
  guard → chart run → same-digest promotion. `//go:build e2e` tag; skips
  without env, so bare
  `go test -tags e2e ./e2e` stays green. `KEEP_CLUSTER=1` keeps the cluster
  for debugging.
- Known e2e gaps (documented in `e2e/README.md`, don't claim coverage): TLS /
  custom CAs, cron schedule firing, which repo name containerd's dedup
  reports for a shared digest, pagination scale.
- Unit tests run in `buildGoModule`'s check phase, so `nix build` failing can
  mean a test failure. Behavioral changes need the covering unit test; chart
  or discovery changes warrant an e2e run.
