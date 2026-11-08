#!/usr/bin/env python3
"""Fetch pinned GitHub release assets through the official API and verify them."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from urllib.parse import unquote, urlsplit


def asset_parts(url):
    parsed = urlsplit(url)
    parts = [unquote(part) for part in parsed.path.split("/")[1:]]
    if (parsed.scheme != "https" or parsed.netloc != "github.com" or parsed.query
            or parsed.fragment or len(parts) != 6 or parts[2:4] != ["releases", "download"]
            or any(not re.fullmatch(r"[A-Za-z0-9_.+-]+", part) or part in {".", ".."} for part in parts)):
        raise ValueError("expected a GitHub release asset URL")
    return parts[0], parts[1], parts[4], parts[5]


def digest(path):
    hashed = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            hashed.update(block)
    return hashed.hexdigest()


def gh(arguments, output=None):
    result = subprocess.run(["gh", "api", *arguments], check=True,
                            stdout=output or subprocess.PIPE, stderr=subprocess.PIPE)
    return result.stdout


def fetch_asset(url, expected, destination):
    owner, repo, tag, name = asset_parts(url)
    if not re.fullmatch(r"[0-9a-f]{64}", expected):
        raise ValueError("expected a lowercase SHA-256")
    destination = Path(destination)
    if destination.is_file() and digest(destination) == expected:
        return
    release = json.loads(gh([f"repos/{owner}/{repo}/releases/tags/{tag}"]))
    assets = [asset for asset in release.get("assets", []) if asset.get("name") == name]
    if len(assets) != 1 or not isinstance(assets[0].get("id"), int):
        raise ValueError("release asset was not found uniquely")
    destination.parent.mkdir(parents=True, exist_ok=True)
    temp = None
    try:
        with tempfile.NamedTemporaryFile(dir=destination.parent, prefix=".release-asset-", delete=False) as out:
            temp = Path(out.name)
            gh(["-H", "Accept: application/octet-stream", f"repos/{owner}/{repo}/releases/assets/{assets[0]['id']}"], out)
        if digest(temp) != expected:
            raise ValueError(f"SHA-256 mismatch for {name}")
        os.replace(temp, destination)
    finally:
        if temp is not None:
            temp.unlink(missing_ok=True)


def cache_root():
    if sys.platform == "darwin":
        return Path.home() / "Library" / "Caches"
    if os.name == "nt":
        local = os.environ.get("LOCALAPPDATA")
        if not local:
            raise ValueError("LOCALAPPDATA is required on Windows")
        return Path(local)
    return Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache")


def prepare_natives(targets, root=None):
    repo = Path(__file__).resolve().parent.parent
    module = subprocess.run(["go", "mod", "download", "-json", "github.com/egoist/mygo"],
                            cwd=repo / "desktop-native", check=True, capture_output=True)
    plugin = Path(json.loads(module.stdout)["Dir"]) / "plugins" / "terminal" / "mygo-plugin.json"
    libraries = json.loads(plugin.read_text(encoding="utf-8"))["libraries"]
    library = next(item for item in libraries if item["name"] == "libghostty-vt")
    for target in targets:
        item = library["files"][target]
        name, sha = item["name"], item["sha256"]
        if (not re.fullmatch(r"[A-Za-z0-9_.+-]+", name) or name in {".", ".."}
                or not re.fullmatch(r"[0-9a-f]{64}", sha)):
            raise ValueError("invalid native library name")
        fetch_asset(item["url"], sha, (root or cache_root()) / "mygo" / "natives" / sha / name)
        print(f"Verified {target} native library")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    asset = sub.add_parser("asset")
    asset.add_argument("url")
    asset.add_argument("sha256")
    asset.add_argument("destination", type=Path)
    natives = sub.add_parser("natives")
    natives.add_argument("targets", nargs="+")
    args = parser.parse_args()
    try:
        if args.command == "asset":
            fetch_asset(args.url, args.sha256, args.destination)
        else:
            prepare_natives(args.targets)
    except (OSError, ValueError, KeyError, StopIteration, subprocess.CalledProcessError) as error:
        print(f"Cannot prepare release dependency: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
