# Design decisions

Append-only record of decisions that shaped harbor-labeler, with the
reasoning that is otherwise scattered across commit messages. Newest last.
Each entry: what was decided, why, and what it rules out. Commit hashes
refer to this repository.

## 1. Match images by digest, never by tag

The running set is keyed on the manifest digest from the kubelet-attested
`status.containerStatuses[].imageID` (plus init containers). Tags are
ignored: a `:latest` that moved after the pod started would otherwise
protect the wrong artifact, and Harbor retention operates on artifacts
(digests), not tags. Rules out: any tag-based matching, and trusting
`spec.image` for the digest.

## 2. Consumer-defined interfaces as the only test seams

`Reconcile` depends on `HarborAPI` (declared in `reconcile.go`), `Run`
depends on `ImageDiscovery` (declared in `run.go`); the concrete HTTP
client and `KubeDiscovery` are production adapters. Interfaces live in the
consuming code per Go guidance (go.dev/wiki/CodeReviewComments#interfaces),
one fake per seam, stdlib `testing` only. Chosen over mock frameworks and
over exposing `kubernetes.Interface`/`*Client` directly, which forced tests
to assemble fake pods just to get past discovery (d38b996, 1e6edc4,
84949b6).

## 3. Raw net/http Harbor client, no SDK

Direct dependencies are exactly `k8s.io/{api,apimachinery,client-go}`. The
Harbor v2 surface used here (labels, projects, repositories, artifacts) is
small enough that an SDK buys mostly transitive dependencies — "a little
copying is better than a little dependency". Encoding quirks
(double-encoded nested repo names), pagination, and retry live in one file,
`internal/labeler/harbor.go`.

## 4. Idempotent by API semantics, tolerant per artifact

`AddLabel` treats 409 as success, `RemoveLabel` treats 404 as success,
`EnsureGlobalLabel` re-looks-up on 409 create races; transport errors and
5xx are retried, 4xx never. Per-artifact failures are warnings aggregated
via `errors.Join` — the run continues but exits non-zero. A CronJob rerun
must be safe by construction, and one deleted artifact must not stop the
other 200 from being labeled.

## 5. Zero-images safety guard

An empty running set aborts the run before any Harbor call. An empty
result almost always means discovery is broken (wrong host filter, RBAC,
API outage), and proceeding would detach the label from everything the
cluster is protecting — the exact failure the tool exists to prevent. The
cost is a false negative on a genuinely empty cluster, which is accepted.

## 6. Single-shot job: env-only config, stdlib log

No daemon, no flags, no config file, no structured logging. The unit of
execution is one CronJob run; `LoadConfig` reads four required env vars
(plus optional `POD_PHASES`), output is plain `log` lines readable in
`kubectl logs`. Deliberately boring — observability is the CronJob's
success/failure history plus the optional PrometheusRule.

## 7. e2e on kind + self-hosted Harbor, not a shared demo instance

The e2e suite (5b13cb4) provisions its own Harbor via the official chart
inside a kind cluster. demo.goharbor.io was evaluated and rejected: admin
APIs are blocked for self-registered users (breaks `EnsureGlobalLabel`),
the instance is wiped every couple of days, and its valid public
certificate makes the custom-CA path untestable. The suite cannot run
under nixbot because the nix build sandbox has no docker, hence the
separate GitHub workflow.

## 8. Spec-aware discovery to compensate containerd digest dedup

containerd dedupes pulls by digest, so the kubelet may report repository A
in `imageID` for a pod whose spec references repository B holding the same
digest; B's copy then never got the label and retention could delete an
in-use artifact. Each running digest is therefore attributed to both the
imageID repository and the spec-declared repository (paired by container
name, same host filter); the digest itself still always comes from
`status.imageID` (893220b). The e2e same-digest promotion stage pins this
against real containerd.

## 9. Release versioning from nix/package.nix, publish gated on e2e

`version` in `nix/package.nix` is the single source of truth; CI reads it
via `nix eval`, `Chart.yaml` stays 0.1.0 and is overridden at package
time. Publishing runs only after the e2e suite succeeds on main
(74a71ef) and skips when the image tag already exists, so a version bump
on main is the release trigger and a broken deployment artifact cannot
ship.

## 10. TLS e2e coverage as an additive stage, not a suite conversion

Covering the customCAs path (6fc3d57, 98a39d6) terminates TLS in an nginx
proxy on a second NodePort in front of the plain-http Harbor, with one
suspended chart release per CA source variant (referenced ConfigMap,
referenced Secret, inline certificates). Converting the whole suite to
https was rejected: it would rework the docker push path and put cert
plumbing under every stage for no extra coverage. Constraint discovered on
the way, worth remembering: Harbor core builds the token-service realm as
`<externalURL scheme>://<request Host>` and ignores `X-Forwarded-Proto`,
so a TLS proxy in front of plain-http Harbor must pin the forwarded Host
to the canonical http endpoint or containerd's anonymous token fetch
breaks.
