#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/../.."

GIT_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
BUILD_DATE="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo 'dev')}"

docker build -f cloud/Dockerfile \
  --build-arg VERSION="${VERSION}" \
  --build-arg GIT_COMMIT="${GIT_COMMIT}" \
  --build-arg BUILD_DATE="${BUILD_DATE}" \
  -t ghcr.io/thekiharani/monitor-cloud . --load
