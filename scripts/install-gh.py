#!/usr/bin/env python3
"""Install GitHub CLI for a CI target and add it to the workflow PATH."""

import hashlib
import io
import os
from pathlib import Path, PurePosixPath
import sys
import tarfile
import urllib.request
import zipfile


VERSION = "2.85.0"
ASSETS = {
    ("darwin", "amd64"): ("macOS_amd64.zip", "e58bdb7da9cf4c5254f6b847198d18af7c07c486c2a3af94ad957a48c1051c1c"),
    ("darwin", "arm64"): ("macOS_arm64.zip", "3d6999b85fa1e63aadec49b65986280578e8094c394a3776c24c789f0cc73cef"),
    ("linux", "amd64"): ("linux_amd64.tar.gz", "5dcc4e282b9c22c0d31c217805976448c966cfc56c20157c78385a8a62c93cf0"),
    ("linux", "arm64"): ("linux_arm64.tar.gz", "c554878f62506c88464ef94474322d899dfb30468bb234f23e2355e8e1613cb2"),
    ("windows", "amd64"): ("windows_amd64.zip", "def347c8bbb22c79109dc546d1c083753512e54fd48deca41aad73b9beef9277"),
    ("windows", "arm64"): ("windows_arm64.zip", "a7b2717867c443870b131ac7240d47ad7a029224071562c644e621d37f20bf73"),
}


def executable(data, archive_name, filename):
    """Read just the executable, without extracting archive paths to disk."""
    if archive_name.endswith(".zip"):
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            names = [name for name in archive.namelist() if PurePosixPath(name).parts[-2:] == ("bin", filename)]
            if len(names) != 1:
                raise ValueError("expected one GitHub CLI executable in archive")
            return archive.read(names[0])
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        members = [member for member in archive.getmembers() if member.isfile() and PurePosixPath(member.name).parts[-2:] == ("bin", filename)]
        if len(members) != 1:
            raise ValueError("expected one GitHub CLI executable in archive")
        with archive.extractfile(members[0]) as source:
            return source.read()


def install(system, arch):
    if (system, arch) not in ASSETS:
        raise ValueError(f"unsupported target: {system}/{arch}")
    suffix, expected = ASSETS[system, arch]
    archive_name = f"gh_{VERSION}_{suffix}"
    url = f"https://github.com/cli/cli/releases/download/v{VERSION}/{archive_name}"
    request = urllib.request.Request(url, headers={"User-Agent": "gh-star-repos-ci"})
    with urllib.request.urlopen(request, timeout=120) as response:
        data = response.read()
    if hashlib.sha256(data).hexdigest() != expected:
        raise ValueError(f"checksum mismatch for {archive_name}")
    directory = Path(os.environ["RUNNER_TEMP"]) / f"gh-{VERSION}" / "bin"
    directory.mkdir(parents=True, exist_ok=True)
    filename = "gh.exe" if system == "windows" else "gh"
    binary = directory / filename
    binary.write_bytes(executable(data, archive_name, filename))
    binary.chmod(0o755)
    with open(os.environ["GITHUB_PATH"], "a", encoding="utf-8") as output:
        output.write(str(directory.resolve()) + "\n")
    print(f"Installed GitHub CLI {VERSION} for {system}/{arch}")


if __name__ == "__main__":
    try:
        if len(sys.argv) != 3:
            raise ValueError("usage: python3 scripts/install-gh.py GOOS GOARCH")
        install(*sys.argv[1:])
    except (ValueError, OSError, KeyError, tarfile.TarError, zipfile.BadZipFile) as error:
        print(error, file=sys.stderr)
        sys.exit(1)
