#!/usr/bin/env bash
set -euo pipefail

service="${1:?service is required}"
base="${2:-}"
head="${3:-HEAD}"
case "$service" in
  host|cloud) ;;
  *) echo "Unknown service: $service" >&2; exit 2 ;;
esac
build=false test=false compose=false

if [ -z "$base" ] || ! git cat-file -e "$base^{commit}" 2>/dev/null; then
  build=true test=true compose=true
else
  changed="$(mktemp)"
  trap 'rm -f "$changed"' EXIT
  git diff --name-only --no-renames -z "$base" "$head" > "$changed"
  while IFS= read -r -d '' path; do
    owner=""
    case "$path" in
      .github/workflows/release.yml|Dockerfile)
        owner=host ;;
      .github/workflows/cloud.yml|cloud/Dockerfile)
        owner=cloud ;;
      .github/scripts/*|.github/workflows/*|go.mod|go.sum|go.work|go.work.sum|.dockerignore)
        owner=shared ;;
      cmd/host/*|cmd/webbuild/*|internal/host/*)
        owner=host ;;
      cmd/cloud/*|cmd/cloud-*/*|internal/cloud/*)
        owner=cloud ;;
      internal/*)
        owner=shared ;;
      cloud/dashboard/*|cloud/db/migrations/*|cloud/db/*.go|cloud/scripts/build-images.sh)
        owner=cloud ;;
      cloud/compose*.yml|cloud/.env.example)
        if [ "$service" = cloud ]; then
          build=true test=true compose=true
        fi ;;
      cloud/db/provision/*)
        if [ "$service" = cloud ]; then test=true; fi ;;
      deploy/*|compose.local.yml)
        if [ "$service" = host ]; then compose=true; fi ;;
    esac
    if [ "$owner" = "$service" ] || [ "$owner" = shared ]; then
      test=true
      case "$path" in
        *_test.go) ;;
        *) build=true ;;
      esac
      case "$path" in
        .github/*) compose=true ;;
      esac
    fi
  done < "$changed"
fi

outputs() {
  echo "build=$build"
  echo "test=$test"
  echo "compose=$compose"
}
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  outputs >> "$GITHUB_OUTPUT"
else
  outputs
fi
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  echo "$service: build=$build; test=$test; compose=$compose" >> "$GITHUB_STEP_SUMMARY"
fi
