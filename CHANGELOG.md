# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- Patch GO-2026-6859 (`net/http`) with Go 1.26.11 in CI, production and
  development images.
- Raise the pinned compiler to Go 1.26.11 and update `golang.org/x/net` and
  its dependencies to patched releases identified by the vulnerability scan.
- **Breaking:** authenticated HTTP requires RFC 8693 delegated user token
  exchange into a separate Kubernetes audience. Native Kubernetes RBAC is
  enforced with that credential; no ServiceAccount/kubeconfig fallback exists.
  Authenticated stdio is refused. See the operator guide for IdP setup.
- Enforce per-user/session MCP read budgets, bounded state, whole-invocation
  deadlines and burst cooldown circuits with structured alert-owner events.
- Rebuild verified identity/credentials on each request, preserve stable subject
  IDs and audit schema/transport rejections plus resolved attempted read scopes.
- Demo uses one replica, removes the ServiceAccount's Skupper grant and keeps
  illustrative native-user RBAC in a separately rendered namespace overlay.

### Added

- Version-tag CI publication of the Linux amd64 container to
  `quay.io/darioajr/skupper-mcp-server` and
  `ghcr.io/darioajr/skupper-mcp-server`, gated on all validation jobs, with SBOM,
  build provenance, version/revision labels and digest output. Document Quay
  robot credentials and GHCR access setup.

- Non-root development container with the pinned Go compiler, race-test tooling,
  lint/security checks, kubectl, Node.js and gopls; no host Go installation needed.
- Operator/user guide, control-to-evidence map, pending deployment record,
  onboarding/offboarding and periodic/version-change review procedures.
- Regression tests for native rights separation, token refresh, failed exchange
  without fallback, malformed-call budgets, circuit recovery and broad-read audit.

### Fixed

- Correct the release publishing repository guard and public repository links
  after the rename to `darioajr/skupper-mcp`.
- CI SBOM generation disables Anchore's built-in artifact and release uploads;
  the dedicated artifact step retains the SPDX file without broader API permissions.
- Devcontainer checks the host Podman engine before creation; document the
  application-scoped VS Code executable path and macOS machine startup to avoid
  `spawn podman ENOENT` in GUI-launched editors.
- CI explicitly installs and verifies the `toolchain` compiler pin rather than
  selecting the older `go` language minimum. Refresh actions for Node.js 24 and
  use a golangci-lint action that supports v2.
- Inspector assertions avoid producer/`grep -q` pipelines, which can report a
  false failure under `pipefail` when a large response triggers SIGPIPE.
- Formatting check fails when gofmt is unavailable; manifest checks include
  nested watched-namespace RBAC overlays.

### Deployment requirements

- Legal/security/source-owner approvals, classification, CMDB/PIA confirmation,
  registry submission, central log retention/export and real alert delivery are
  pending operator evidence, not approvals supplied by this release.
- Limits are process-local; shared enforcement is required before scaling.

## [0.1.0] — 2026-08-26

First release. A read-only MCP server for observing a Skupper v2 application
network.

### Added

- **Six MCP tools**: `list_sites`, `list_links`, `list_listeners`,
  `list_connectors`, `get_topology`, and `diagnose_connectivity`. All return
  typed JSON; none accept a connection parameter, a file path, or an arbitrary
  resource kind.
- **Five MCP resources** — `skupper://sites`, `skupper://links`,
  `skupper://listeners`, `skupper://connectors`, `skupper://topology` — served
  from the same application service as the tools, so the two surfaces cannot
  disagree.
- **Structured topology**: sites as nodes, links as edges, listeners and
  connectors attached to their sites, and routing-key groups pairing the two
  sides. A site referenced by a link but outside the allowed scope appears as an
  unobserved node, so the edge of the view is explicit.
- **Deterministic connectivity diagnosis** over seven checks: site readiness,
  link path, link health, source listener, target connector, endpoint match, and
  workload binding. Every piece of evidence is labelled `DECLARED`, `OBSERVED`,
  `INFERRED`, or `ABSENT`, and every response carries an in-band statement of
  what was *not* verified.
- **Multi-cluster reads** across an allowlist of contexts and namespaces, with
  one unreachable cluster degrading to a warning rather than failing the call.
- **Streamable HTTP transport** with cross-origin protection and idle session
  expiry, plus a stdio transport for desktop clients and MCP Inspector.
- **OAuth 2.0 / OIDC bearer authentication** via the MCP Go SDK's auth package
  and `github.com/coreos/go-oidc/v3`, with RFC 9728 protected resource metadata
  and two domain roles, `viewer` and `diagnostician`.
- **Structured audit log**: one record per tool invocation carrying tool,
  subject, scopes, resolved scope, duration, outcome, and result cardinality —
  and never a Kubernetes object.
- **Credential redaction** applied independently to responses and to logs.
- **UBI Micro container image** running non-root with a read-only root
  filesystem, the binary as PID 1, and no shell or package manager.
- **Kustomize manifests** with `dev` and `demo` overlays, minimal read-only
  RBAC bound per namespace, startup/readiness/liveness probes, and an example
  NetworkPolicy.
- **CI** covering `gofmt`, `go vet`, race-detector tests, `golangci-lint`,
  `govulncheck`, Kustomize rendering, an image build with an SBOM, and an
  end-to-end run driven by MCP Inspector.

### Security

- Read-only by construction: no create, update, patch, or delete verb exists
  anywhere in the codebase, and `SKUPPER_MCP_READ_ONLY=false` fails startup.
- No `Secret` is ever read, and `tlsCredentials` is dropped at decode time so no
  Secret name can reach a response.
- Connector pod names and IP addresses are reduced to a count.
- The allowlist is evaluated before any Kubernetes call; a denied scope contacts
  no API server.
- Cluster-wide list calls are refused even if an empty namespace reaches the
  repository.
- Configuration validation refuses a non-loopback bind when authentication is
  disabled, unless `SKUPPER_MCP_ALLOW_UNAUTHENTICATED_REMOTE=true` acknowledges
  it — which the server then warns about at startup.
- `go.mod` pins a patched toolchain; `govulncheck ./...` reports nothing.

### Known limitations

- The server reports resource state, not network reality. It sends no traffic,
  measures no latency, and validates no TLS chain.
- Redaction of free-form controller messages is best-effort and deliberately
  does not scrub hyphenated tokens, which would destroy useful diagnostics.
- Verified against fixtures and a stub API server. Not yet verified against a
  live Skupper v2 cluster; see [ROADMAP.md](ROADMAP.md).

[Unreleased]: https://github.com/darioajr/skupper-mcp/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/darioajr/skupper-mcp/releases/tag/v0.1.0
