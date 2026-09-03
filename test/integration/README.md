# Integration harness

Three containers — `source`, `jump`, `dest` — with sshd, tmux and git.
`dest` is only reachable through `jump`.

    test/integration/build.sh                # fakeclaude image (layer 1)
    go test -tags integration ./test/integration/ -v -timeout 30m

    test/integration/build.sh 2.1.247        # real Claude Code (layer 2)
    CLAUDE_VERSION=2.1.247 go test -tags 'integration realclaude' ./test/integration/ -run TestReal -v -timeout 30m

Everything under `keys/` and `api-log/` is generated per run and git-ignored.
