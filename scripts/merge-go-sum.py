#!/usr/bin/env python3
"""Resolve only go.sum conflicts; never choose between different checksums."""

import subprocess
import sys
from pathlib import Path


def parse(text):
    entries = {}
    for line in text.splitlines():
        if not line.strip():
            continue
        fields = line.split()
        if len(fields) != 3:
            raise ValueError("invalid go.sum record")
        key, checksum = tuple(fields[:2]), fields[2]
        if key in entries and entries[key] != checksum:
            raise ValueError("different checksums for " + " ".join(key))
        entries[key] = checksum
    return entries


def record_order(key):
    path, version = key
    version, _, suffix = version.partition("/")
    core = version.partition("+")[0]
    release, dash, prerelease = core.partition("-")
    numbers = tuple(int(n) for n in release.removeprefix("v").split("."))
    pre = tuple((0, int(n)) if n.isdigit() else (1, n) for n in prerelease.split(".")) if dash else ()
    return path, numbers, not dash, pre, suffix, version


def merge(base, ours, theirs):
    base, ours, theirs = map(parse, (base, ours, theirs))
    result = {}
    for key in base.keys() | ours.keys() | theirs.keys():
        values = {side[key] for side in (base, ours, theirs) if key in side}
        if len(values) != 1:
            raise ValueError("different checksums for " + " ".join(key))
        b, o, t = (side.get(key) for side in (base, ours, theirs))
        chosen = o if o == t or t == b else t
        if chosen is not None:
            result[key] = chosen
    return "".join(" ".join((*key, result[key])) + "\n" for key in sorted(result, key=record_order))


def git(*args):
    return subprocess.run(["git", *args], check=True, capture_output=True).stdout.decode("utf-8")


def main():
    root = Path(git("rev-parse", "--show-toplevel").strip())
    conflicts = git("-C", str(root), "diff", "--name-only", "--diff-filter=U", "-z").split("\0")
    conflicts = [name for name in conflicts if name]
    if conflicts != ["go.sum"]:
        raise ValueError("manual merge required: " + ", ".join(conflicts or ["no go.sum conflict"]))
    versions = [git("-C", str(root), "show", f":{stage}:go.sum") for stage in (1, 2, 3)]
    content = merge(*versions)  # Validate everything before writing or staging.
    (root / "go.sum").write_text(content, encoding="utf-8", newline="\n")
    git("-C", str(root), "add", "--", "go.sum")
    print("Resolved go.sum by record; dependency versions and checksums were not overridden.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, subprocess.CalledProcessError) as error:
        print(f"go.sum merge stopped: {error}", file=sys.stderr)
        sys.exit(1)
