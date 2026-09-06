#!/usr/bin/env python3
"""Build claude-teleport as a fat (multi-architecture) binary.

Builds a native claude-teleport for every supported architecture with the Go
team's reproducible recipe (CGO_ENABLED=0, -trimpath, -buildvcs=false, stripped,
empty build id), then uses go-multi-binary's fatpack to staple the shared
FATBLOB onto each. Every released claude-teleport-linux-<arch> is therefore a
native ELF for <arch> that also carries every architecture inside it, so any
install can reconstruct and install the matching helper onto a different-arch
remote — the zero-install teleport path.

Usage:
  uv run python packaging/fatbuild.py --version vX.Y [--out dist] [--arches amd64,arm64]

The output binaries are dist/claude-teleport-linux-<arch>.
"""
import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
GO_MODULE = "github.com/mithro/go-claude-teleport"
FATPACK = "github.com/mithro/go-multi-binary/cmd/fatpack"

# (arch id, GOARCH, GOARM) — the set go-multi-binary's fatpack understands.
SUPPORTED = [
    ("386", "386", None),
    ("amd64", "amd64", None),
    ("arm", "arm", "6"),
    ("arm64", "arm64", None),
    ("riscv64", "riscv64", None),
]


def build_native(arch, goarch, goarm, native_dir, version):
    native_dir.mkdir(parents=True, exist_ok=True)
    out = native_dir / f"native.{arch}"
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=goarch)
    if goarm:
        env["GOARM"] = goarm
    else:
        env.pop("GOARM", None)
    ldflags = f"-s -w -buildid= -X {GO_MODULE}/internal/version.Version={version}"
    print(f"[build] {arch:8} GOARCH={goarch} GOARM={goarm or '-'}", flush=True)
    subprocess.run(
        ["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags,
         "-o", str(out), "./cmd/claude-teleport"],
        cwd=REPO, env=env, check=True,
    )
    return out


def main():
    ap = argparse.ArgumentParser(description="Reproducible fat multi-arch build")
    ap.add_argument("--version", default=os.environ.get("VERSION", "") or "dev")
    ap.add_argument("--out", default=str(REPO / "dist"))
    ap.add_argument("--arches", default="", help="comma list to limit (local testing only)")
    args = ap.parse_args()

    dist = Path(args.out).resolve()
    native = dist / "native"
    manifest = dist / "MANIFEST.json"
    limit = {a for a in args.arches.split(",") if a}

    print(f"version: {args.version}")
    for arch, goarch, goarm in SUPPORTED:
        if limit and arch not in limit:
            continue
        build_native(arch, goarch, goarm, native, args.version)

    print("[pack] assembling FATBLOB + canonical artifacts", flush=True)
    subprocess.run(["go", "run", FATPACK, "assemble", "--in", str(native),
                    "--out", str(dist), "--manifest", str(manifest)], cwd=REPO, check=True)

    # fatpack writes canonical files named go-teleport-self.<arch>; the bytes
    # are canonical claude-teleport (we packed our own natives). Rename to the
    # released artifact names.
    made = []
    for f in sorted(dist.glob("go-teleport-self.*")):
        arch = f.name.split(".", 1)[1]
        dest = dist / f"claude-teleport-linux-{arch}"
        shutil.move(str(f), str(dest))
        os.chmod(dest, 0o755)
        made.append(dest.name)
        print(f"[pack] {dest.name} ({dest.stat().st_size} bytes)")
    if not made:
        print("no canonical artifacts produced", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
