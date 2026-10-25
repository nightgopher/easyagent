#!/usr/bin/env python3
"""Mirror the update-related assets of a GitHub Release to a Gitee release.

Mainland-China users often cannot reach GitHub reliably, so the Android updater and scripts/install.sh fall
back to a Gitee mirror. This script publishes exactly the assets those fallbacks read. It is conservative:
local files are checked against checksums.txt first, an existing mirror asset is never replaced, and every
asset is downloaded back anonymously and re-hashed before old releases are pruned. The token is read from the
environment and never printed.
"""
import argparse
import hashlib
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from release import TAG_RE  # same version grammar as the release tooling

PLATFORMS = ("linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64")
# What the Android fallback (release.json + APK), install.sh (checksums.txt + core + bridge) and manual macOS installs read.
ASSETS = (
    "release.json", "checksums.txt", "easyagent-android.apk", "easyagent-native-darwin-arm64.dmg",
    *[f"easyagent-{p}" for p in PLATFORMS], *[f"easyagent-bridge-{p}" for p in PLATFORMS],
)
MAX_ASSET = 100 * 1024 * 1024  # Gitee: a single attachment cannot exceed 100MB
DEFAULT_REPO = "weijian-huang/easyagent"
RETRIES = 4


class MirrorError(Exception):
    pass


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_checksums(assets_dir):
    sums = {}
    for line in (Path(assets_dir) / "checksums.txt").read_text().splitlines():
        parts = line.split()
        if len(parts) == 2:
            sums[parts[1].lstrip("*")] = parts[0]
    return sums


def verify_local(assets_dir, tag):
    """Every mirrored file must exist, fit Gitee's limit and match checksums.txt; release.json must be this tag's."""
    root = Path(assets_dir)
    if not (root / "checksums.txt").is_file():
        raise MirrorError("checksums.txt is missing from the assets directory")
    sums = read_checksums(root)
    for name in ASSETS:
        path = root / name
        if not path.is_file():
            raise MirrorError(f"missing asset: {name}")
        if path.stat().st_size == 0 and name != "checksums.txt":
            raise MirrorError(f"empty asset: {name}")
        if path.stat().st_size > MAX_ASSET:
            raise MirrorError(f"{name} exceeds Gitee's 100MB attachment limit")
        if name == "checksums.txt":
            continue
        if name not in sums:
            raise MirrorError(f"no checksum listed for {name}")
        if sha256_file(path) != sums[name]:
            raise MirrorError(f"SHA-256 mismatch against checksums.txt: {name}")
    manifest = json.loads((root / "release.json").read_text())
    if manifest.get("version") != tag or manifest.get("core_only") is True:
        raise MirrorError("release.json does not describe a complete release of this tag")
    return sums


def multipart(fields, file_field, filename, data):
    boundary = uuid.uuid4().hex
    chunks = []
    for key, value in fields.items():
        chunks.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode())
    chunks.append((f'--{boundary}\r\nContent-Disposition: form-data; name="{file_field}"; filename="{filename}"\r\n'
                   "Content-Type: application/octet-stream\r\n\r\n").encode() + data + b"\r\n")
    chunks.append(f"--{boundary}--\r\n".encode())
    return b"".join(chunks), f"multipart/form-data; boundary={boundary}"


class Gitee:
    def __init__(self, repo, token, api, web, sleep=time.sleep):
        self.repo, self.token, self.api, self.web, self.sleep = repo, token, api.rstrip("/"), web.rstrip("/"), sleep

    def request(self, method, path, query=None, form=None, upload=None, timeout=120, retries=RETRIES):
        """JSON request. The token travels in the query or body and is never part of an error message.

        Calls that create things pass retries=1: replaying a POST whose response was lost could create a duplicate,
        so their callers look before trying again."""
        query = dict(query or {})
        body, headers = None, {"User-Agent": "easyagent-gitee-mirror"}
        if method in ("GET", "DELETE"):
            query["access_token"] = self.token
        elif upload:
            fields = dict(form or {}, access_token=self.token)
            body, headers["Content-Type"] = multipart(fields, "file", upload[0], upload[1])
        else:
            body = urllib.parse.urlencode(dict(form or {}, access_token=self.token)).encode()
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        url = f"{self.api}{path}" + (f"?{urllib.parse.urlencode(query)}" if query else "")
        for attempt in range(retries):
            try:
                request = urllib.request.Request(url, data=body, headers=headers, method=method)
                with urllib.request.urlopen(request, timeout=timeout) as response:
                    raw = response.read()
                return json.loads(raw) if raw.strip() else {}
            except urllib.error.HTTPError as error:
                error.close()
                if error.code == 404:
                    raise MirrorError(f"{method} {path}: HTTP 404") from None
                if error.code not in (429, 500, 502, 503, 504) or attempt == retries - 1:
                    raise MirrorError(f"{method} {path}: HTTP {error.code}") from None
            except (urllib.error.URLError, TimeoutError, ConnectionError, json.JSONDecodeError):
                if attempt == retries - 1:
                    raise MirrorError(f"{method} {path}: request failed") from None
            self.sleep(2 ** attempt)
        raise MirrorError(f"{method} {path}: request failed")

    def pages(self, path):
        page, items = 1, []
        while True:
            batch = self.request("GET", path, {"page": page, "per_page": 100})
            items += batch
            if len(batch) < 100:
                return items
            page += 1

    def default_branch(self):
        return self.request("GET", f"/repos/{self.repo}").get("default_branch") or "master"

    def release_by_tag(self, tag):
        """Gitee answers 200 with `null` for a tag that has no release; a 404 is treated the same way."""
        try:
            found = self.request("GET", f"/repos/{self.repo}/releases/tags/{tag}")
        except MirrorError as error:
            if str(error).endswith("HTTP 404"):
                return None
            raise
        return found if isinstance(found, dict) and "id" in found else None

    def create_release(self, tag, name, body):
        return self.request("POST", f"/repos/{self.repo}/releases", form={
            "tag_name": tag, "name": name, "body": body, "prerelease": "false",
            "target_commitish": self.default_branch()}, retries=1)

    def attachments(self, release_id):
        return self.pages(f"/repos/{self.repo}/releases/{release_id}/attach_files")

    def upload(self, release_id, path):
        name = Path(path).name
        for attempt in range(RETRIES):
            # A retried upload may have succeeded server-side, so look before posting again.
            if any(item.get("name") == name for item in self.attachments(release_id)):
                return
            try:
                self.request("POST", f"/repos/{self.repo}/releases/{release_id}/attach_files",
                             upload=(name, Path(path).read_bytes()), timeout=600, retries=1)
                return
            except MirrorError:
                if attempt == RETRIES - 1:
                    raise
                self.sleep(2 ** attempt)

    def releases(self):
        return self.pages(f"/repos/{self.repo}/releases")

    def delete_release(self, release_id):
        self.request("DELETE", f"/repos/{self.repo}/releases/{release_id}")

    def download_sha256(self, tag, name):
        """Fetch the public download link without credentials, as an end user's fallback would."""
        url = f"{self.web}/{self.repo}/releases/download/{tag}/{urllib.parse.quote(name)}"
        for attempt in range(RETRIES):
            try:
                digest = hashlib.sha256()
                with urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "easyagent-gitee-mirror"}), timeout=300) as response:
                    for chunk in iter(lambda: response.read(1 << 20), b""):
                        digest.update(chunk)
                return digest.hexdigest()
            except (urllib.error.URLError, TimeoutError, ConnectionError):
                if attempt == RETRIES - 1:
                    raise MirrorError(f"public download failed: {name}") from None
                self.sleep(2 ** attempt)


