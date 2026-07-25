# harbor-labeler

A Kubernetes CronJob that marks container images in a Harbor registry with a
`running-<cluster>` label while pods in your cluster reference them, and
removes the label once no considered pod does.

## Why

Harbor knows what it stores. It has no idea what a cluster runs. Nothing in
Harbor closes that gap on its own: retention rules, immutable tag rules and
garbage collection select artifacts by repository, tag, count and age, and
none of them can be told to spare an artifact because a label says it is in
use.

harbor-labeler writes that missing fact into Harbor. Once every running
digest carries `running-<cluster>`, the Harbor features that *are*
label-aware can work with it:

- **Vulnerability reports scoped to what actually runs.** Harbor's CVE
  export takes a label filter, so a CSV covering only the artifacts your
  clusters currently run is one request away. A registry-wide report is
  dominated by artifacts nobody has run in a year; the same report scoped to
  the running set is a work queue.
- **Deletion review before deletion.** Retention cannot skip labeled
  artifacts, but a retention dry run can be checked against them, and a
  cleanup script can query them and leave them alone.
- **Replication of the running set.** Replication rules do filter by label,
  so "mirror what production currently runs to the DR registry" becomes a
  native Harbor rule.
- **Inventory across clusters.** Labels are global and each cluster manages
  only its own, so the artifact list answers "is this digest still running,
  and where" without querying every cluster.

The deletion case is worth spelling out, because it fails late. Removing an
image that pods are running breaks nothing at first, since the nodes have it
cached. It breaks at the next pull — a reschedule onto a cold node, a
scale-out, a node reboot, a rollback. For the same reason Harbor's "pulled
within the last # days" retention template is not a proxy for "in use": a
pod can run for months without a single re-pull.

