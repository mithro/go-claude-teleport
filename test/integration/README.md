# Integration harness

Three containers — `source`, `jump`, `dest` — with sshd, tmux and git.
`dest` is only reachable through `jump`.

    test/integration/build.sh                # fakeclaude image (layer 1)
    go test -tags integration ./test/integration/ -v -timeout 30m

    test/integration/build.sh 2.1.247        # real Claude Code (layer 2)
    CLAUDE_VERSION=2.1.247 go test -tags 'integration realclaude' ./test/integration/ -run TestReal -v -timeout 30m

Everything under `keys/` and `api-log/` is generated per run and git-ignored.

Two layer-1 scenarios (the killed-runner and network-drop resumes) each
create a **1 GiB** random file on `source` and transfer it, to make the
"transfer running" window wide enough to intervene in from outside: budget
~2 GiB of free disk (source's copy plus dest's) and most of the suite's
~4 minutes for those two tests. The other scenarios are seconds each.

`build.sh` with a CLAUDE_VERSION argument also builds the profile-gated
`api` (fakeapi) image; without one it builds only the layer-1 services,
since layer 1 never starts `api`. Pass `-count=1` when re-running a suite
locally: `go test` caches a passing result, and a cached `ok` starts no
container at all.
