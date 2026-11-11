# syntax=docker/dockerfile:1

# Match the Node major used by the Inspector CI job. Both stages use the same
# Debian release so the copied Node binary has a compatible libc.
FROM docker.io/library/node:22-bookworm-slim AS node

# Keep this compiler in step with go.mod and the production Dockerfile.
FROM docker.io/library/golang:1.26.10-bookworm AS tools

ENV GOTOOLCHAIN=local

ARG GOLANGCI_LINT_VERSION=v2.12.2
ARG GOVULNCHECK_VERSION=v1.9.0
# Newer gopls releases require Go 1.27; do not silently fetch another compiler.
ARG GOPLS_VERSION=v0.23.0

RUN GOBIN=/opt/devtools go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && GOBIN=/opt/devtools go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION} \
    && GOBIN=/opt/devtools go install golang.org/x/tools/gopls@${GOPLS_VERSION}

FROM docker.io/library/golang:1.26.10-bookworm

ARG KUBECTL_VERSION=v1.36.4

ENV GOTOOLCHAIN=local \
    GOPATH=/home/vscode/go \
    GOCACHE=/home/vscode/.cache/go-build \
    PATH=/home/vscode/go/bin:/usr/local/go/bin:/usr/local/bin:${PATH}

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       bash ca-certificates curl gcc git libc6-dev make openssh-client \
    && rm -rf /var/lib/apt/lists/* \
    && arch="$(dpkg --print-architecture)" \
    && curl --fail --silent --show-error --location --retry 3 \
       "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl" -o /tmp/kubectl \
    && curl --fail --silent --show-error --location --retry 3 \
       "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${arch}/kubectl.sha256" -o /tmp/kubectl.sha256 \
    && (cd /tmp && printf '%s  kubectl\n' "$(cat kubectl.sha256)" | sha256sum --check -) \
    && install -m 0755 /tmp/kubectl /usr/local/bin/kubectl \
    && rm /tmp/kubectl /tmp/kubectl.sha256 \
    && groupadd --gid 1000 vscode \
    && useradd --uid 1000 --gid vscode --create-home --shell /bin/bash vscode \
    && mkdir -p /home/vscode/go/pkg/mod /home/vscode/.cache/go-build \
    && chown -R vscode:vscode /home/vscode

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=tools /opt/devtools/ /usr/local/bin/

RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# The editor mounts the workspace. Do not bake source, kubeconfig, credentials
# or a container-engine socket into this development image.
USER vscode
WORKDIR /workspaces/skupper-scope
CMD ["sleep", "infinity"]