What the label cannot do is protect anything by itself — see
[What the label can and cannot do](#what-the-label-can-and-cannot-do).

## How it works

Each run performs one full reconcile:

1. Lists all pods in all namespaces, applies the optional `POD_PHASES`
   filter, and reads kubelet-attested digests from
   `status.containerStatuses[].imageID` and
   `status.initContainerStatuses[].imageID`.
1. Attributes each digest to both the repository reported by the kubelet and
   the repository declared by the matching container's `spec.image`.
1. Keeps each reference only when its registry host (including any port)
   matches the host derived from `HARBOR_URL`.
1. Ensures the global label `running-<CLUSTER_NAME>` exists in Harbor,
   creating it if missing.
1. Attaches the label to every running artifact, matched by digest.
1. Lists all artifacts currently carrying the label and detaches it from any
   that are no longer running.

Multiple clusters can safely point at the same Harbor: each cluster only ever
touches its own `running-<cluster>` label.

Matching is always by digest, never tag. The two repository sources matter
when containerd deduplicates pulls by digest and reports a different
repository from the one declared by the workload.

Safety: if the pod scan finds zero matching images, the run aborts without
touching Harbor — an empty result almost always means discovery is broken,
and proceeding would strip the label from everything.

Recoverable failures for individual artifacts or project listings are logged
and aggregated. The run continues with the available results, then exits
non-zero so failures stay visible in the CronJob history.

Harbor listings are read in pages of 100. API requests use up to three
attempts on transport errors and 5xx responses, but never retry 4xx responses.
Label attachment and removal are idempotent: an already-attached label and
an already-removed label or artifact are treated as success.

## Configuration

Four required environment variables, two optional:

| Variable | Description |
| ----------------- | -------------------------------------------------- |
| `HARBOR_URL` | Harbor base URL, e.g. `https://harbor.example.com` |
| `HARBOR_USERNAME` | Harbor user or robot account |
| `HARBOR_PASSWORD` | Password or robot token |
| `CLUSTER_NAME` | Cluster identifier; becomes `running-<name>` |
| `POD_PHASES` | Optional. Comma-separated pod phases to consider (`Pending`, `Running`, `Succeeded`, `Failed`, `Unknown`), case-insensitive, e.g. `Running`. Unset: every pod object counts — including completed Job pods until they are deleted. |
| `DRY_RUN` | Optional. `true` previews label creation, attachment, and removal in logs without writing to Harbor; `false` or unset applies changes. Values are case-insensitive. |

Outside a cluster the standard kubeconfig resolution applies (`KUBECONFIG`,
`~/.kube/config`); in-cluster the service account is used.

## Harbor permissions

Use a system-level robot account with:

- label: create/list (global labels)
- project: list
- repository: list, artifact: list
- artifact-label: create/delete on the relevant projects

## What the label can and cannot do

Harbor consumes labels in some features and ignores them in others. Checked
against Harbor 2.13 and against Harbor's current main branch.

| Feature | Filters by label |
| ------------------------------------- | ---------------- |
| CVE / scan data export | yes |
| Artifact list and search (UI and API) | yes |
| Replication rules | yes |
| P2P preheat policies | yes |
| Tag retention rules | no |
| Immutable tag rules | no |
| Garbage collection | no |
| Webhooks, quotas, deployment security | no |

The cleanup mechanisms are exactly the ones that ignore labels. Harbor's
retention and immutability rules restrict their selector to path patterns
and reject a label selector outright, and garbage collection takes no
selector at all. So the rule to keep in mind is: **an artifact carrying
`running-<cluster>` is deleted by retention and GC like any other.** The
label makes the running set visible and queryable; it never protects it.

### Vulnerability reports for running images

Harbor's CVE export (2.6 and later) accepts a label filter, which turns the
running set into a scoped vulnerability report.

In the UI, open a project, choose **Export CVEs**, and pick the
`running-<cluster>` labels from the label dropdown. Through the API:

```bash
curl -u "$HARBOR_USERNAME:$HARBOR_PASSWORD" \
  -X POST "$HARBOR_URL/api/v2.0/export/cve" \
  -H 'Content-Type: application/json' \
  -H 'X-Scan-Data-Type: vulnerability' \
  -d '{"job_name":"running-cves","projects":[1],"labels":[7]}'
```

Poll `/api/v2.0/export/cve/executions` and download the CSV from the
finished execution. Worth knowing:

- Selecting several labels matches artifacts carrying **any** of them, so a
  single export can cover `running-production` and `running-staging`
  together.
- One export covers one project, so a label spanning several projects needs
  one export per project.
- The filter takes label IDs, not names. Resolve them once through
  `GET /api/v2.0/labels?scope=g`.
- Triggering an export requires the Admin, Developer or Maintainer role on
  the project.

### Retention and cleanup review

Retention rules combine with OR and support count/age templates plus
repository and tag filters, but no label filter. Use the label around them:

- **Dry-run cross-check.** Before enabling a rule, use **Dry Run** and check
  the candidate deletions against artifacts carrying `running-*` labels.
- **Label-aware cleanup scripts.** For automated deletion, query the
  artifact API yourself and skip anything carrying a `running-*` label —
  labels are first-class there (`q=labels=(<id>)`), unlike in retention
  rules.
- **Generous windows.** If you rely on plain retention rules, size them
  against your slowest redeploy cycle, and don't use the "pulled within the
  last # days" templates as a proxy for "running".

See the [Harbor tag retention docs](https://goharbor.io/docs/2.13.0/working-with-projects/working-with-images/create-tag-retention-rules/)
for rule semantics.

### Replicating or preheating the running set

Replication rules and P2P preheat policies both accept label filters, so
unlike the review workflows above these run unattended:

- A replication rule filtering on `running-production` mirrors the
  production working set to a DR or air-gapped registry.
- A preheat policy with the same filter distributes only running artifacts
  to Dragonfly or Kraken peers.

## Deploy with Helm

The release version comes from `nix/package.nix`. After e2e succeeds on a
push to `main`, CI publishes a release only when that version is not already
present in GHCR: image tags `<version>` and `latest`, chart version
`<version>`, and Git tag/GitHub release `v<version>`. Otherwise it refreshes
only the image tag `main`; the chart is release-only.

```bash
helm install harbor-labeler oci://ghcr.io/fosskar/charts/harbor-labeler \
  --set harborLabeler.env.HARBOR_URL=https://harbor.example.com \
  --set harborLabeler.env.HARBOR_USERNAME=robot_labeler \
  --set harborLabeler.env.HARBOR_PASSWORD=... \
  --set harborLabeler.env.CLUSTER_NAME=production
```

The chart defaults to a one-minute schedule, forbids overlapping runs, and
gives each Job 900 seconds to finish. It creates a dedicated ServiceAccount,
cluster-wide `pods/list` RBAC, and, when enforced by the cluster's CNI, a
NetworkPolicy restricting egress to DNS plus TCP ports 443 and 6443. If
Harbor uses another port, add it to `networkPolicy.egressPorts`.

Set `harborLabeler.dryRun: true` to preview reconciliation without creating,
attaching, or removing Harbor labels. Planned changes are written to the Job
logs.

Prefer a Secret for the token:

```yaml
harborLabeler:
  env:
    HARBOR_URL: "https://harbor.example.com"
    HARBOR_USERNAME: "robot_labeler"
    CLUSTER_NAME: "production"
  envFrom:
    - secretRef:
        name: harbor-labeler-credentials # contains HARBOR_PASSWORD
  schedule: "*/5 * * * *"
```

### Custom CA certificates

For Harbor behind an internal CA, mount the CA bundle:

```yaml
harborLabeler:
  customCAs:
    enabled: true
    certificates:
      harbor-ca.crt: |
        -----BEGIN CERTIFICATE-----
        ...
        -----END CERTIFICATE-----
    # or reference an existing ConfigMap/Secret:
    # configMap: "your-ca-configmap"
    # secret: "your-ca-secret"
```

### Failure alerting

The job exits non-zero on error, but a CronJob that fails silently for
weeks means new in-use images never get labeled — and Harbor retention can
delete them. On clusters with prometheus-operator and kube-state-metrics,
the chart can ship a `PrometheusRule` covering both failed runs and a
missing recent success:

```yaml
monitoring:
  enabled: true
  # match your Prometheus rule selector, e.g.:
  additionalLabels:
    release: kube-prometheus-stack
  # alert when no run succeeded within this window (seconds)
  maxAgeSeconds: 86400
```

Without prometheus-operator, alert externally on
`kube_cronjob_status_last_successful_time` going stale for this CronJob.

## Container image

Release images are published as `ghcr.io/fosskar/harbor-labeler:<version>`
and `:latest`; non-release builds from `main` use `:main`. To build and push
the current version manually:

```bash
version="$(nix eval --raw .#default.version)"
nix build .#image
./result | docker load
docker tag "harbor-labeler:$version" "ghcr.io/fosskar/harbor-labeler:$version"
docker push "ghcr.io/fosskar/harbor-labeler:$version"
```

## Development

```bash
nix develop                 # go, gopls, kind, kubectl, helm
go test ./...
go build ./cmd/harbor-labeler
helm template test ./chart
nix build                   # static binary; runs unit tests
nix build .#image           # OCI image tar stream
nix fmt
```

The unit test suite runs without a Kubernetes cluster or a Harbor instance:
the Kubernetes side uses the client-go fake clientset, the Harbor client is
tested against an `httptest` fake.

The end-to-end suite verifies against real infrastructure — a kind cluster
with the official Harbor helm chart deployed inside it, real pods, the real
binary, and this repo's chart running the labeler in-cluster:

```bash
nix develop -c ./e2e/run.sh # Linux and docker required
```

It covers what the fakes cannot: the Harbor v2 API contract, the `imageID`
format real containerd reports, env/kubeconfig wiring, chart rendering,
RBAC, in-cluster auth, the OCI image, same-digest repository promotion, and
all three custom-CA sources. See [e2e/README.md](e2e/README.md) for exact
scope, topology, and known gaps.

CI runs e2e on pull requests and pushes to `main` that touch code, chart,
e2e, or Nix inputs, plus manual dispatch. nixbot runs `nix flake check`,
which checks formatting, runs unit tests while building the binary, builds
the OCI image, and lints and renders the chart.

Design decisions and their rationale are recorded in
[docs/DECISIONS.md](docs/DECISIONS.md).

## Troubleshooting

**`found 0 unique running images` and a non-zero exit** — the safety guard
aborted the run (see above). Almost always a host-matching problem: an
image only counts if its registry host — including the port — equals the
host of `HARBOR_URL`. Compare `kubectl get pod ... -o jsonpath='{.status.containerStatuses[*].imageID}'` against your
`HARBOR_URL`. Common causes:

- `HARBOR_URL` includes a port but pods pull without one (or vice versa).
- Pods pull through a pull-through cache or mirror, so the kubelet reports
  the mirror's host. The digest is also attributed to the repository named
  in the pod's `spec.image`, so this still works as long as the pod spec
  references the Harbor host — but if the spec references the mirror too,
  the image is invisible to the labeler.
- `POD_PHASES` set too strictly, filtering out every pod.

**Label attached but artifacts still deleted** — Harbor retention rules
cannot filter by label, so a retention or GC policy will delete labeled
artifacts like any others. The label is advisory: it feeds CVE exports,
dry-run review and label-aware cleanup scripts (see "What the label can and
cannot do" above).

## License

MIT
