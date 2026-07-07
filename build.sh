#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

# Fork variant: per-client Unix-domain sockets (streamlocal) instead of a shared
# TCP port. Binaries are suffixed "-unix-<arch>" so they can be installed next to
# the original TCP build without clobbering it.
platforms=(
  "linux/amd64"
  "darwin/amd64"
  "darwin/arm64"
)

for platform in "${platforms[@]}"; do
  os="${platform%/*}"
  arch="${platform#*/}"
  suffix="${os}-${arch}"

  echo "Building for ${os}/${arch}..."

  echo "  ccimgd-unix-${suffix}"
  (cd daemon && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -o "ccimgd-unix-${suffix}" .)

  echo "  ccimg-unix-${suffix}"
  (cd client && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -o "ccimg-unix-${suffix}" .)
done

echo "Done."
