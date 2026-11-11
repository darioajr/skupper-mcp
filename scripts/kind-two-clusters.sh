#!/usr/bin/env bash
# Bring up two single-node Kubernetes clusters on Podman, install Skupper v2 on
# both, link them, and deploy a workload that produces Listeners and Connectors.
#
# The result is what this MCP server is built to read: two kubeconfig contexts
# (kind-west, kind-east) holding a real Skupper v2 application network.
#
# Usage:
#   scripts/kind-two-clusters.sh up      # create everything (~10 min first run)
#   scripts/kind-two-clusters.sh status  # show sites, links, listeners, connectors
#   scripts/kind-two-clusters.sh break   # break the network, to exercise diagnosis
#   scripts/kind-two-clusters.sh down    # delete both clusters
set -euo pipefail

SKUPPER_VERSION="${SKUPPER_VERSION:-2.2.2}"
METALLB_VERSION="${METALLB_VERSION:-v0.15.2}"
WEST_CTX="kind-west"
EAST_CTX="kind-east"

export KIND_EXPERIMENTAL_PROVIDER=podman

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

require() {
  for bin in kind kubectl podman; do
    command -v "$bin" >/dev/null || { echo "missing: $bin (brew install $bin)" >&2; exit 1; }
  done
  podman machine inspect --format '{{.Rootful}}' | grep -qx true || {
    echo "podman machine must be rootful for kind:" >&2
    echo "  podman machine stop && podman machine set --rootful && podman machine start" >&2
    exit 1
  }
}

create_cluster() {
  local name="$1"
  if kind get clusters 2>/dev/null | grep -qx "$name"; then
    log "cluster $name already exists"
  else
    log "creating cluster $name"
    kind create cluster --name "$name" --wait 120s
  fi
}

# MetalLB gives each cluster a LoadBalancer IP on the shared Podman "kind"
# network. Both clusters sit on that bridge, so a Skupper router exposed on one
# is reachable from the other. Without it, linkAccess=loadbalancer stays Pending.
install_metallb() {
  local ctx="$1" pool_start="$2" pool_end="$3"
  log "installing MetalLB in $ctx (pool $pool_start-$pool_end)"
  kubectl --context "$ctx" apply -f \
    "https://raw.githubusercontent.com/metallb/metallb/${METALLB_VERSION}/config/manifests/metallb-native.yaml"
  kubectl --context "$ctx" -n metallb-system wait --for=condition=Available \
    deploy/controller --timeout=300s
  kubectl --context "$ctx" -n metallb-system wait --for=condition=Ready \
    pod -l component=speaker --timeout=300s
  kubectl --context "$ctx" apply -f - <<EOF
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: kind-pool
  namespace: metallb-system
spec:
  addresses:
    - ${pool_start}-${pool_end}
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: kind-l2
  namespace: metallb-system
spec:
  ipAddressPools:
    - kind-pool
EOF
}

install_skupper() {
  local ctx="$1"
  log "installing Skupper $SKUPPER_VERSION in $ctx"
  local base="https://github.com/skupperproject/skupper/releases/download/${SKUPPER_VERSION}"
  kubectl --context "$ctx" apply -f "${base}/skupper-crds.yaml"
  kubectl --context "$ctx" apply -f "${base}/skupper-cluster-scope.yaml"
  kubectl --context "$ctx" -n skupper wait --for=condition=Available \
    deploy --all --timeout=300s
}

create_sites() {
  log "creating site west (linkAccess=loadbalancer) and site east"
  kubectl --context "$WEST_CTX" create namespace west --dry-run=client -o yaml | \
    kubectl --context "$WEST_CTX" apply -f -
  kubectl --context "$WEST_CTX" apply -f - <<'EOF'
apiVersion: skupper.io/v2alpha1
kind: Site
metadata:
  name: west
  namespace: west
spec:
  linkAccess: loadbalancer
EOF
  kubectl --context "$EAST_CTX" create namespace east --dry-run=client -o yaml | \
    kubectl --context "$EAST_CTX" apply -f -
  kubectl --context "$EAST_CTX" apply -f - <<'EOF'
apiVersion: skupper.io/v2alpha1
kind: Site
metadata:
  name: east
  namespace: east
spec: {}
EOF
  kubectl --context "$WEST_CTX" -n west wait --for=condition=Ready site/west --timeout=300s
  kubectl --context "$EAST_CTX" -n east wait --for=condition=Ready site/east --timeout=300s
}

