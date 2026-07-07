# harbor-labeler

A Kubernetes CronJob that marks container images in a Harbor registry with a
`running-<cluster>` label while they are running in your cluster, and removes
the label once they are not.

This makes it visible in Harbor which artifacts are actually in use — for
example as a guard when building retention/garbage-collection policies.

## How it works

Each run performs one full reconcile:

1. Lists all pods (all namespaces) and collects the image digests of their
   containers and init containers.
1. Keeps only images pulled from this Harbor instance (host derived from
   `HARBOR_URL`).
1. Ensures the global label `running-<CLUSTER_NAME>` exists in Harbor,
   creating it if missing.
1. Attaches the label to every running artifact (matched by digest).
1. Lists all artifacts currently carrying the label and detaches it from any
   that are no longer running.

Multiple clusters can safely point at the same Harbor: each cluster only ever
touches its own `running-<cluster>` label.

Safety: if the pod scan finds zero matching images, the run aborts without
touching Harbor — an empty result almost always means discovery is broken,
and proceeding would strip the label from everything.

Per-artifact failures (e.g. an image that was deleted from Harbor) are logged,
the run continues, and the job exits non-zero at the end so failures stay
visible in the CronJob history.

## Configuration

Four environment variables, all required:

| Variable | Description |
| ----------------- | -------------------------------------------------- |
| `HARBOR_URL` | Harbor base URL, e.g. `https://harbor.example.com` |
| `HARBOR_USERNAME` | Harbor user or robot account |
| `HARBOR_PASSWORD` | Password or robot token |
| `CLUSTER_NAME` | Cluster identifier; becomes `running-<name>` |

Outside a cluster the standard kubeconfig resolution applies (`KUBECONFIG`,
`~/.kube/config`); in-cluster the service account is used.

## Harbor permissions

Use a system-level robot account with:

- label: create/list (global labels)
- project: list
- repository: list, artifact: list
- artifact-label: create/delete on the relevant projects

## Deploy with Helm

```bash
helm install harbor-labeler ./chart \
  --set harborLabeler.image.registry=your-registry.example.com \
  --set harborLabeler.env.HARBOR_URL=https://harbor.example.com \
  --set harborLabeler.env.HARBOR_USERNAME=robot_labeler \
  --set harborLabeler.env.HARBOR_PASSWORD=... \
  --set harborLabeler.env.CLUSTER_NAME=production
```

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

## Container image

The OCI image is built with Nix:

```bash
nix build .#image
./result | docker load        # streamLayeredImage emits a tar stream
docker tag harbor-labeler:0.1.0 your-registry.example.com/tools/harbor-labeler:0.1.0
docker push your-registry.example.com/tools/harbor-labeler:0.1.0
```

## Development

```bash
nix develop        # go, gopls, helm
go test ./...
go build ./cmd/harbor-labeler
nix fmt
```

The test suite runs without a Kubernetes cluster or a Harbor instance: the
Kubernetes side uses the client-go fake clientset, the Harbor client is tested
against an `httptest` fake.

## License

MIT
