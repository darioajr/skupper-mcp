#!/usr/bin/env bash
# Bring up a single OpenShift Local (CRC) cluster, install Skupper v2, put two
# linked sites in two namespaces, and expose both to this server as two
# kubeconfig contexts.
#
# Why one cluster and not two: CRC needs about 11 GiB on its own and runs in a
# VM of its own, next to the Podman machine. Two of them do not fit on a 24 GiB
# laptop. What this setup gives that kind cannot is the OpenShift-specific
# surface: linkAccess=route, the restricted-v2 SCC, the internal registry, and
# the in-cluster deployment with a ServiceAccount instead of a kubeconfig.
#
# What it does NOT give: two real API servers. The contexts crc-west and
# crc-east address the same cluster with different default namespaces. That
# exercises the allowlist and the per-context code path, not cross-cluster
# latency or partition behaviour. Use scripts/kind-two-clusters.sh for that.
#
# Usage:
#   scripts/crc-openshift.sh up      # cluster + Skupper + two linked sites
#   scripts/crc-openshift.sh deploy  # build the image and run the server in-cluster
#   scripts/crc-openshift.sh local   # print how to run the server from your Mac
#   scripts/crc-openshift.sh status
#   scripts/crc-openshift.sh break   # remove the connector, for diagnosis testing
#   scripts/crc-openshift.sh down    # stop the cluster (delete needs crc delete)
set -euo pipefail

SKUPPER_VERSION="${SKUPPER_VERSION:-2.2.2}"
CRC_CPUS="${CRC_CPUS:-4}"
CRC_MEMORY="${CRC_MEMORY:-11264}"   # MiB
CRC_DISK="${CRC_DISK:-60}"          # GiB
# route uses an OpenShift Route for link access, which is the OpenShift default
# and the thing worth testing here. local uses a ClusterIP instead, which always
# works within one cluster; fall back to it if the router hostname does not
# resolve from inside the cluster.
LINK_MODE="${LINK_MODE:-route}"
DEPLOY_NS="skupper-mcp-crc"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }

require() {
  command -v crc >/dev/null || die "missing crc. Install: brew install --cask crc"
  command -v podman >/dev/null || die "missing podman"
}

# CRC runs in its own VM. With the Podman machine also running, the two together
# can exceed what the host has.
check_memory() {
  local host_gib podman_gib
  host_gib=$(( $(sysctl -n hw.memsize) / 1073741824 ))
  podman_gib=0
  if podman machine list --format '{{.Running}}' 2>/dev/null | grep -qx true; then
    podman_gib=$(( $(podman machine inspect --format '{{.Resources.Memory}}') / 1024 ))
  fi
  log "memory: host ${host_gib} GiB, podman machine ${podman_gib} GiB, CRC wants $(( CRC_MEMORY / 1024 )) GiB"
  if (( podman_gib + CRC_MEMORY / 1024 + 4 > host_gib )); then
    cat >&2 <<EOF

The Podman machine and CRC together leave the host short. Free memory first:

  podman machine stop                     # release ${podman_gib} GiB
  # or shrink it permanently:
  podman machine set --memory 6144

Set CRC_MEMORY to override this check's assumption.
EOF
    read -r -p "Continue anyway? [y/N] " ans
    [ "$ans" = "y" ] || exit 1
  fi
}

pull_secret() {
  local f="${CRC_PULL_SECRET_FILE:-$HOME/Downloads/pull-secret.txt}"
  [ -f "$f" ] || die "no pull secret at $f
Get one (free Red Hat account) at https://console.redhat.com/openshift/create/local
then save it there, or set CRC_PULL_SECRET_FILE."
  echo "$f"
}

start_cluster() {
  if crc status 2>/dev/null | grep -q "Running"; then
    log "CRC already running"
  else
    local secret; secret="$(pull_secret)"
    log "configuring CRC (${CRC_CPUS} cpus, ${CRC_MEMORY} MiB, ${CRC_DISK} GiB)"
    crc config set preset openshift
    crc config set cpus "$CRC_CPUS"
    crc config set memory "$CRC_MEMORY"
    crc config set disk-size "$CRC_DISK"
    crc config set pull-secret-file "$secret"
    crc setup
    log "starting CRC (first run pulls ~4.5 GB and takes 20-30 min)"
    crc start
  fi
  eval "$(crc oc-env)"
  command -v oc >/dev/null || die "oc not on PATH after crc oc-env"
  # crc start normally leaves a working kubeadmin context behind. Log in only
  # if it did not, reading the password out of the line crc prints for it.
  if ! oc whoami >/dev/null 2>&1; then
    local pass
    pass=$(crc console --credentials | grep -- '-u kubeadmin' \
             | sed -n 's/.*-u kubeadmin -p \([^ ]*\).*/\1/p' | head -1 | tr -d "'\"")
    [ -n "$pass" ] || die "could not read the kubeadmin password from 'crc console --credentials'"
    oc login -u kubeadmin -p "$pass" https://api.crc.testing:6443 --insecure-skip-tls-verify=true
  fi
}

