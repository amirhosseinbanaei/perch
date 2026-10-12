#!/usr/bin/env bash
# Build the release archives into dist/: one tar.gz per platform (the binary, LICENSE and
# README.md) plus checksums.txt. Usage: scripts/release.sh VERSION   (e.g. 0.1.0 or v0.1.0)
set -euo pipefail

VERSION="${1:?usage: scripts/release.sh VERSION}"
VERSION="${VERSION#v}"
cd "$(dirname "$0")/.."

PKG=github.com/amirhosseinbanaei/perch/internal/version
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT} -X ${PKG}.Date=${DATE}"
TARGETS="linux/amd64 linux/arm64 linux/armv7 darwin/amd64 darwin/arm64"

rm -rf dist
mkdir -p dist
for target in $TARGETS; do
  os="${target%/*}"
  arch="${target#*/}"
  goarch="$arch"
  goarm=""
  if [ "$arch" = armv7 ]; then
    goarch=arm
    goarm=7
  fi
  name="perch_${VERSION}_${os}_${arch}"
  stage="dist/.stage/${name}"
  mkdir -p "$stage"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$goarch" GOARM="$goarm" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${stage}/perch" ./cmd/perch
  cp LICENSE README.md "$stage/"
  tar -C "$stage" -czf "dist/${name}.tar.gz" perch LICENSE README.md
done
rm -rf dist/.stage

cd dist
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum ./*.tar.gz | sed 's| \./| |' > checksums.txt
else
  shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt
fi
echo "perch ${VERSION} (${COMMIT}) → dist/"
for f in *.tar.gz checksums.txt; do
  printf '  %-48s %8s\n' "$f" "$(du -h "$f" | cut -f1)"
done
