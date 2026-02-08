#!/usr/bin/env python3
"""Build GitHub CLI-compatible binaries and checksums; does not publish them."""

import hashlib
import os
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
TARGETS = [(system, arch) for system in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")]


def build(version):
    if re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version) is None:
        raise ValueError("expected a release tag such as v1.0.0 or v1.0.0-rc.1")
    destination = ROOT / "dist"
    destination.mkdir(exist_ok=True)
    sums = []
    for system, arch in TARGETS:
        suffix = ".exe" if system == "windows" else ""
        filename = f"gh-star-repos_{system}_{arch}{suffix}"
        output = destination / filename
        env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0", GOTOOLCHAIN="local")
        subprocess.run(
            ["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.version={version}", "-o", str(output), "."],
            cwd=ROOT, env=env, check=True,
        )
        sums.append(f"{hashlib.sha256(output.read_bytes()).hexdigest()}  {filename}\n")
        print(filename, flush=True)
    (destination / "checksums.txt").write_text("".join(sums))


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ValueError("usage: python3 scripts/build-release.py TAG")
        build(sys.argv[1])
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(error, file=sys.stderr)
        sys.exit(1)
