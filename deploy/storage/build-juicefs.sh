#!/usr/bin/env bash
set -euo pipefail
# Official v1.4.1 binary. Build requires a native ARM64 Docker engine.
version=1.4.1
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL "https://github.com/juicedata/juicefs/releases/download/v${version}/juicefs-${version}-linux-arm64.tar.gz" -o "$work/juicefs-${version}-linux-arm64.tar.gz"
curl -fsSL "https://github.com/juicedata/juicefs/releases/download/v${version}/checksums.txt" -o "$work/checksums.txt"
(cd "$work"; rg "juicefs-${version}-linux-arm64.tar.gz" checksums.txt | shasum -a 256 -c -)
tar -xzf "$work/juicefs-${version}-linux-arm64.tar.gz" -C "$work"
cp "$(dirname "$0")/Dockerfile.juicefs" "$work/Dockerfile"
docker build --platform linux/arm64 -t "artifact-juicefs:${version}-arm64" "$work"
