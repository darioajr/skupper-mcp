# Security Policy

## Reporting a vulnerability

Report privately through GitHub's ["Report a vulnerability"][advisory] form on
this repository. Please do not open a public issue for a security problem.

Include what you were able to make the server do, the configuration it was
running under, and the version or commit. A proof of concept helps but is not
required.

Expect an acknowledgement within a week. This is a community project maintained
on volunteer time; there is no commercial support and no guaranteed patch
window. If a fix will take a while, you will be told that rather than left
waiting.

[advisory]: https://github.com/darioajr/skupper-mcp/security/advisories/new

## Supported versions

The `main` branch. There are no maintained release branches yet.

## Design guarantees

These are structural, enforced by the code's shape and pinned by tests:

- **No write path exists.** The repository interface declares only `List`
  methods. No package imports a create, update, patch, or delete verb.
  `SKUPPER_MCP_READ_ONLY=false` fails startup rather than unlocking anything.
- **No Secret is ever read.** No code path names the `secrets` resource, and the
  shipped RBAC does not grant it.
- **`tlsCredentials` never leaves the wire.** The field is absent from the
  decode structs in `internal/kubernetes/skupperapi`, so a Secret name is
  dropped before it can reach a DTO. `TestMappersDropSecretNames` pins this.
- **Pod identity is reduced to a count.** `Connector.status.selectedPods`
  carries pod names and IPs; the mapper keeps only how many there were.
- **No generic escape hatch.** There is no `execute_command`, `run_kubectl`,
  `read_resource`, or `get_secret`. No tool takes a kubeconfig path, a server
  URL, a file path, a shell command, or an arbitrary resource kind.
  `TestNoToolOffersAGenericEscapeHatch` inspects every published input schema
  and fails if one appears.
- **No cluster-wide reads.** An empty namespace argument means "every namespace
  in the allowlist", and the repository refuses a list call with no namespace
  even if one somehow reaches it.
- **The allowlist is checked before any I/O.** A denied context or namespace
  returns a typed error and contacts no API server, which
  `TestDeniedScopeNeverReachesTheAPIServer` verifies against a stub that records
  every request it receives.

## Redaction: what it is and is not

`internal/redact` scrubs credential-shaped material from free-form text, in two
independent places: every response on its way to a client, and every record on
its way to the audit log.

It catches PEM blocks, JWTs, `Bearer` values, words that name themselves as
credentials, and long separator-free base64 or hex runs.

**It deliberately does not** scrub hyphenated tokens. Kubernetes names, pod name
suffixes and UUIDs share that shape, and redacting them would destroy the
diagnostics an operator needs without meaningfully improving safety.

This is a mitigation for one residual case — a Skupper controller, or a client
library error, writing credential material into a status or condition message.
It is not the security guarantee. The guarantee is that nothing
credential-bearing is read in the first place.

## Threat model

### What this server protects against

- A model, or a client acting on a model's behalf, asking for more than it
  should. The tool surface is six domain operations; there is nothing to widen.
- A compromised or careless client trying to reach a namespace or cluster it was
  not granted. The allowlist is checked in the application layer, and the
  per-namespace RoleBindings mean the Kubernetes API would refuse anyway.
- Credential material leaking into a transcript or a log through a status
  message. See above.
- A browser page on another origin driving the server through a user's session.
  The Streamable HTTP handler is wrapped in cross-origin protection, and the
  SDK's localhost DNS-rebinding protection is left enabled.
- An unauthenticated server being exposed off-host *by accident*. Configuration
  validation refuses a non-loopback bind unless authentication is enabled, or
  `SKUPPER_MCP_ALLOW_UNAUTHENTICATED_REMOTE=true` acknowledges it. That flag
  exists because a Pod must bind `0.0.0.0` to receive Service traffic; when it
  is set, the server logs a warning at startup naming what the operator is now
  responsible for bounding.

### What it does not protect against

- **A compromised pod.** Local mode exposes its ambient read grants.
  Authenticated mode handles user credentials and an exchange-client credential;
  protect the IdP delegation policy and the pod as sensitive infrastructure.
- **A malicious or misconfigured identity provider.** MCP scopes and delegated
  identity/groups are trusted as issued. The IdP must preserve native identity
  and forbid privilege expansion during exchange; Kubernetes checks native RBAC.
- **Traffic analysis of the responses.** The topology of a network is itself
  sensitive information. Anyone who can call these tools learns the shape of
  your application network.
- **Distributed denial of service or restart-persistent enumeration.** Item
  caps, whole-invocation/list deadlines, user/session invocation budgets, bounded
  limiter state and a burst circuit limit one process. State is in memory and
  resets on restart. Use one replica or enforce shared budgets at an approved
  gateway. Alert events require an external notification route.

## Authentication and authorization

No cryptography, JWT parsing, or OAuth protocol step is hand-written.

