#!/usr/bin/env bash
# End-to-end test: real kind cluster + real Harbor (helm chart) + the real
# harbor-labeler binary + the real harbor-labeler chart running in-cluster.
# See e2e/README.md for what this covers and e2e/e2e_test.go for the
# scenario; this script only provisions infrastructure and invokes
# `go test -tags e2e`.
#
# Requirements: docker, kind, kubectl, helm, go, nix (all but nix in
# `nix develop`). Linux only: the host must be able to reach the kind node's
# docker-network IP directly.
#
# Env knobs:
#   KEEP_CLUSTER=1   don't delete the kind cluster on exit (debugging)
set -euo pipefail

cluster_name=harbor-labeler-e2e
harbor_port=30002
admin_password=Harbor12345
cluster_label_name=e2e-kind
labeler_namespace=harbor-labeler-system
repo_root=$(cd "$(dirname "$0")/.." && pwd)
workdir=$(mktemp -d)

cleanup() {
  if [[ "${KEEP_CLUSTER:-0}" != 1 ]]; then
    kind delete cluster --name "$cluster_name" >/dev/null 2>&1 || true
  else
    echo "KEEP_CLUSTER=1: cluster '$cluster_name' left running"
  fi
  rm -rf "$workdir"
}
trap cleanup EXIT

log() { printf '\n==> %s\n' "$*"; }

log "creating kind cluster '$cluster_name'"
cat >"$workdir/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      # Harbor NodePort (http) -> host localhost. Only docker push uses this
      # mapping: docker allows plain http for localhost registries without
      # daemon config. Everything else addresses Harbor by node IP.
      - containerPort: ${harbor_port}
        hostPort: ${harbor_port}
EOF
kind delete cluster --name "$cluster_name" >/dev/null 2>&1 || true
kind create cluster --name "$cluster_name" --config "$workdir/kind.yaml" \
  --kubeconfig "$workdir/kubeconfig" --wait 120s
export KUBECONFIG="$workdir/kubeconfig"

# The registry identity everything agrees on is <node-ip>:<port>: it is
# reachable under that one name from the host (docker network route), from
# in-cluster pods (NodePort on the node address), and from the node's
# containerd. That mirrors production, where pods, the labeler, and Harbor
# all use one hostname — the labeler requires imageID host == HARBOR_URL
# host.
node="${cluster_name}-control-plane"
node_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$node")
registry="${node_ip}:${harbor_port}"
harbor_url="http://${registry}"
push_registry="localhost:${harbor_port}"

# Teach the node's containerd that ${registry} speaks plain http. certs.d
# changes are picked up without a containerd restart.
docker exec "$node" mkdir -p "/etc/containerd/certs.d/${registry}"
docker exec -i "$node" tee "/etc/containerd/certs.d/${registry}/hosts.toml" >/dev/null <<EOF
server = "${harbor_url}"
[host."${harbor_url}"]
EOF

log "installing Harbor via helm (this pulls ~2 GiB of images on first run)"
helm repo add harbor https://helm.goharbor.io >/dev/null
helm repo update harbor >/dev/null
helm upgrade --install harbor harbor/harbor \
  --namespace harbor --create-namespace \
  --set expose.type=nodePort \
  --set expose.tls.enabled=false \
  --set "externalURL=${harbor_url}" \
  --set "harborAdminPassword=${admin_password}" \
  --set trivy.enabled=false \
  --set metrics.enabled=false \
  --wait --timeout 15m

log "waiting for Harbor API"
for _ in $(seq 1 60); do
  if curl -fsS "${harbor_url}/api/v2.0/systeminfo" >/dev/null 2>&1; then
    break
  fi
  sleep 5
done
curl -fsS "${harbor_url}/api/v2.0/systeminfo" >/dev/null

log "creating public project 'e2e'"
curl -fsS -u "admin:${admin_password}" -X POST \
  -H 'Content-Type: application/json' \
  -d '{"project_name":"e2e","public":true}' \
  "${harbor_url}/api/v2.0/projects" \
  || true # 409 when the project survives from a kept cluster

log "pushing test images"
docker pull busybox:stable
docker login "$push_registry" -u admin -p "$admin_password"
# app-a and app-b must keep distinct digests: containerd dedupes images by
# digest, so identical retags would make the kubelet report both pods'
# imageID under one repository and hide the second artifact from the labeler.
# app-promoted (pushed below) instead deliberately SHARES app-a's digest, to
# exercise spec-aware discovery.
for app in app-a app-b; do
  docker build -t "${push_registry}/e2e/${app}:latest" - <<EOF
FROM busybox:stable
ENV E2E_APP=${app}
EOF
  docker push "${push_registry}/e2e/${app}:latest"
done

# retag app-a into a second repository WITHOUT rebuilding so it shares app-a's
# digest: the pod references app-promoted, but containerd may attribute the
# shared digest to either repository.
docker tag "${push_registry}/e2e/app-a:latest" "${push_registry}/e2e/app-promoted:latest"
docker push "${push_registry}/e2e/app-promoted:latest"

log "building harbor-labeler binary and image"
labeler_bin="$workdir/harbor-labeler"
(cd "$repo_root" && CGO_ENABLED=0 go build -o "$labeler_bin" ./cmd/harbor-labeler)
version=$(nix eval --raw "$repo_root#default.version")
nix build "$repo_root#image" --out-link "$workdir/image"
"$workdir/image" | docker load
kind load docker-image "harbor-labeler:${version}" --name "$cluster_name"

log "installing harbor-labeler chart (suspended; the test triggers runs)"
helm upgrade --install harbor-labeler "$repo_root/chart" \
  --namespace "$labeler_namespace" --create-namespace \
  --set suspend=true \
  --set harborLabeler.image.registry="" \
  --set harborLabeler.image.repository=harbor-labeler \
  --set-string harborLabeler.image.tag="$version" \
  --set harborLabeler.image.pullPolicy=Never \
  --set "harborLabeler.env.HARBOR_URL=${harbor_url}" \
  --set harborLabeler.env.HARBOR_USERNAME=admin \
  --set "harborLabeler.env.HARBOR_PASSWORD=${admin_password}" \
  --set "harborLabeler.env.CLUSTER_NAME=${cluster_label_name}" \
  --set 'networkPolicy.egressPorts={443,6443,'"${harbor_port}"'}' \
  --wait

log "running e2e tests"
cd "$repo_root"
HARBOR_URL="$harbor_url" \
  HARBOR_USERNAME=admin \
  HARBOR_PASSWORD="$admin_password" \
  CLUSTER_NAME="$cluster_label_name" \
  LABELER_BIN="$labeler_bin" \
  E2E_IMAGE_A="${registry}/e2e/app-a:latest" \
  E2E_IMAGE_B="${registry}/e2e/app-b:latest" \
  E2E_IMAGE_PROMOTED="${registry}/e2e/app-promoted:latest" \
  E2E_CRONJOB=harbor-labeler \
  E2E_CRONJOB_NAMESPACE="$labeler_namespace" \
  go test -tags e2e -count=1 -timeout 20m -v ./e2e/...

log "e2e passed"
