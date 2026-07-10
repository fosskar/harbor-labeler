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
tls_port=30003
admin_password=Harbor12345
cluster_label_name=e2e-kind
tls_cluster_label_name=e2e-kind-tls
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

# --- TLS stage (issue #4): terminate TLS in front of Harbor with nginx on a
# second NodePort and run a second, suspended chart release against the https
# endpoint with the referenced-ConfigMap customCAs path. The http topology
# above stays untouched; spec-aware discovery keeps the stage deterministic
# even though the TLS pod's digest is shared with the http pulls.
tls_registry="${node_ip}:${tls_port}"
tls_harbor_url="https://${tls_registry}"

log "generating self-signed certificate for ${node_ip}"
(cd "$repo_root" && go run ./e2e/gencert -ip "$node_ip" -dir "$workdir")

log "deploying TLS terminating proxy in front of Harbor"

cat >"$workdir/nginx.conf" <<EOF
events {}
http {
  server {
    listen 8443 ssl;
    ssl_certificate /certs/tls.crt;
    ssl_certificate_key /certs/tls.key;
    client_max_body_size 0;
    location / {
      # keep Harbor's canonical plain-http Host: core builds the token
      # realm from the externalURL scheme + request Host, so forwarding the
      # TLS host would yield an http:// realm pointing at this ssl-only
      # port. With the canonical Host containerd fetches the token via
      # plain http on ${harbor_port} and pulls manifests/blobs over TLS.
      proxy_pass http://harbor.harbor.svc.cluster.local:80;
      proxy_set_header Host ${registry};
    }
  }
}
EOF
kubectl -n harbor create secret tls tls-proxy-cert \
  --cert="$workdir/tls.crt" --key="$workdir/tls.key" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n harbor create configmap tls-proxy-conf \
  --from-file=nginx.conf="$workdir/nginx.conf" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tls-proxy
  namespace: harbor
spec:
  replicas: 1
  selector:
    matchLabels:
      app: tls-proxy
  template:
    metadata:
      labels:
        app: tls-proxy
    spec:
      containers:
        - name: nginx
          # pulled from docker.io by the node: the proxy pod's imageID host
          # must NOT be the Harbor registry, or the labelers would count it
          # as a running image and break the safety-guard stage
          image: nginx:alpine
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 8443
          volumeMounts:
            - name: conf
              mountPath: /etc/nginx/nginx.conf
              subPath: nginx.conf
            - name: cert
              mountPath: /certs
              readOnly: true
      volumes:
        - name: conf
          configMap:
            name: tls-proxy-conf
        - name: cert
          secret:
            secretName: tls-proxy-cert
---
apiVersion: v1
kind: Service
metadata:
  name: tls-proxy
  namespace: harbor
spec:
  type: NodePort
  selector:
    app: tls-proxy
  ports:
    - port: 8443
      targetPort: 8443
      nodePort: ${tls_port}
EOF
kubectl -n harbor rollout status deploy/tls-proxy --timeout=120s

# teach the node's containerd to trust the self-signed cert for pulls via the
# TLS endpoint (picked up without a containerd restart, like the http entry)
docker exec "$node" mkdir -p "/etc/containerd/certs.d/${tls_registry}"
docker cp "$workdir/tls.crt" "${node}:/etc/containerd/certs.d/${tls_registry}/ca.crt"
docker exec -i "$node" tee "/etc/containerd/certs.d/${tls_registry}/hosts.toml" >/dev/null <<EOF
server = "${tls_harbor_url}"
[host."${tls_harbor_url}"]
  ca = "/etc/containerd/certs.d/${tls_registry}/ca.crt"
EOF

log "waiting for Harbor API via TLS"
for _ in $(seq 1 30); do
  if curl -fsS --cacert "$workdir/tls.crt" "${tls_harbor_url}/api/v2.0/systeminfo" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
curl -fsS --cacert "$workdir/tls.crt" "${tls_harbor_url}/api/v2.0/systeminfo" >/dev/null

# one release per customCAs source variant, each with its own CLUSTER_NAME
# (harbor-labeler-tls-<variant> / e2e-kind-tls-<variant>): the referenced
# ConfigMap, the referenced Secret, and inline certificates (chart-rendered
# ConfigMap) must all end up as a working CA mount.
tls_variants="configmap secret inline"
kubectl -n "$labeler_namespace" create configmap harbor-labeler-e2e-ca \
  --from-file=harbor-ca.crt="$workdir/tls.crt" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$labeler_namespace" create secret generic harbor-labeler-e2e-ca \
  --from-file=harbor-ca.crt="$workdir/tls.crt" \
  --dry-run=client -o yaml | kubectl apply -f -
for variant in $tls_variants; do
  log "installing harbor-labeler TLS chart release (${variant} CA)"
  case "$variant" in
    configmap) ca_flags=(--set harborLabeler.customCAs.configMap=harbor-labeler-e2e-ca) ;;
    secret) ca_flags=(--set harborLabeler.customCAs.secret=harbor-labeler-e2e-ca) ;;
    inline) ca_flags=(--set-file "harborLabeler.customCAs.certificates.harbor-ca\.crt=$workdir/tls.crt") ;;
  esac
  helm upgrade --install "harbor-labeler-tls-${variant}" "$repo_root/chart" \
    --namespace "$labeler_namespace" \
    --set suspend=true \
    --set harborLabeler.image.registry="" \
    --set harborLabeler.image.repository=harbor-labeler \
    --set-string harborLabeler.image.tag="$version" \
    --set harborLabeler.image.pullPolicy=Never \
    --set "harborLabeler.env.HARBOR_URL=${tls_harbor_url}" \
    --set harborLabeler.env.HARBOR_USERNAME=admin \
    --set "harborLabeler.env.HARBOR_PASSWORD=${admin_password}" \
    --set "harborLabeler.env.CLUSTER_NAME=${tls_cluster_label_name}-${variant}" \
    --set harborLabeler.customCAs.enabled=true \
    "${ca_flags[@]}" \
    --set 'networkPolicy.egressPorts={443,6443,'"${tls_port}"'}' \
    --wait
done

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
  E2E_IMAGE_TLS="${tls_registry}/e2e/app-a:latest" \
  E2E_CRONJOB=harbor-labeler \
  E2E_CRONJOB_NAMESPACE="$labeler_namespace" \
  E2E_TLS_CRONJOB=harbor-labeler-tls \
  E2E_TLS_CLUSTER_NAME="$tls_cluster_label_name" \
  E2E_TLS_VARIANTS="$tls_variants" \
  go test -tags e2e -count=1 -timeout 20m -v ./e2e/...

log "e2e passed"
