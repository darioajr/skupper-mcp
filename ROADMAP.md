# Roadmap

The future capabilities below are **deliberately not implemented**. It is recorded so the
absence is a decision rather than an oversight, and so nobody adds speculative
code for it before it is needed.

Stable version tags now publish the Linux amd64 image to Quay and GHCR after
all CI checks pass. See the README for registry setup and release commands.
Multi-architecture publication and automatic GitHub Release creation remain
future work; OpenShift deployment and environment configuration are manual.

## Near term

### Verification against a live cluster

The MVP is verified against sanitized fixtures and a stub API server that serves
the real Kubernetes list endpoints. That covers the group/version/resource
paths, the unstructured decoding, and the mapping — but not a real Skupper v2
controller's behaviour over time.

What is needed: a documented Kind or OpenShift setup with two linked sites, and
a checklist of what each tool should return against it. This is the highest
value item on the list, because it is the only one that can invalidate the CRD
contract in [ADR-0001](docs/adr/0001-skupper-api-access-strategy.md).

### `envtest` coverage of the repository

The repository is tested against a fake dynamic client and a stub HTTP server.
`envtest` with the real Skupper CRDs installed would catch schema drift that
neither can.

### Live delegated identity integration

The operator guide documents the required RFC 8693 exchange contract and native
identity mapping. The demo retains placeholder IdP endpoints and groups. A
version-specific Keycloak/cluster setup with live validation of signed tokens,
delegation, revocation and native rights equivalence remains deployment work.
Stub integration tests do not certify a real identity provider.

### Deployment approval and operational evidence

Complete the pending record in `docs/compliance.md`: legal/ESS assessment,
data-owner classification, source CMDB/PIA confirmation, registry submission,
central immutable logs, delivered alerts and the first monthly audit. These
cannot be inferred from a source checkout or CI result.

## Explicitly out of scope for the MVP

These were considered and excluded. Several are excluded permanently.

### Any write operation — permanently out of scope

Creating or removing links, exposing services, creating listeners or connectors.

"This server cannot change your network" is the property that makes it safe to
hand to a model. A write path guarded by a flag is still a write path, and the
guarantee would become a promise instead of a fact. If write operations are ever
wanted, they belong in a separate server with its own name, its own RBAC, and
its own audit trail.

### AccessToken and AccessGrant handling — permanently out of scope

Generating, reading, or transporting an AccessToken means handling a credential.
This server's central claim is that it never does.

### Self-remediation

Automatically fixing what the diagnosis found. This is a write path wearing a
helpful hat.

### Ansible execution

Running playbooks in response to a diagnosis. Out of scope for the same reason,
and it would put arbitrary command execution behind an MCP tool.

### Integration with a specific LLM

The server speaks MCP. Which model is on the other end is not its concern, and
coupling to one would narrow it for no gain.

### AI-driven log analysis

Reading router or application logs and summarising them. It would require log
access this server's RBAC deliberately does not grant, and it would blur the
line the diagnosis works hard to keep sharp between observed state and
inference.

### Traffic and latency metrics

Skupper exposes some traffic data through its observer component. Surfacing it
would be legitimate — but only by reading real measurements, never by inventing
them. Until that data source is consumed properly, the honest answer is that
this server does not measure anything, and every diagnosis says so.

### Simultaneous Skupper v1 and v2 support

v1 and v2 have different resource models. Supporting both would mean two
mappers, two topology builders, and a diagnosis engine that has to explain which
model it is reasoning about. v2 only, until v1 stops mattering entirely.

### Web dashboard

The MCP client renders. `get_topology` returns typed JSON precisely so the
client can decide how to draw it; shipping a dashboard would duplicate that
decision in the wrong place.

## Possible later

- **Watch-based caching.** Tool calls currently list on demand. An informer
  cache would cut latency on a large network, at the cost of serving slightly
  stale data — which, for a diagnostic tool, is a real trade-off rather than an
  obvious win.
- **Prompts.** The MCP prompt primitive could package common investigations
  ("why can't A reach B on key K") as reusable templates.
- **Distributed/persistent invocation limits.** User/session budgets and burst
  circuits already run in process. Shared enforcement across replicas/restarts
  still requires a gateway or future shared state implementation.
- **Resource subscriptions.** Notifying a client when a site or link changes
  state, using the SDK's `ResourceUpdated` support.
