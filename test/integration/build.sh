#!/bin/sh
# test/integration/build.sh — build the binaries for the container arch,
# generate the per-run key, and (re)build the images.
# Usage: test/integration/build.sh [CLAUDE_VERSION]
set -eu
cd "$(dirname "$0")/../.."
arch=$(docker info --format '{{.Architecture}}' | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
mkdir -p dist test/integration/keys test/integration/api-log
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -ldflags "-X github.com/mithro/go-claude-teleport/internal/version.Version=it-$(git rev-parse --short HEAD)" -o dist/claude-teleport ./cmd/claude-teleport
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o dist/fakeclaude ./test/fakeclaude
rm -f test/integration/keys/id_ed25519 test/integration/keys/id_ed25519.pub
ssh-keygen -q -t ed25519 -N '' -C claude-teleport-it -f test/integration/keys/id_ed25519
# 600, not 644 (C13): this throwaway per-run key is bind-mounted read-only
# into every container, and entrypoint.sh copies it into each user's ~/.ssh
# (chmod 600, chown) before anything uses it — so nothing needs to read it
# as another uid, and a private key on the host has no business being
# world-readable even for a few minutes.
chmod 600 test/integration/keys/id_ed25519
export CLAUDE_VERSION="${1:-}"
if [ -n "$CLAUDE_VERSION" ]; then
  # Layer 2 (real Claude) also runs the fakeapi service, and `api` is
  # gated behind the "realclaude" profile: a plain `compose build` skips
  # profile-gated services entirely, so without this the fakeapi image is
  # never rebuilt here — `compose up` would build it only if no image of
  # that name exists yet, silently reusing a stale one after any change to
  # internal/fakeapi or test/fakeapi-server.
  docker compose -f test/integration/docker-compose.yml --profile realclaude build
else
  docker compose -f test/integration/docker-compose.yml build
fi