# The v2 link handshake, done with kubectl only: west issues an AccessGrant, the
# controller fills its status with url/code/ca, east redeems it as an AccessToken
# and the controller creates the Link.
link_sites() {
  if kubectl --context "$EAST_CTX" -n east get link east-to-west >/dev/null 2>&1; then
    log "link already exists"
    return
  fi
  log "issuing AccessGrant in west"
  kubectl --context "$WEST_CTX" apply -f - <<'EOF'
apiVersion: skupper.io/v2alpha1
kind: AccessGrant
metadata:
  name: west-grant
  namespace: west
spec:
  redemptionsAllowed: 10
  expirationWindow: 1h
EOF
  local url code ca i=0
  until url=$(kubectl --context "$WEST_CTX" -n west get accessgrant west-grant \
                -o jsonpath='{.status.url}' 2>/dev/null) && [ -n "$url" ]; do
    i=$((i+1)); [ "$i" -gt 60 ] && { echo "AccessGrant never got a url" >&2; exit 1; }
    sleep 5
  done
  code=$(kubectl --context "$WEST_CTX" -n west get accessgrant west-grant -o jsonpath='{.status.code}')
  ca=$(kubectl --context "$WEST_CTX" -n west get accessgrant west-grant -o jsonpath='{.status.ca}')

  log "redeeming AccessToken in east (url $url)"
  CA="$ca" URL="$url" CODE="$code" python3 - <<'PY' | kubectl --context "$EAST_CTX" apply -f -
import os, json
ca = os.environ["CA"]
doc = {
    "apiVersion": "skupper.io/v2alpha1",
    "kind": "AccessToken",
    "metadata": {"name": "east-to-west", "namespace": "east"},
    "spec": {"url": os.environ["URL"], "code": os.environ["CODE"], "ca": ca},
}
print(json.dumps(doc))
PY
  kubectl --context "$EAST_CTX" -n east wait --for=condition=Ready link --all --timeout=300s
}

# A backend in east exposed through a Connector, consumed in west by a Listener.
deploy_workload() {
  log "deploying backend in east + connector, listener in west"
  kubectl --context "$EAST_CTX" -n east create deployment backend \
    --image=docker.io/library/nginx:alpine --port=80 \
    --dry-run=client -o yaml | kubectl --context "$EAST_CTX" apply -f -
  kubectl --context "$EAST_CTX" apply -f - <<'EOF'
apiVersion: skupper.io/v2alpha1
kind: Connector
metadata:
  name: backend
  namespace: east
spec:
  routingKey: backend
  port: 80
  selector: app=backend
EOF
  kubectl --context "$WEST_CTX" apply -f - <<'EOF'
apiVersion: skupper.io/v2alpha1
kind: Listener
metadata:
  name: backend
  namespace: west
spec:
  routingKey: backend
  host: backend
  port: 8080
EOF
  kubectl --context "$EAST_CTX" -n east rollout status deploy/backend --timeout=180s
}

up() {
  require
  create_cluster west
  create_cluster east
  local subnet prefix
  subnet=$(podman network inspect kind --format '{{ (index .Subnets 0).Subnet }}')
  prefix=${subnet%.*}
  echo "podman kind network: $subnet"
  install_metallb "$WEST_CTX" "${prefix}.200" "${prefix}.219"
  install_metallb "$EAST_CTX" "${prefix}.220" "${prefix}.239"
  install_skupper "$WEST_CTX"
  install_skupper "$EAST_CTX"
  create_sites
  link_sites
  deploy_workload
  status
  cat <<EOF

Both contexts are ready. Point the MCP server at them:

  SKUPPER_MCP_ALLOWED_CONTEXTS=kind-west,kind-east \\
  SKUPPER_MCP_ALLOWED_NAMESPACES=kind-west/west,kind-east/east \\
  go run ./cmd/skupper-mcp-server

EOF
}

status() {
  for pair in "$WEST_CTX:west" "$EAST_CTX:east"; do
    local ctx="${pair%%:*}" ns="${pair##*:}"
    log "$ctx / $ns"
    kubectl --context "$ctx" -n "$ns" get sites,links,listeners,connectors 2>/dev/null || true
  done
}

# Deletes the Connector in east: the routing key loses its backend, the Listener
# in west keeps waiting, and diagnose_connectivity should report it.
break_network() {
  log "deleting the east connector to produce a broken network"
  kubectl --context "$EAST_CTX" -n east delete connector backend --ignore-not-found
  status
}

down() {
  log "deleting both clusters"
  kind delete cluster --name west
  kind delete cluster --name east
}

case "${1:-up}" in
  up)     up ;;
  status) status ;;
  break)  break_network ;;
  down)   down ;;
  *)      echo "usage: $0 {up|status|break|down}" >&2; exit 1 ;;
esac
