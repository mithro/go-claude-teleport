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
# tmux (verified on 3.5a and next-3.8 alike) vis-encodes control characters
# -- including the literal tab byte internal/tmuxx uses as its -F list-panes
# field separator -- into "_" when the process locale isn't UTF-8-aware;
# this container's default is POSIX/C. glibc's built-in C.UTF-8 needs no
# locale-gen. AcceptEnv (sshd_config) does not forward LC_ALL, so every ssh
# session (this is where claude-teleport's own dial lands, on jump and
# dest) needs it via PermitUserEnvironment + ~/.ssh/environment instead.
for u in alice bob; do
  echo 'LC_ALL=C.UTF-8' > /home/$u/.ssh/environment
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
