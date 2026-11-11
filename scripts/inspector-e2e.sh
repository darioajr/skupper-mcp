#!/usr/bin/env bash
# End-to-end check driven by MCP Inspector's CLI mode.
#
# It starts a loopback fixture API server, starts the real skupper-mcp-server
# against it over Streamable HTTP, then uses MCP Inspector to list the tools and
# call each one. It asserts on Inspector's own output, so a protocol-level
# regression that the Go tests cannot see still fails here.
#
# Requires: go, node/npx. Run from the repository root:
#   ./scripts/inspector-e2e.sh
set -euo pipefail

cd "$(dirname "$0")/.."

STUB_PID=""
SERVER_PID=""
WORKDIR="$(mktemp -d)"

cleanup() {
  # Job-control notices from the shell are suppressed: both children are
  # expected to die here, and the message reads like a failure otherwise.
  set +m
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi
  if [ -n "$STUB_PID" ]; then kill "$STUB_PID" 2>/dev/null || true; wait "$STUB_PID" 2>/dev/null || true; fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "==> building"
go build -o "$WORKDIR/skupper-mcp-server" ./cmd/skupper-mcp-server
go build -o "$WORKDIR/stubapi" ./test/stubapi

echo "==> starting the fixture API server"
"$WORKDIR/stubapi" \
  -addr 127.0.0.1:0 \
  -fixtures internal/kubernetes/testdata \
  -write-kubeconfig "$WORKDIR/kubeconfig" \
  > "$WORKDIR/stub.url" 2> "$WORKDIR/stub.log" &
STUB_PID=$!

for _ in $(seq 1 100); do
  [ -s "$WORKDIR/stub.url" ] && break
  sleep 0.1
done
STUB_URL="$(head -1 "$WORKDIR/stub.url")"
[ -n "$STUB_URL" ] || fail "the fixture API server did not start; see $WORKDIR/stub.log"
echo "    fixture API server at $STUB_URL"

echo "==> starting skupper-mcp-server"
PORT=18080
KUBECONFIG="$WORKDIR/kubeconfig" \
SKUPPER_MCP_TRANSPORT=http \
SKUPPER_MCP_ADDR="127.0.0.1:$PORT" \
SKUPPER_MCP_ALLOWED_CONTEXTS="west-cluster,east-cluster" \
SKUPPER_MCP_ALLOWED_NAMESPACES="west-cluster/west,east-cluster/east" \
SKUPPER_MCP_LOG_FORMAT=text \
  "$WORKDIR/skupper-mcp-server" > "$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 100); do
  if curl -sf "http://127.0.0.1:$PORT/healthz" > /dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -sf "http://127.0.0.1:$PORT/healthz" > /dev/null \
  || fail "the server did not become healthy; see $WORKDIR/server.log"
echo "    server healthy on 127.0.0.1:$PORT"

ENDPOINT="http://127.0.0.1:$PORT/mcp"
inspect() {
  npx -y @modelcontextprotocol/inspector --cli \
    --transport http --server-url "$ENDPOINT" "$@"
}

# A producer piped to grep -q can receive SIGPIPE after an early match, making
# pipefail report a false failure for large, pretty-printed Inspector responses.
# Here-strings let grep own the status without a concurrent producer.
echo "==> tools/list"
TOOLS="$(inspect --method tools/list)"
for tool in list_sites list_links list_listeners list_connectors get_topology diagnose_connectivity; do
  grep -q "\"$tool\"" <<<"$TOOLS" || fail "Inspector did not report the tool $tool"
  echo "    $tool"
done

echo "==> tools/call"
SITES="$(inspect --method tools/call --tool-name list_sites)"
grep -q '"name": *"west"' <<<"$SITES" || fail "list_sites did not return the west site"
grep -q '"name": *"east"' <<<"$SITES" || fail "list_sites did not return the east site"
echo "    list_sites returned both sites"

LINKS="$(inspect --method tools/call --tool-name list_links)"
grep -q '"remoteSiteName": *"east"' <<<"$LINKS" || fail "list_links did not resolve the remote site"
echo "    list_links resolved the remote site"

LISTENERS="$(inspect --method tools/call --tool-name list_listeners)"
grep -q '"routingKey": *"backend"' <<<"$LISTENERS" || fail "list_listeners did not return backend"
echo "    list_listeners returned the routing key"

CONNECTORS="$(inspect --method tools/call --tool-name list_connectors)"
grep -q '"routingKey": *"backend"' <<<"$CONNECTORS" || fail "list_connectors did not return backend"
echo "    list_connectors returned the routing key"

TOPOLOGY="$(inspect --method tools/call --tool-name get_topology)"
grep -q '"routingKey": *"backend"' <<<"$TOPOLOGY" || fail "get_topology did not group the backend routing key"
echo "    get_topology grouped the routing key"

DIAGNOSIS="$(inspect --method tools/call --tool-name diagnose_connectivity \
  --tool-arg sourceContext=west-cluster sourceNamespace=west \
             targetContext=east-cluster targetNamespace=east routingKey=backend)"
grep -q '"status": *"HEALTHY"' <<<"$DIAGNOSIS" || fail "diagnose_connectivity was not HEALTHY: $DIAGNOSIS"
grep -q 'No traffic was sent' <<<"$DIAGNOSIS" || fail "the diagnosis is missing its in-band disclaimer"
echo "    diagnose_connectivity returned HEALTHY with its disclaimer"

echo "==> resources/list"
RESOURCES="$(inspect --method resources/list)"
for uri in "skupper://sites" "skupper://links" "skupper://listeners" "skupper://connectors" "skupper://topology"; do
  grep -q "$uri" <<<"$RESOURCES" || fail "Inspector did not report the resource $uri"
done
echo "    all five resources published"

echo "==> checking that no Secret name reached any response"
ALL="$SITES$LINKS$LISTENERS$CONNECTORS$TOPOLOGY$DIAGNOSIS"
for uri in "skupper://sites" "skupper://links" "skupper://listeners" "skupper://connectors" "skupper://topology"; do
  ALL="$ALL$(inspect --method resources/read --uri "$uri")"
done
for secret in link-west-to-east-secret listener-backend-secret connector-backend-secret; do
  grep -q "$secret" <<<"$ALL" && fail "the Secret name $secret leaked into a response"
done
echo "    no Secret name leaked"

echo
echo "PASS: MCP Inspector drove all six tools and all five resources."
