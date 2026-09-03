FROM debian:trixie-slim

ARG CLAUDE_VERSION=""
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server tmux git ca-certificates curl procps \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd \
 && useradd --create-home --shell /bin/bash alice \
 && useradd --create-home --shell /bin/bash bob \
 && for u in alice bob; do mkdir -p /home/$u/.ssh && chmod 700 /home/$u/.ssh && chown -R $u:$u /home/$u; done \
 # useradd leaves the shadow password field "!" (administratively locked);
 # recent OpenSSH refuses pubkey auth for locked accounts even with
 # UsePAM no. "*" means "no password set" without the locked flag, which
 # is what key-only auth needs.
 && usermod -p '*' alice && usermod -p '*' bob

COPY sshd_config /etc/ssh/sshd_config
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 755 /usr/local/bin/entrypoint.sh

# Layer 2 only: the real Claude Code at a pinned version or "latest", via
# the native installer, as alice. Layer 1 images leave CLAUDE_VERSION empty
# and get fakeclaude bind-mounted as /usr/local/bin/claude instead.
USER alice
RUN if [ -n "$CLAUDE_VERSION" ]; then \
      curl -fsSL https://claude.ai/install.sh | bash -s "$CLAUDE_VERSION" \
      && ln -s /home/alice/.local/bin/claude /home/alice/claude-real; \
    fi
USER root

# Task 26 adaptation (disclosed): the native installer puts real claude at
# ~/.local/bin/claude, only on alice's PATH via a shell-config line it
# suggests adding, which no `docker compose exec` session sources (that's
# not a login/interactive shell). entrypoint.sh already carries an
# equivalent PATH into the tool's own ssh sessions via
# ~/.ssh/environment, but that mechanism doesn't reach a bare `docker
# compose exec ... claude ...` the way this test suite calls claude on
# source/dest directly. An image-level ENV, unlike ~/.ssh/environment,
# reaches every process the container starts, `exec` included (verified:
# without this, "claude --version" via exec was "command not found" the
# moment the layer-1 fakeclaude bind-mount — which happened to also land
# at /usr/local/bin/claude, already on PATH — was correctly dropped by
# docker-compose.realclaude.yml).
ENV PATH="/home/alice/.local/bin:${PATH}"

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
