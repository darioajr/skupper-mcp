# Contributing

Thanks for considering it. This is a small, independent project, so the process
is light.

## Before you start

For anything beyond a bug fix or a typo, open an issue first. The scope of this
project is deliberately narrow — see [Scope](#scope) below — and it is kinder to
find out that an idea is out of scope before you have written it.

## Getting set up

You can develop without installing Go on your machine using the repository's
devcontainer. Install Podman and the VS Code Dev Containers
extension, open this folder and select **Dev Containers: Reopen in Container**.
Set `dev.containers.dockerPath` in **User Settings (JSON)** to the absolute
path printed by `command -v podman`. For the macOS Podman installer:

```json
{
  "dev.containers.dockerPath": "/opt/podman/bin/podman"
}
```

This is an application-scoped setting: workspace settings and the container's
`customizations.vscode.settings` cannot select the host engine. The absolute
path avoids `spawn podman ENOENT` when a GUI-launched editor has a different
`PATH`. On macOS/Windows, run `podman machine start` before reopening (run
`podman machine init` first if no machine exists), then check `podman info`.
The devcontainer checks Podman availability on the host before creation.
After changing User Settings, run **Developer: Reload Window**, then
**Dev Containers: Reopen in Container**. See the
[VS Code Podman instructions](https://code.visualstudio.com/remote/advancedcontainers/docker-options#_podman).

The image includes the pinned Go compiler, GCC for race tests, Make, Git,
golangci-lint, govulncheck, kubectl/Kustomize, Node.js/npm for MCP Inspector and
gopls for the Go editor extension. Dependency download and checksum verification
run once when the workspace is created. The first build requires network access.
Setup trusts only the opened workspace in the container's Git configuration so
bind mounts with a different host UID still support Go's VCS metadata.
All commands below run in the container terminal:

```bash
make verify
make build
make inspector-e2e
```

The development user is non-root. Port 8080 is forwarded by the editor when you
run the MCP server; no server starts automatically. This configuration defines
no mounts for host kubeconfig, credentials or the container runtime socket.
Editor-managed Git/SSH credential forwarding follows your editor settings.
The fixture-based Inspector test needs no cluster. To work with a real approved
cluster, configure credentials explicitly inside the development environment
and follow the [operator guide](docs/operator-guide.md).

The devcontainer supports Linux amd64 and arm64. When updating the compiler,
keep `go.mod`, the production Dockerfile and `.devcontainer/Dockerfile` in step,
then select **Dev Containers: Rebuild Container**. Startup refuses a compiler
different from the `toolchain` pin. Tool versions are declared as build arguments
in the development Dockerfile; the language server is pinned to a release that
supports the project's Go version.

Alternatively, use a Podman terminal without an editor:

```bash
podman build -t skupper-scope-dev -f .devcontainer/Dockerfile .devcontainer
podman run --rm -it -v "$PWD:/workspaces/skupper-scope" -w /workspaces/skupper-scope skupper-scope-dev bash
bash .devcontainer/post-create.sh
make verify
```

Image building (`make image`) runs on the host, where the container engine is
available; the devcontainer deliberately has no engine socket. On Linux, use
the editor's UID adjustment or adapt your runtime's user mapping if bind-mounted
files are not writable by the development user.

For development directly on the host, install the exact Go toolchain in
`go.mod`, GCC, Make, golangci-lint, govulncheck and kubectl. Node.js is also
needed for the Inspector check.

```bash
git clone https://github.com/darioajr/skupper-scope
cd skupper-scope
make verify
```

`make verify` runs everything CI runs: `gofmt`, `go vet`, the tests with the
race detector, `golangci-lint`, `govulncheck`, and a render of every Kustomize
overlay.

You do not need a Kubernetes cluster to develop on this. The domain and
application layers are tested against an in-memory repository, and
`make inspector-e2e` drives the real binary against a loopback fixture API
server using MCP Inspector.

If you do have a Skupper v2 cluster:

```bash
NAMESPACES=west make run
```

## Scope

The MVP is **read-only** and stays that way. Pull requests that add a write path
will not be merged, however carefully guarded, because "this server cannot
change your network" is the property that makes it safe to hand to a model.

Also out of scope, and tracked in [ROADMAP.md](ROADMAP.md) rather than
implemented speculatively: AccessToken handling, self-remediation, Ansible,
LLM-specific integrations, AI log analysis, invented traffic or latency metrics,
Skupper v1 support, and a web dashboard.

## Conventions

The Go conventions this codebase holds itself to are listed in
[AGENTS.md](AGENTS.md). The ones worth repeating here:

- **The domain imports nothing.** No Kubernetes, no MCP. If a change makes
  `internal/domain` depend on either, the change is in the wrong package.
- **Interfaces live with their consumer.** `SkupperResourceRepository` is
  declared in `internal/application`, not in `internal/kubernetes`.
- **Errors carry context and stay inspectable.** Wrap with `%w`, and preserve
  the sentinels — the allowlist denials in particular are matched with
  `errors.Is` by the tool layer to decide an audit outcome.
- **Comments explain why, not what.** A comment restating the code is noise; a
  comment recording a decision, a constraint, or a surprise is the point.

## Tests

Test behaviour and contracts, not mocks. A test that asserts a mock was called
proves nothing.

- New branches in the topology builder or the diagnosis engine belong in the
  existing table-driven tests in `internal/application`.
- New Skupper fields need a fixture in `internal/kubernetes/testdata` and an
  assertion in the mapper tests. The fixtures are what pin the CRD contract this
  project assumes; see [ADR-0001](docs/adr/0001-skupper-api-access-strategy.md).
- Anything that touches what leaves the process needs a case in
  `internal/mcpserver/leak_test.go`.

Failure messages should say what was expected and why it matters, not just that
something differed.

## Commits and pull requests

Write commit messages that explain the reasoning, not the diff. One logical
change per commit.

In the pull request, say what you changed, why, and what you verified. If you
could not verify something — no cluster, no OpenShift, no identity provider —
say that too. An honest gap is more useful than a confident guess.

## Decisions

Architectural decisions live in [docs/adr/](docs/adr/). If your change reverses
one, update the ADR in the same pull request rather than leaving the record
wrong.

## Code of conduct

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

Contributions are licensed under [Apache-2.0](LICENSE), the same as the project.
