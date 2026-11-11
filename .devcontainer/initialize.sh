#!/usr/bin/env bash
set -euo pipefail

# GUI-launched editors on macOS may not inherit the shell's Podman PATH.
podman_bin="$(command -v podman || true)"
if [[ -z "$podman_bin" ]]; then
  for candidate in /opt/podman/bin/podman /opt/homebrew/bin/podman /usr/local/bin/podman; do
    if [[ -x "$candidate" ]]; then
      podman_bin="$candidate"
      break
    fi
  done
fi

if [[ -z "$podman_bin" ]]; then
  echo 'Podman was not found. Install Podman and set dev.containers.dockerPath in VS Code User Settings to its absolute path.' >&2
  exit 1
fi

if ! "$podman_bin" info > /dev/null; then
  echo 'Podman is unavailable. On macOS/Windows, run podman machine start, then reopen the devcontainer.' >&2
  exit 1
fi