def sync(gitee, assets_dir, tag, notes, keep, verify_download=True, log=lambda *a, **k: print(*a, **k, flush=True)):
    if not TAG_RE.fullmatch(tag) or "-" in tag:
        raise MirrorError("only stable release tags are mirrored")
    sums = verify_local(assets_dir, tag)
    release = gitee.release_by_tag(tag)
    if release is None:
        try:
            release = gitee.create_release(tag, f"EasyAgent {tag}", notes)
        except MirrorError:
            release = gitee.release_by_tag(tag)  # the response may have been lost after the release was created
            if release is None:
                raise
        log(f"created Gitee release {tag}")
    existing = {item.get("name"): item for item in gitee.attachments(release["id"])}
    # Refuse before uploading anything if the mirror already holds a different file under one of our names.
    for name, item in existing.items():
        if name in ASSETS and item.get("size") is not None and int(item["size"]) != (Path(assets_dir) / name).stat().st_size:
            raise MirrorError(f"{name} already exists on the mirror with a different size; assets are never replaced")
    pending = [name for name in ASSETS if name not in existing]
    for index, name in enumerate(pending, start=1):
        size = (Path(assets_dir) / name).stat().st_size
        log(f"uploading {name} ({size} bytes, {index}/{len(pending)})")
        gitee.upload(release["id"], Path(assets_dir) / name)
        log(f"uploaded {name} ({size} bytes)")
    if verify_download:
        for name in ASSETS:
            expected = sha256_file(Path(assets_dir) / name)
            if gitee.download_sha256(tag, name) != expected:
                raise MirrorError(f"the mirror serves different bytes than GitHub for {name}")
        log(f"verified {len(ASSETS)} assets by public download")
    if keep > 0:
        stable = [item for item in gitee.releases() if TAG_RE.fullmatch(item.get("tag_name", ""))]
        stable.sort(key=lambda item: item.get("created_at", ""), reverse=True)
        for item in stable[keep:]:
            if item["tag_name"] != tag:
                gitee.delete_release(item["id"])
                log(f"pruned {item['tag_name']}")
    return release


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--tag")
    parser.add_argument("--assets-dir")
    parser.add_argument("--list-assets", action="store_true", help="print the asset names that are mirrored and exit")
    parser.add_argument("--notes-file")
    parser.add_argument("--repo", default=os.environ.get("GITEE_REPO", DEFAULT_REPO))
    parser.add_argument("--keep", type=int, default=3, help="stable Gitee releases to keep (0 disables pruning)")
    parser.add_argument("--skip-download-check", action="store_true")
    args = parser.parse_args(argv)
    if args.list_assets:
        print("\n".join(ASSETS))
        return 0
    if not args.tag or not args.assets_dir:
        parser.error("--tag and --assets-dir are required")
    token = os.environ.get("GITEE_TOKEN", "")
    if not token:
        print("GITEE_TOKEN is not set", file=sys.stderr)
        return 2
    notes = Path(args.notes_file).read_text().strip() if args.notes_file else ""
    notes = notes or f"国内网络更新镜像，内容与 GitHub Release {args.tag} 一致，仅包含客户端更新和安装脚本用到的资产。"
    gitee = Gitee(args.repo, token, os.environ.get("GITEE_API_BASE", "https://gitee.com/api/v5"),
                  os.environ.get("GITEE_WEB_BASE", "https://gitee.com"),
                  sleep=(lambda _s: None) if os.environ.get("GITEE_MIRROR_NO_SLEEP") else time.sleep)
    try:
        sync(gitee, args.assets_dir, args.tag, notes, args.keep, not args.skip_download_check)
    except MirrorError as error:
        print(f"mirror failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
