#!/usr/bin/env bash
# Cross-compile hub + agent binaries into ./dist and ./releases.
# ./releases is what the hub serves under /releases/ for the one-command install.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p dist releases

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo 0.1.3)}"
PKG="github.com/FengBujue0104/tanzhen/internal/agent"
LDFLAGS="-s -w -X ${PKG}.Version=${VERSION}"

build() {
  local os=$1 arch=$2 out=$3 pkg=$4
  echo "building $out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="$LDFLAGS" -o "$out" "$pkg"
}

# Hub (server-side, linux only). The hub binaries are what /install-hub.sh
# fetches for the one-command deployment, so they are release artifacts, not
# just local build output.
build linux amd64 releases/tanzhen-hub-linux-amd64 ./cmd/hub
build linux arm64 releases/tanzhen-hub-linux-arm64 ./cmd/hub

# Agents, one per platform the installer can ask for
build linux amd64   releases/tanzhen-agent-linux-amd64          ./cmd/agent
build linux arm64   releases/tanzhen-agent-linux-arm64          ./cmd/agent
build linux arm     releases/tanzhen-agent-linux-arm            ./cmd/agent
build linux 386     releases/tanzhen-agent-linux-386            ./cmd/agent
build linux riscv64 releases/tanzhen-agent-linux-riscv64        ./cmd/agent
build linux loong64 releases/tanzhen-agent-linux-loong64        ./cmd/agent
build darwin amd64  releases/tanzhen-agent-darwin-amd64         ./cmd/agent
build darwin arm64  releases/tanzhen-agent-darwin-arm64         ./cmd/agent
build windows amd64 releases/tanzhen-agent-windows-amd64.exe    ./cmd/agent
build windows arm64 releases/tanzhen-agent-windows-arm64.exe    ./cmd/agent

# Native copies for a local `make run-hub`
cp releases/tanzhen-hub-linux-amd64 dist/tanzhen-hub
cp releases/tanzhen-agent-linux-amd64 dist/tanzhen-agent

echo "Done."
ls -lh dist releases
