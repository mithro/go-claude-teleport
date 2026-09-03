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
# Real Claude (layer 2) lives in alice's ~/.local/bin; put it on PATH for sshd sessions.
if [ -x /home/alice/.local/bin/claude ]; then
  echo 'PATH=/home/alice/.local/bin:/usr/local/bin:/usr/bin:/bin' >> /home/alice/.ssh/environment
fi
exec /usr/sbin/sshd -D -e