# Two contexts on one cluster. The server validates each context name at
# startup and scopes namespaces per context, so this is enough to exercise the
# multi-context path end to end.
make_contexts() {
  log "creating kubeconfig contexts crc-west and crc-east"
  local cluster user
  cluster=$(oc config view --minify -o jsonpath='{.contexts[0].context.cluster}')
  user=$(oc config view --minify -o jsonpath='{.contexts[0].context.user}')
  oc config set-context crc-west --cluster="$cluster" --user="$user" --namespace=west >/dev/null
  oc config set-context crc-east --cluster="$cluster" --user="$user" --namespace=east >/dev/null
}

install_skupper() {
  log "installing Skupper $SKUPPER_VERSION"
  local base="https://github.com/skupperproject/skupper/releases/download/${SKUPPER_VERSION}"
  oc apply -f "${base}/skupper-crds.yaml"
  oc apply -f "${base}/skupper-cluster-scope.yaml"
  oc -n skupper rollout status deploy --timeout=300s
}

create_sites() {
  log "creating sites west (linkAccess=${LINK_MODE}) and east"
  oc new-project west >/dev/null 2>&1 || oc project west >/dev/null
  oc new-project east >/dev/null 2>&1 || oc project east >/dev/null
  oc apply -f - <<EOF
apiVersion: skupper.io/v2alpha1
kind: Site
metadata:
  name: west
  namespace: west
spec:
  linkAccess: ${LINK_MODE}
EOF
  oc apply -f - <<'EOF'
apiVersion: skupper.io/v2alpha1
kind: Site
metadata:
  name: east
  namespace: east
spec: {}
EOF
  oc -n west wait --for=condition=Ready site/west --timeout=300s
  oc -n east wait --for=condition=Ready site/east --timeout=300s
}

# The v2 handshake with kubectl only: west issues an AccessGrant, the controller
# fills its status, east redeems it as an AccessToken and gets a Link.
link_sites() {
  if oc -n east get link east-to-west >/dev/null 2>&1; then
    log "link already exists"; return
  fi
  log "issuing AccessGrant in west"
  oc apply -f - <<'EOF'
apiVersion: skupper.io/v2alpha1
kind: AccessGrant
metadata:
  name: west-grant
  namespace: west
spec:
  redemptionsAllowed: 10
  expirationWindow: 1h
EOF
  local url i=0
  until url=$(oc -n west get accessgrant west-grant -o jsonpath='{.status.url}' 2>/dev/null) && [ -n "$url" ]; do
    i=$((i+1))
    if [ "$i" -gt 60 ]; then
      oc -n west get accessgrant west-grant -o yaml >&2
      die "AccessGrant never published a url.
With LINK_MODE=route this usually means the router hostname does not resolve
inside the cluster. Retry with: LINK_MODE=local $0 up"
    fi
    sleep 5
  done
  log "redeeming AccessToken in east (url $url)"
  URL="$url" \
  CODE="$(oc -n west get accessgrant west-grant -o jsonpath='{.status.code}')" \
  CA="$(oc -n west get accessgrant west-grant -o jsonpath='{.status.ca}')" \
  python3 -c 'import os,json;print(json.dumps({"apiVersion":"skupper.io/v2alpha1","kind":"AccessToken","metadata":{"name":"east-to-west","namespace":"east"},"spec":{"url":os.environ["URL"],"code":os.environ["CODE"],"ca":os.environ["CA"]}}))' \
    | oc apply -f -
  oc -n east wait --for=condition=Ready link --all --timeout=300s
}

# nginx-unprivileged, not nginx: the restricted-v2 SCC assigns a random high UID
# and forbids binding port 80, so the stock image would crash-loop.
deploy_workload() {
  log "deploying backend in east + connector, listener in west"
  oc -n east create deployment backend \
    --image=docker.io/nginxinc/nginx-unprivileged:alpine --port=8080 \
    --dry-run=client -o yaml | oc apply -f -
  oc apply -f - <<'EOF'
apiVersion: skupper.io/v2alpha1
kind: Connector
metadata:
  name: backend
  namespace: east
spec:
  routingKey: backend
  port: 8080
  selector: app=backend
---
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
  oc -n east rollout status deploy/backend --timeout=180s
}

up() {
  require
  check_memory
  start_cluster
  make_contexts
  install_skupper
  create_sites
  link_sites
  deploy_workload
  status
  local_hint
}

