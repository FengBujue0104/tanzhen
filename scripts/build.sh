#!/usr/bin/env bash
# Cross-compile hub + agent binaries into ./dist and ./releases
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p dist releases

VERSION="${VERSION:-0.1.0}"
LDFLAGS="-s -w -X github.com/FengBujue0104/tanzhen/internal/agent.Version=${VERSION}"

build() {
  local os=$1 arch=$2 out=$3
  echo "building $out"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -ldflags="$LDFLAGS" -o "$out" "$4"
}

# Hub (linux only for server)
build linux amd64 dist/tanzhen-hub-linux-amd64 ./cmd/hub
build linux arm64 dist/tanzhen-hub-linux-arm64 ./cmd/hub

# Agents for install downloads
build linux amd64 releases/tanzhen-agent-linux-amd64 ./cmd/agent
build linux arm64 releases/tanzhen-agent-linux-arm64 ./cmd/agent
build windows amd64 releases/tanzhen-agent-windows-amd64.exe ./cmd/agent

# Local native copies
cp releases/tanzhen-agent-linux-amd64 dist/tanzhen-agent || true
cp dist/tanzhen-hub-linux-amd64 dist/tanzhen-hub || true

echo "Done."
ls -lh dist releases
