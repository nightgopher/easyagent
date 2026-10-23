#!/usr/bin/env python3
"""Release policy and deterministic manifests. No credentials or network needed."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import zipfile

TAG_RE = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(alpha|beta|rc)\.([1-9][0-9]*))?\Z")
TARGETS = ("linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64")
ASSETS = tuple(f"{name}-{target}" for target in TARGETS for name in ("easyagent", "easyagent-bridge"))


def git(*args):
    return subprocess.check_output(["git", *args], text=True, stderr=subprocess.PIPE).strip()


def version_key(tag):
    match = TAG_RE.fullmatch(tag)
    if not match:
        raise ValueError("version must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-(alpha|beta|rc).N (N >= 1)")
    major, minor, patch, phase, count = match.groups()
    return (int(major), int(minor), int(patch), {"alpha": 0, "beta": 1, "rc": 2, None: 3}[phase], int(count or 0))


def desktop_assets(tag):
    version_key(tag)
    # Preserve historical manifests; Electron is retired starting with v0.16.0.
    if version_key(tag) >= version_key("v0.16.0-alpha.1"):
        return ()
    return (f"EasyAgent-{tag[1:]}-arm64.dmg",)


def native_assets(tag):
    version_key(tag)
    linux = ()
    # Linux desktop packaging shares the macOS update channel from v0.21.0:
    # deb (system install), tar.gz (per-user auto-update) and signed manifests.
    if version_key(tag) >= version_key("v0.21.0-alpha.1"):
        linux = (
            "easyagent-native-linux-amd64.deb",
            "easyagent-native-linux-arm64.deb",
            "easyagent-native-install-linux-amd64.sh",
            "easyagent-native-install-linux-arm64.sh",
            "update-linux-amd64.json",
            "update-linux-arm64.json",
            f"easyagent-{tag[1:]}-linux-amd64.tar.gz",
            f"easyagent-{tag[1:]}-linux-arm64.tar.gz",
        )
    # Signed archives and their manifests are mandatory for the native-only release.
    if version_key(tag) >= version_key("v0.16.0-alpha.1"):
        return ("easyagent-native-darwin-arm64.dmg", "update-darwin-arm64.json", f"easyagent-{tag[1:]}-darwin-arm64.tar.gz") + linux
    # MyGo native client ships an Apple Silicon DMG with the promoted release.
    if version_key(tag) >= version_key("v0.15.12"):
        return ("easyagent-native-darwin-arm64.dmg",)
    return ()


def server_assets(tag):
    version_key(tag)
    # Debian/Ubuntu server packages (easyagent serve + Feishu bridge + systemd).
    if version_key(tag) >= version_key("v0.21.0-alpha.1"):
        return (f"easyagent-server_{tag[1:]}-1_amd64.deb", f"easyagent-server_{tag[1:]}-1_arm64.deb")
    return ()


def android_assets(tag):
    if version_key(tag) >= version_key("v0.19.2-alpha.1"):
        return ("easyagent-android.apk", "easyagent-android.zip")
    if version_key(tag) >= version_key("v0.19.0-alpha.1"):
        return ("easyagent-android.apk",)
    return ()


def verify_android_archive(apk, archive):
    try:
        with zipfile.ZipFile(archive) as bundle:
            if (bundle.namelist() != ["easyagent-android.apk"]
                    or bundle.getinfo("easyagent-android.apk").file_size != Path(apk).stat().st_size
                    or bundle.read("easyagent-android.apk") != Path(apk).read_bytes()):
                raise ValueError("Android ZIP must contain only the identical signed APK")
    except (zipfile.BadZipFile, RuntimeError) as error:
        raise ValueError("invalid Android ZIP archive") from error


def android_version(tag):
    major, minor, patch, phase, count = version_key(tag)
    # Reserve 100 codes per prerelease phase; the final release follows rc.99.
    code = major * 10_000_000 + minor * 100_000 + patch * 1000 + phase * 100 + count
    if minor > 99 or patch > 99 or count > 99 or code > 2_100_000_000:
        raise ValueError("Android version requires minor/patch/prerelease N <= 99 and versionCode <= 2100000000")
    return tag[1:], code


def android_source(sha):
    """Git tree of mobile-native/ at the release commit, the APK's only source.

    A release whose tree matches the installed app's ships the same Android code
    under a new version, so the in-app updater does not offer it.
    """
    tree = git("rev-parse", f"{sha}^{{commit}}:mobile-native")
    if not re.fullmatch(r"[0-9a-f]{40}", tree):
        raise ValueError("mobile-native tree must be a full SHA-1")
    return tree


def required_assets(tag):
    required = ASSETS
    if version_key(tag) >= version_key("v0.2.0-alpha.1"):
        required = required + desktop_assets(tag)
    return required + native_assets(tag) + android_assets(tag) + server_assets(tag)


def check_next(tag, tags):
    key = version_key(tag)
    versions = [other for other in tags if TAG_RE.fullmatch(other) and other != tag]
    if any(version_key(other) >= key for other in versions):
        raise ValueError("release must be newer than existing versions; published tags must never be reused or moved")


def release_notes(tag, content):
    # Exact section; do not accidentally use Unreleased or another version's notes.
    match = re.search(r"^## \[" + re.escape(tag) + r"\](?: - \d{4}-\d{2}-\d{2})?\s*\n(.*?)(?=^## |\Z)", content, re.M | re.S)
    if not match or not match.group(1).strip() or "TODO" in match.group(1):
        raise ValueError(f"CHANGELOG.md needs a non-empty ## [{tag}] section without TODO placeholders")
    return match.group(1).strip() + "\n"


def validate(tag, main_ref="origin/main"):
    version_key(tag)
    if git("status", "--porcelain", "--untracked-files=normal"):
        raise ValueError("release checkout must be clean, including untracked files")
    ref = "refs/tags/" + tag
    if git("cat-file", "-t", ref) != "tag":
        raise ValueError("release tags must be annotated (git tag -a or -s), not lightweight")
    sha = git("rev-parse", ref + "^{commit}")
    if git("rev-parse", "HEAD") != sha:
        raise ValueError("checkout does not match the release tag")
    # A merged feature commit is acceptable; an unmerged branch is not.
    subprocess.run(["git", "merge-base", "--is-ancestor", sha, main_ref], check=True, capture_output=True)
    check_next(tag, git("tag", "--list").splitlines())
    notes = release_notes(tag, git("show", sha + ":CHANGELOG.md"))
    return sha, notes


def make_tag(tag, main_ref):
    version_key(tag)
    if git("symbolic-ref", "--short", "HEAD") != "main":
        raise ValueError("create release tags from main")
    if git("status", "--porcelain", "--untracked-files=normal"):
        raise ValueError("working tree must be clean, including untracked files")
    if git("rev-parse", "HEAD") != git("rev-parse", main_ref):
        raise ValueError("main must match the fetched remote main")
    if tag in git("tag", "--list").splitlines():
        raise ValueError("tag already exists")
    check_next(tag, git("tag", "--list").splitlines())
    notes = release_notes(tag, Path("CHANGELOG.md").read_text())
    subprocess.run(["git", "tag", "-a", tag, "-m", f"EasyAgent {tag}\n\n{notes}"], check=True)
    print(f"Created annotated tag {tag}; publish explicitly with: git push origin refs/tags/{tag}")


def manifest(directory, tag, sha, core_only=False):
    version_key(tag)
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("manifest requires a full commit SHA")
    directory = Path(directory)
    # Server debs are built by the core job; a partial core-only manifest must
    # account for them even though it can never be published.
    required = ASSETS + server_assets(tag) if core_only else required_assets(tag)
    allowed = set(required) | {"release.json", "checksums.txt"}
    extra = {p.name for p in directory.iterdir()} - allowed
    if extra:
        raise ValueError(f"unexpected release files: {sorted(extra)}")
    assets = {}
    for name in required:
        path = directory / name
        if not path.is_file() or path.is_symlink() or path.stat().st_size == 0:
            raise ValueError(f"missing or empty release asset: {name}")
        assets[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    if "easyagent-android.zip" in required:
        verify_android_archive(directory / "easyagent-android.apk", directory / "easyagent-android.zip")
    data = {"version": tag, "commit": sha, "assets": assets}
    if "easyagent-android.apk" in required:
        data["sources"] = {"android": android_source(sha)}
    if core_only:
        data["core_only"] = True
    (directory / "release.json").write_text(json.dumps(data, sort_keys=True, indent=2) + "\n")
    assets["release.json"] = hashlib.sha256((directory / "release.json").read_bytes()).hexdigest()
    (directory / "checksums.txt").write_text("".join(f"{digest}  {name}\n" for name, digest in sorted(assets.items())))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for command in ("validate", "tag"):
        sub = commands.add_parser(command)
        sub.add_argument("version")
        sub.add_argument("--main-ref", default="origin/main")
        if command == "validate":
            sub.add_argument("--output")
            sub.add_argument("--notes-file")
    sub = commands.add_parser("manifest")
    sub.add_argument("version")
    sub.add_argument("sha")
    sub.add_argument("directory")
    sub.add_argument("--core-only", action="store_true", help="validate core build outputs only; this partial manifest cannot be published")
    args = parser.parse_args()
    try:
        if args.command == "tag":
            make_tag(args.version, args.main_ref)
        elif args.command == "manifest":
            manifest(args.directory, args.version, args.sha, core_only=args.core_only)
        else:
            sha, notes = validate(args.version, args.main_ref)
            if args.notes_file:
                Path(args.notes_file).write_text(notes)
            if args.output:
                with open(args.output, "a") as output:
                    output.write(f"version={args.version}\nsha={sha}\nprerelease={str('-' in args.version).lower()}\n")
            print(f"Validated {args.version} at {sha}")
    except (ValueError, subprocess.CalledProcessError, OSError) as error:
        print(f"Release rejected: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
