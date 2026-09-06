# Phase 2 — zero-install cross-arch bootstrap (claude-teleport)

Goal: teleport between two machines of different architectures even when the
destination has no `claude-teleport` installed, modeled on `go-multi-binary`'s
fat-binary + reconstruct law. Decisions taken with the user: **import** the
public `github.com/mithro/go-multi-binary/{fatblob,archdetect}` packages
(Phase 1, PR #3); the released `claude-teleport` is the **fat** binary.

## How it works

- Release builds `claude-teleport` for every supported arch, then `fatpack`
  staples the shared FATBLOB onto each → `canonical(arch)`. The distributed
  binary for arch A is a native ELF for A that also carries every arch inside.
- At connect time, when the remote helper is absent OR its version/protocol
  does not match the local one, and the local binary is fat, the local side:
  1. `uname -m` on the remote → `archdetect.FromUname` → remote arch.
  2. `fatblob.Reconstruct(self, remoteArch)` → the exact matching
     `claude-teleport` binary (bit-identical to a release build; no network).
  3. Upload it over the existing in-process sshx transport (`cat > tmp` then
     atomic `chmod +x` + `mv`), md5-verify, into a versioned cache path.
  4. Run `<cachePath> remote serve`.
- Because the deployed binary is the SAME version as local, the protocol/
  version handshake passes — this also removes today's "install the same
  version on both hosts" friction.

## Components

1. `internal/bootstrap/bootstrap.go`
   - `Deploy(ctx, run RunFunc, put PutFunc, selfImage []byte, version string) (remotePath string, res Result, err error)`:
     split self (`fatblob.SplitCanonical`) — if not canonical, return
     `ErrNotFatBinary`; detect arch; `Reconstruct`; resolve remote cache dir
     (`sh -c 'echo ${XDG_CACHE_HOME:-$HOME/.cache}'`) → `<cache>/claude-teleport/claude-teleport-<version>-<arch>`;
     `mkdir -p`; upload to `<path>.<pid>.tmp`; `chmod +x`; `md5sum` verify vs
     local; atomic `mv` to `<path>`; return `<path>`. Idempotent: if the path
     already exists with the right md5, skip the upload.
   - Transport is injected (RunFunc/PutFunc) so it is unit-testable with a
     fake and driven by `sshx.Client` in production. NEVER shell out; reuse
     `sshx.Client.Run(ctx, cmd, stdin)`.
2. `internal/remote/client.go` — new `NewClientOrBootstrap(ctx, ssh, opts)`:
   - Try `NewClient(ctx, ssh, "claude-teleport")` (the existing PATH-probe).
     On success AND matching version/protocol → use it (fast path, no upload).
   - On not-found error, or success-but-mismatch, and `opts.Bootstrap` set
     (local is fat): `bootstrap.Deploy(...)` then `NewClient(ctx, ssh, <path>)`.
   - On not-fat / bootstrap failure: return the original clear error
     (unchanged behavior for a dev build) — the message names both that the
     remote lacks it and that this build cannot bootstrap.
3. `internal/cli/endpoints.go` + `remotecfg.go` — call the new entry point;
   thread a `--no-bootstrap` escape hatch and honor it.
4. `internal/cli/doctor.go` — report whether the local binary is fat and which
   arches it carries (`fatblob.SplitCanonical` + index), and, for the remote,
   whether bootstrap would be used.
5. `release.yml` — build arches {386, amd64, arm(GOARM=6), arm64, riscv64}
   (add the three missing), `go run github.com/mithro/go-multi-binary/cmd/fatpack`
   to assemble the fat canonical binaries, ship those + `.deb`s + checksums.
   The `.deb` for arch A ships `canonical(A)`.
6. Docs: usage note on zero-install teleport + the size tradeoff (~55MB).

## Tests (TDD)

- bootstrap: fake transport; not-fat → ErrNotFatBinary; happy path uploads &
  verifies to the versioned path; md5 mismatch → error; idempotent re-deploy
  skips upload; arch from a range of `uname -m` strings.
- client: fake ssh where installed is absent → bootstrap path taken; installed
  present+matching → no upload; installed present+mismatch+fat → bootstrap.
- A real cross-arch e2e is out of scope for the unit gate (covered by the
  existing docker integration harness in a follow-up; the reconstruct law
  itself is already proven in go-multi-binary).

## Dependency / ordering

- go.mod requires `github.com/mithro/go-multi-binary vX.Y` — needs PR #3
  merged and tagged (v0.1) first. Development uses a local `replace`; the
  final commit pins the tag and drops the replace.

## Constraints

- Remote cache under `~/.cache/claude-teleport/`, never `/tmp`.
- Never transfer or render credentials; the uploaded binary is the tool
  itself, reconstructed from the running image, verified by md5.
- Static, CGO-off builds (already the case); the fat binary must stay
  reproducible so `Reconstruct` is bit-identical to the release build.
