# syntax=docker/dockerfile:1

# Build stage. The tag must match the `toolchain` directive in go.mod, and
# GOTOOLCHAIN=local makes a mismatch fail the build instead of silently
# downloading a different compiler mid-build.
FROM docker.io/library/golang:1.26.11 AS build

ENV GOTOOLCHAIN=local
WORKDIR /src

# Dependencies are copied first so a source-only change reuses the module cache.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd/ cmd/
COPY internal/ internal/

# CGO is disabled so the result is a static binary that runs on UBI Micro,
# which has no glibc loader path this program needs.
# -trimpath and the empty build id keep the output reproducible.
ARG VERSION=dev
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -buildid=" \
      -o /out/skupper-mcp-server \
      ./cmd/skupper-mcp-server

# Fail the build rather than shipping a dynamically linked binary by accident.
RUN ! ldd /out/skupper-mcp-server 2>/dev/null | grep -q "=>" || \
    (echo "binary is dynamically linked; UBI Micro has no loader for it" && exit 1)

# Runtime stage. UBI Micro has no shell, no package manager, and no compiler.
FROM registry.access.redhat.com/ubi9/ubi-micro:latest

ARG VERSION=dev

LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="skupper-mcp-server" \
      org.opencontainers.image.description="Read-only Model Context Protocol server for observing a Skupper v2 application network" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/darioajr/skupper-mcp"

COPY --from=build /out/skupper-mcp-server /usr/local/bin/skupper-mcp-server

# A fixed high UID that is not in /etc/passwd. OpenShift overrides it with a
# random UID from the namespace range; nothing here depends on the value.
USER 65532:65532

EXPOSE 8080

ENV SKUPPER_MCP_ADDR=0.0.0.0:8080 \
    SKUPPER_MCP_READ_ONLY=true \
    SKUPPER_MCP_IN_CLUSTER=true \
    SKUPPER_MCP_LOG_FORMAT=json

# The binary is PID 1 directly: no shell wrapper, so SIGTERM reaches the
# process and its own graceful shutdown runs.
ENTRYPOINT ["/usr/local/bin/skupper-mcp-server"]
