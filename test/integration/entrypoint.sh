#!/bin/sh
# test/integration/entrypoint.sh — install the per-run key, start sshd.
set -eu
# alice is the main user on every host; bob exists on every host too so
# the "different $HOME" scenario can teleport alice@source -> bob@dest.
for u in alice bob; do
  if [ -f /run/keys/id_ed25519.pub ]; then
    cp /run/keys/id_ed25519.pub /home/$u/.ssh/authorized_keys
    cp /run/keys/id_ed25519 /home/$u/.ssh/id_ed25519
    chmod 600 /home/$u/.ssh/authorized_keys /home/$u/.ssh/id_ed25519
    chown $u:$u /home/$u/.ssh/authorized_keys /home/$u/.ssh/id_ed25519
  fi
done
ssh-keygen -A >/dev/null
# ~/.ssh/environment (with PermitUserEnvironment below) is the only way to
# put a variable into the tool's own non-interactive ssh sessions: sshd's
# AcceptEnv does not forward LC_ALL, and no login shell is ever run there.
# Layer 2 needs PATH here (below). LC_ALL is deliberately NOT set: real
# tmux (3.5a, 3.6b) mangles the tab-separated `-F` output it sends a
# control client whose LC_ALL/LC_CTYPE/LANG name no UTF-8 locale, and
# internal/tmuxx now sets LC_ALL=C.UTF-8 on the `tmux -C` client it spawns
# itself (tmuxx.utf8Env) — the tool must work on a plain C-locale host, so
# the harness must not paper over it.
for u in alice bob; do
  : > /home/$u/.ssh/environment
  chown $u:$u /home/$u/.ssh/environment
  chmod 600 /home/$u/.ssh/environment
done
sed -i 's/^#\?PermitUserEnvironment.*/PermitUserEnvironment yes/' /etc/ssh/sshd_config || true
grep -q PermitUserEnvironment /etc/ssh/sshd_config || echo 'PermitUserEnvironment yes' >> /etc/ssh/sshd_config
# Real Claude (layer 2) lives in alice's ~/.local/bin; put it on PATH for
# sshd sessions.
#
# Task 26 adaptation (disclosed): a real teleport's every claude process on
# dest is started by the tool's OWN runner, itself spawned over ssh (never
# a `docker compose exec`, which is the only thing that sees the
# container's own `environment:` block) — so a fresh, non-interactive ssh
# session's minimal environment never carried ANTHROPIC_BASE_URL/
# ANTHROPIC_API_KEY/DISABLE_*/CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC:
# real claude on dest tried api.anthropic.com for real ("Unable to connect
# to Anthropic services... ETIMEOUT" in the job log) the first time this
# was run without this block. Re-exporting entrypoint.sh's OWN inherited
# values (set once, by docker-compose.yml's `environment:`, the single
# source of truth for the fakeapi URL and dummy key) keeps controller
# requirement A intact: nothing here is a hardcoded credential, and a
# missing/empty value is simply not written.
#
# Written for BOTH users, not just alice: the layer-2 image puts
# /home/alice/.local/bin on every account's PATH (an image-level ENV is
# container-wide — Dockerfile), so bob's sessions can reach that same real
# claude binary. Giving bob the PATH but not the fakeapi endpoint is the
# one combination that would let a bob@dest scenario reach for the real
# api.anthropic.com, so the two always travel together.
if [ -x /home/alice/.local/bin/claude ]; then
  for u in alice bob; do
    {
      echo 'PATH=/home/alice/.local/bin:/usr/local/bin:/usr/bin:/bin'
      [ -n "${ANTHROPIC_BASE_URL:-}" ] && echo "ANTHROPIC_BASE_URL=$ANTHROPIC_BASE_URL"
      [ -n "${ANTHROPIC_API_KEY:-}" ] && echo "ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY"
      [ -n "${DISABLE_AUTOUPDATER:-}" ] && echo "DISABLE_AUTOUPDATER=$DISABLE_AUTOUPDATER"
      [ -n "${DISABLE_TELEMETRY:-}" ] && echo "DISABLE_TELEMETRY=$DISABLE_TELEMETRY"
      [ -n "${DISABLE_ERROR_REPORTING:-}" ] && echo "DISABLE_ERROR_REPORTING=$DISABLE_ERROR_REPORTING"
      [ -n "${CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC:-}" ] && echo "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"
    } >> /home/$u/.ssh/environment
  done
fi
exec /usr/sbin/sshd -D -e
