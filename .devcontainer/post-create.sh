#!/usr/bin/env bash
set -euo pipefail

# Fail explicitly when the project pin changes instead of downloading a second
# compiler behind the developer's back. Rebuilding uses the updated image pin.
expected="$(awk '$1 == "toolchain" { print $2 }' go.mod)"
actual="$(go env GOVERSION)"
if [[ -z "$expected" || "$actual" != "$expected" ]]; then
  echo "Devcontainer Go is $actual; go.mod requires $expected. Update .devcontainer/Dockerfile and rebuild." >&2
  exit 1
fi

# Bind mounts can retain a different host UID (notably in Podman and CI).
# Trust only this explicitly opened workspace, inside the container's Git config.
workspace="$(pwd -P)"
if ! git config --global --get-all safe.directory | grep -Fxq -- "$workspace"; then
  git config --global --add safe.directory "$workspace"
fi

go mod download
go mod verify
echo "Development environment ready: make verify, make build, make inspector-e2e"
