# AGENTS.md

Working notes for anyone — human or agent — changing this repository.

## What this is

A read-only MCP server that observes a Skupper v2 application network through
the Kubernetes API. Six tools, five resources, no write path.

## Commands

```bash
make verify         # everything CI runs; run this before opening a PR
make build          # binary into bin/
make test           # go test -race ./...
make cover          # coverage by function
make lint           # golangci-lint run ./...
make vulncheck      # govulncheck ./...
make manifests      # render every Kustomize overlay
make inspector-e2e  # drive the real binary with MCP Inspector, no cluster needed
make run            # serve on 127.0.0.1:8080; set NAMESPACES=...
make stdio          # serve on stdio
make image          # build the container image
```

`make verify` is `fmt-check vet test lint vulncheck manifests`. It does not
include `inspector-e2e`, which needs Node.

## Layout

```
cmd/skupper-mcp-server/   wiring and signal handling only
internal/domain/          types; imports nothing
internal/application/     use cases; declares the repository interface it needs
internal/kubernetes/      dynamic client, decode structs, domain mapping
internal/mcpserver/       MCP tools, resources, HTTP transport
internal/auth/            roles and bearer token middleware
internal/config/          typed config, validation, allowlist
internal/observability/   logging and the tool audit trail
internal/redact/          credential scrubbing, used by responses and logs
test/e2e/                 full stack against a stub API server
test/stubapi/             fixture API server for external tooling
```

Dependency direction is one way: `mcpserver` → `application` → `domain`.
`kubernetes` implements an interface `application` declares. Nothing points
back up.

## Conventions

- **The domain imports nothing.** No `k8s.io/...`, no `modelcontextprotocol`.
  This is what makes the topology builder and the diagnosis engine testable with
  no cluster. If a change needs the domain to import either, the change belongs
  in a different package.
- **Interfaces live with the consumer.** `SkupperResourceRepository` is declared
  in `internal/application`.
- **`context.Context` first** on anything that does I/O, and honour it. The
  listing loop checks `ctx.Err()` between scopes, not only inside the call.
- **Wrap errors with `%w`** and keep sentinels inspectable. `config.ErrContextNotAllowed`,
  `config.ErrNamespaceNotAllowed`, `application.ErrInvalidRequest`, and
  `auth.ErrForbidden` are matched with `errors.Is` by the tool layer to classify
  an audit outcome. Breaking that chain silently mislabels the audit log.
- **Constructors validate.** `NewService`, `NewRepository`, `NewHTTPServer`, and
  `mcpserver.New` all reject nil or nonsensical dependencies rather than
  panicking later.
- **No global state.** No singletons, no `init()` with side effects, no service
  locator.
- **No unmanaged goroutines.** Reads are sequential on purpose: the item cap has
  to be enforced across scopes, and fan-out would trade a deterministic, ordered,
  capped result for concurrency this workload has not been shown to need.
- **No `panic` for operational failure.**
- **Typed JSON out.** Avoid `map[string]any` outside the unstructured decode
  boundary. Empty slices marshal as `[]`, never `null`.
- **Comments record decisions.** Why a rule exists, what it deliberately does
  not do, what would break if it changed. A comment that restates the code
  should be deleted.

## Security rules

These are not style preferences. A change that breaks one should not be merged.

1. **No write verb, anywhere.** Not in the repository interface, not in the
   dynamic client calls, not behind a flag.
2. **No `Secret` access.** No code path names the resource; the RBAC does not
   grant it.
3. **`tlsCredentials` stays out of the decode structs.** Adding it, even
   "temporarily", puts a Secret name one `json.Marshal` away from a client.
4. **No tool parameter may name a connection, a file, a command, or a resource
   kind.** A client selects a cluster by logical context name from the
   allowlist, and nothing else.
5. **The allowlist is checked before any I/O**, and a denial is reported
   explicitly rather than as an empty result — a silently empty listing would
   let a client map the allowlist by inference.
6. **Never claim a verification that did not happen.** The diagnosis reports
   observed resource state. It does not send traffic, measure latency, or
   validate TLS, and every response says so in-band. Adding a check that implies
   otherwise, in wording or in structure, is a correctness bug.
7. **The audit log records scope, never content.** Tool name, subject, resolved
   context and namespace, duration, outcome, count. No Kubernetes object.

Tests pin all seven. If one starts failing, the fix is the code, not the test.

## The Skupper contract

This project does not import the Skupper Go module; see
[ADR-0001](docs/adr/0001-skupper-api-access-strategy.md) for why. The CRD
contract lives in two places, and they must stay in step:

- `internal/kubernetes/skupperapi/types.go` — the decode structs
- `internal/kubernetes/testdata/*.yaml` — the fixtures that pin them

Adding a Skupper field means editing both, plus an assertion in
`internal/kubernetes/mapper_test.go`. A field that leaves the process also needs
a case in `internal/mcpserver/leak_test.go`.

## Testing

- Table-driven, with failure messages that say what was expected and why it
  matters.
- Test behaviour and contracts. A test that asserts a mock was called proves
  nothing and will be asked to change.
- New diagnosis or topology branches go in the existing tables in
  `internal/application`.
- `test/e2e` runs the whole stack — real MCP client, real HTTP, real dynamic
  Kubernetes client — against a stub API server. Add to it when a change spans
  layers.
- Coverage is roughly 76% overall, concentrated in the domain, application,
  security, and redaction paths. Do not chase the number; do not let those four
  areas regress.

## Definition of done

- `gofmt -l .` prints nothing
- `go vet ./...` passes
- `go test -race ./...` passes
- `golangci-lint run` passes
- `govulncheck ./...` reports nothing, or the limitation is documented in
  `SECURITY.md`
- `go build ./cmd/skupper-mcp-server` produces a binary with no untracked inputs
- `make manifests` renders every overlay
- `make inspector-e2e` passes if Node is available
- No new TODO without an issue behind it, no skipped test, no hidden failure
- The seven security rules above still hold
- ADRs, `README.md`, `SECURITY.md`, `CHANGELOG.md`, and `ROADMAP.md` reflect
  what the code now does

## Things that are the way they are on purpose

- **Reads are sequential.** See the conventions above.
- **BFS, not lowest cost, for the link path.** An operator reads a path for hop
  count. Cost is reported per hop but is not the search metric, because a
  read-only observer cannot know how the router actually weighted it.
- **Links traverse in both directions.** A Skupper link names one initiator but
  carries traffic both ways once established, so reachability must not depend on
  which side happens to hold the `Link` resource.
- **Differing ports between a listener and a connector are not a fault.**
  Skupper routes on the routing key; the connector port is the backend port. It
  is reported for review, labelled as such.
- **An unset protocol never mismatches.** It is the Skupper default.
- **Unknown status values pass through unchanged.** A newer controller must not
  be able to make this server fail.
- **Redaction skips hyphenated tokens.** Kubernetes names, pod suffixes and
  UUIDs share that shape; redacting them would destroy the diagnostics.
