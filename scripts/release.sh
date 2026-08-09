#!/usr/bin/env bash
# Build a release binary with version + commit baked in via -ldflags.
# Output: bin/perch
#
# Usage:  scripts/release.sh            # uses git describe for version
#         VERSION=0.2.0 scripts/release.sh   # override version
set -euo pipefail

cd "$(dirname "$0")/.."

# git describe --tags --always --dirty produces something like:
#   v0.1.0          (clean tag)
#   v0.1.0-39-gabc1234       (39 commits past tag, short hash)
#   v0.1.0-39-gabc1234-dirty (uncommitted local changes)
# That string is already the version an operator wants to see, so we bake
# it into main.version verbatim and use the short hash (without the -N-g
# prefix) for main.commit. Falls back to "dev" / "unknown" when not in a
# git repo (e.g. release tarball build).
DESC="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
SHORT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

# If DESC is "dev" (no git), commit stays "unknown" and versionString()
# prints just "dev". Otherwise DESC has a short hash embedded — strip the
# leading "v" so the output is "0.1.0-39-gabc1234" not "v0.1.0-...".
VERSION="${VERSION:-${DESC#v}}"
COMMIT="${COMMIT:-${SHORT}}"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

LDFLAGS=(
  "-s" "-w"
  "-extldflags '-static'"
  "-X main.version=${VERSION}"
  "-X main.commit=${COMMIT}"
)

echo "Building bin/perch ${VERSION} (commit ${COMMIT}, ${BUILD_DATE})"
CGO_ENABLED=0 go build \
  -ldflags="${LDFLAGS[*]}" \
  -tags 'osusergo,netgo' \
  -o bin/perch \
  ./cmd/perch

upx --best --lzma bin/perch
./bin/perch --version