- Bearer token handling, RFC 6750 rejection, and RFC 9728 protected resource
  metadata come from the MCP Go SDK's `auth` package.
- JWKS retrieval and JWT signature, issuer, audience, and expiry validation come
  from `github.com/coreos/go-oidc/v3`.
- Two domain roles, mapped from token scopes: `viewer` (`skupper:read`) and
  `diagnostician` (`skupper:diagnose`), the latter a strict superset. The read
  scope is required at the transport layer; the diagnose scope is enforced per
  tool, so a viewer gets a clear refusal on one tool rather than a 401 on the
  whole session.

Authenticated HTTP requires RFC 8693 token exchange at an operator-approved HTTPS
IdP endpoint, with a distinct Kubernetes audience. Every request reconstructs
identity from verified transport metadata; credentials captured at connection
initialization are not reused after refresh. The stable issuer-validated `sub`
keys user budgets and audit identity. Empty identities are rejected.

A native-user client keeps trusted cluster address/CA settings but strips all
ambient client certificates, tokens, exec plugins and impersonation. Kubernetes
receives the exchanged user token and enforces native RBAC. Exchange refusal or a
native 401/403 is an explicit denial, never an administrative fallback or silent
partial result. Redirects at the exchange endpoint are refused; IdP bodies and
transport error details are not echoed. The exchange client secret is env-only,
not accepted in YAML or MCP inputs. Short-lived token and revocation policy must
be enforced and tested at the IdP; local JWT validation does not check employment
status or guarantee instantaneous revocation.

Unauthenticated stdio/dev/CRC use ambient operator credentials for single-user
testing. Non-loopback unauthenticated HTTP still requires an explicit override
and network isolation. These profiles are not shared enterprise access modes.
Authenticated stdio is refused at configuration validation.

## Supply chain

- `go.mod` pins a `toolchain` directive, and both CI and the Dockerfile set
  `GOTOOLCHAIN=local` so a mismatch fails the build rather than silently
  compiling with a different Go. The local CI setup action explicitly selects
  that pin and checks `go env GOVERSION`; `go-version-file` alone can choose the
  older `go` directive when `GOTOOLCHAIN=local` is set. The pin is raised when a
  standard-library advisory affects this code's call paths — as of go1.26.11
  with the patched network dependencies, `govulncheck ./...`
  reports nothing.
- CI runs `go mod verify`, `go mod tidy` with a diff check, `govulncheck`,
  `gosec` via golangci-lint, and generates an SPDX SBOM for the image.
  The image validation job keeps `contents: read`: Anchore only generates the
  file, and a dedicated artifact step retains it for seven days. Anchore's
  built-in artifact and release uploads are disabled.
- Stable version tag pushes publish the container to Quay and GHCR only after
  every CI validation job succeeds. Registry login and `packages: write` are
  confined to the publishing job in the upstream repository; PR/branch runs do
  not publish. Quay credentials are repository secrets for a scoped robot account;
  GHCR uses `GITHUB_TOKEN`. Protect release tags with repository rules, restrict
  robot access to this image, and rotate its token when access changes.
- Published images include SBOM and minimal build provenance attestations. Use
  the digest from the job summary for deployment; version tags can be overwritten
  by a rerun and are not an immutability boundary. No GitHub Release is created.
- The final image is UBI Micro: no shell, no package manager, no compiler.

The separate `.devcontainer` image is a development workstation with a compiler,
shell and build tools, not a deployment image. It runs as a non-root user and
defines no mounts for host kubeconfig, credentials or the container-engine
socket. Editor-managed Git/SSH forwarding follows the editor's settings. Use
fixture tests by default; connecting it to enterprise systems still requires the
approvals and access boundaries in the operator guide.

## Auditing

Tool/resource invocation records contain stable subject identity, token scopes,
hashed session correlation, attempted resolved context/namespace pairs, requested
scope filters, duration, outcome and cardinality. Scope recording happens before
each sequential repository read and includes failures; a network-wide diagnosis
records transit scopes as well as endpoints. Requested filters are labelled
`requested_context`/`requested_namespace`, not passed off as completed reads.

SDK receiving middleware also audits discovery/list access, unknown calls and
schema rejections. HTTP rejection auditing covers missing/invalid authentication
and malformed requests before dispatch. Unverified callers are anonymous, never
attributed using an untrusted header. No resource bodies, raw headers, argument
blobs or tokens are logged. Verified subject identifiers are preserved even if
their opaque shape resembles a credential; credential redaction still applies
to parameter/error content.

Circuit openings emit `mcp_circuit_open` with a configured alert owner. Configure
central collection, immutable retention, restricted export and alert delivery in
the deployment; the server writes stderr and has no log store or notifier.

See [the operator guide](docs/operator-guide.md), [compliance evidence
record](docs/compliance.md) and [ADR-0004](docs/adr/0004-native-user-access-and-invocation-controls.md).
The repository demonstrates implementation, not organizational approval.
