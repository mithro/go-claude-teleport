#!/usr/bin/env python3
"""Release version helpers for the GitHub Actions release pipeline.

Versions come from git tags of the form vXX.ZZZ (the repo's tag ruleset).

    version.py tag        -> the tag for HEAD: the exact vX.Y tag if HEAD is
                             tagged, otherwise the next patch tag (latest
                             reachable vX.Y with Y+1; v0.1 when no tag exists)
    version.py is-tagged  -> exit 0 if HEAD carries an exact vX.Y tag, else 1
    version.py deb TAG    -> Debian version for TAG (v0.2 -> 0.2)

stdlib only; runs on the CI runner's python3.
"""
import re
import subprocess
import sys

TAG_RE = re.compile(r"^v(\d+)\.(\d+)$")


def git(*args):
    return subprocess.run(["git", *args], capture_output=True, text=True,
                          check=True).stdout.strip()


def parse_tag(tag):
    """Return (major, patch) for a vX.Y tag, or None."""
    m = TAG_RE.match(tag)
    return (int(m.group(1)), int(m.group(2))) if m else None


def next_patch(latest):
    """Next patch tag after `latest` (a vX.Y tag or None)."""
    if latest is None:
        return "v0.1"
    major, patch = parse_tag(latest)
    return f"v{major}.{patch + 1}"


def deb_version(tag):
    """Debian version string for a vX.Y tag."""
    if parse_tag(tag) is None:
        raise ValueError(f"not a vX.Y tag: {tag!r}")
    return tag[1:]


def exact_tag():
    """The vX.Y tag pointing at HEAD, or None."""
    try:
        out = git("tag", "--points-at", "HEAD")
    except subprocess.CalledProcessError:
        return None
    tags = sorted((t for t in out.splitlines() if parse_tag(t)), key=parse_tag)
    return tags[-1] if tags else None


def latest_reachable_tag():
    """The highest vX.Y tag reachable from HEAD, or None."""
    out = git("tag", "--merged", "HEAD")
    tags = sorted((t for t in out.splitlines() if parse_tag(t)), key=parse_tag)
    return tags[-1] if tags else None


def head_tag():
    return exact_tag() or next_patch(latest_reachable_tag())


def main(argv):
    if len(argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    cmd = argv[1]
    if cmd == "tag":
        print(head_tag())
    elif cmd == "is-tagged":
        return 0 if exact_tag() else 1
    elif cmd == "deb":
        if len(argv) != 3:
            print("usage: version.py deb TAG", file=sys.stderr)
            return 2
        print(deb_version(argv[2]))
    else:
        print(f"unknown command {cmd!r}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