local_hint() {
  cat <<EOF

Run the server on your Mac against both contexts:

  eval "\$(crc oc-env)"
  SKUPPER_MCP_ALLOWED_CONTEXTS=crc-west,crc-east \\
  SKUPPER_MCP_ALLOWED_NAMESPACES=crc-west/west,crc-east/east \\
  go run ./cmd/skupper-mcp-server

Or run it inside the cluster with a ServiceAccount instead:

  $0 deploy

EOF
}

# In-cluster deployment: build for the cluster's architecture, push to the
# internal registry, apply the overlay, then replace the base NetworkPolicy with
# one that matches CRC's actual addressing.
deploy() {
  require
  eval "$(crc oc-env)"
  local arch registry
  arch=$(oc get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')
  log "cluster architecture: $arch"

  oc new-project "$DEPLOY_NS" >/dev/null 2>&1 || oc project "$DEPLOY_NS" >/dev/null

  log "exposing the internal registry"
  oc patch configs.imageregistry.operator.openshift.io/cluster --type=merge \
    -p '{"spec":{"defaultRoute":true}}'
  local i=0
  until registry=$(oc -n openshift-image-registry get route default-route \
                     -o jsonpath='{.spec.host}' 2>/dev/null) && [ -n "$registry" ]; do
    i=$((i+1)); [ "$i" -gt 30 ] && die "registry route never appeared"; sleep 5
  done

  log "building skupper-mcp-server for linux/$arch"
  podman build --platform "linux/$arch" --build-arg "TARGETARCH=$arch" \
    -t "${registry}/${DEPLOY_NS}/skupper-mcp-server:latest" "$REPO_ROOT"

  log "pushing to $registry"
  podman login --tls-verify=false -u kubeadmin -p "$(oc whoami -t)" "$registry"
  podman push --tls-verify=false "${registry}/${DEPLOY_NS}/skupper-mcp-server:latest"

  log "applying the crc overlay"
  oc apply -k "$REPO_ROOT/deploy/kustomize/overlays/crc"
  oc apply -k "$REPO_ROOT/deploy/kustomize/overlays/crc/watched-rbac"

  # The base NetworkPolicy is documented as an example: it admits ingress only
  # from a namespace that does not exist here, and its egress rule excludes the
  # private ranges that CRC's API server and DNS actually live in. Both are
  # replaced with the addresses this cluster really uses.
  local api_ip dns_ip
  api_ip=$(oc -n default get svc kubernetes -o jsonpath='{.spec.clusterIP}')
  dns_ip=$(oc -n openshift-dns get svc dns-default -o jsonpath='{.spec.clusterIP}')
  log "rewriting the NetworkPolicy for CRC (api $api_ip, dns $dns_ip)"
  oc apply -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: skupper-mcp-server
  namespace: ${DEPLOY_NS}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: skupper-mcp-server
  policyTypes: [Ingress, Egress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: openshift-ingress
      ports:
        - protocol: TCP
          port: 8080
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: openshift-dns
      ports:
        - {protocol: UDP, port: 53}
        - {protocol: TCP, port: 53}
        - {protocol: UDP, port: 5353}
        - {protocol: TCP, port: 5353}
    - to:
        - ipBlock:
            cidr: ${api_ip}/32
      ports:
        - protocol: TCP
          port: 443
EOF
  oc -n "$DEPLOY_NS" rollout status deploy/skupper-mcp-server --timeout=180s
  local host; host=$(oc -n "$DEPLOY_NS" get route skupper-mcp-server -o jsonpath='{.spec.host}')
  cat <<EOF

Running in-cluster with a ServiceAccount, no kubeconfig:

  MCP endpoint: https://${host}/mcp
  Logs:         oc -n ${DEPLOY_NS} logs -f deploy/skupper-mcp-server

The Route has no authentication in front of it: SKUPPER_MCP_AUTH_ENABLED is
false in this overlay. That is acceptable on a laptop VM and nowhere else. To
test the OAuth path, point the overlay at an issuer and set AUTH_ENABLED=true.

EOF
}

status() {
  eval "$(crc oc-env)"
  for ns in west east; do
    log "namespace $ns"
    oc -n "$ns" get sites,links,listeners,connectors 2>/dev/null || true
  done
}

break_network() {
  eval "$(crc oc-env)"
  log "deleting the east connector to produce a broken network"
  oc -n east delete connector backend --ignore-not-found
  status
}

down() {
  log "stopping CRC (data is kept; use 'crc delete' to remove the VM)"
  crc stop
}

case "${1:-up}" in
  up)     up ;;
  deploy) deploy ;;
  local)  local_hint ;;
  status) status ;;
  break)  break_network ;;
  down)   down ;;
  *)      echo "usage: $0 {up|deploy|local|status|break|down}" >&2; exit 1 ;;
esac
