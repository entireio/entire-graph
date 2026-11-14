"""Read immutable objects without switching the developer's checkout."""
import io
import re
import subprocess
import tarfile
from pathlib import Path, PurePosixPath

SCOPE = "pact/demo/workspace_app"


def git(repo, *args, binary=False):
    p = subprocess.run(["git", "-C", str(repo), *args], capture_output=True, timeout=60)
    if p.returncode:
        raise RuntimeError(p.stderr.decode(errors="replace")[:2000])
    return p.stdout if binary else p.stdout.decode().strip()


def resolve(repo, ref):
    sha = git(repo, "rev-parse", "--verify", "--end-of-options", ref + "^{commit}")
    if not re.fullmatch(r"[0-9a-f]{40,64}", sha):
        raise ValueError("Invalid commit identity")
    return sha


def fixture_files(repo, sha) -> dict[str, str]:
    archive = git(repo, "archive", sha, "--", SCOPE, binary=True)
    files = {}
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        for entry in tar:
            path = PurePosixPath(entry.name)
            if entry.isdir():
                continue
            if (not entry.isfile() or path.is_absolute() or ".." in path.parts
                    or not entry.name.startswith(SCOPE + "/") or path.suffix != ".py"):
                raise ValueError("Fixture must contain only regular Python files under the registered prefix")
            if entry.size > 131072:
                raise ValueError("Fixture file exceeds the pilot size limit")
            files[entry.name] = tar.extractfile(entry).read().decode("utf-8")
    if len(files) > 50 or sum(len(v.encode()) for v in files.values()) > 1048576:
        raise ValueError("Fixture exceeds the pilot size limit")
    if SCOPE + "/app.py" not in files:
        raise ValueError("No registered permission application at this commit")
    return files


def write_fixture(root: Path, files: dict[str, str]):
    for name, content in files.items():
        path = PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or not name.startswith(SCOPE + "/") or path.suffix != ".py":
            raise ValueError("Invalid fixture path")
        target = root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)
