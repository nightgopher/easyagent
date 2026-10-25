"""Exercise scripts/gitee_mirror.py against an in-process fake of the Gitee API."""
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.parse
from email import policy
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gitee_mirror as mirror

TOKEN = "test-token-123"
OWNER_REPO = "owner/repo"
API = f"/api/v5/repos/{OWNER_REPO}"


class FakeGitee:
    """Just enough of Gitee: releases, attachments, public download links."""

    def __init__(self):
        self.releases, self.files = [], {}  # releases: dicts; files: {release_id: {name: bytes}}
        self.counter = 0
        self.upload_count = 0
        self.token_violations = []
        self.upload_status = None  # force this status for uploads
        self.fail_after_store_once = False
        self.fail_after_create_once = False
        self.corrupt = set()
        self.report_size = True
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, status, payload=None, raw=None):
                data = raw if raw is not None else json.dumps(payload if payload is not None else {}).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def body(self):
                return self.rfile.read(int(self.headers.get("Content-Length", 0)))

            def handle_any(self, method):
                parsed = urllib.parse.urlparse(self.path)
                query = dict(urllib.parse.parse_qsl(parsed.query))
                path = parsed.path
                payload = self.body() if method in ("POST", "PATCH") else b""
                form, upload = {}, None
                ctype = self.headers.get("Content-Type", "")
                if method == "POST" and ctype.startswith("multipart/"):
                    msg = BytesParser(policy=policy.default).parsebytes(
                        f"Content-Type: {ctype}\r\n\r\n".encode() + payload)
                    for part in msg.iter_parts():
                        field = part.get_param("name", header="content-disposition")
                        if part.get_filename():
                            upload = (part.get_filename(), part.get_payload(decode=True))
                        else:
                            form[field] = part.get_content()
                elif method == "POST":
                    form = dict(urllib.parse.parse_qsl(payload.decode()))
                if path.startswith("/api/"):
                    if (query if method in ("GET", "DELETE") else form).get("access_token") != TOKEN:
                        outer.token_violations.append(f"{method} {path}")
                        return self.reply(401, {"message": "bad token"})
                    return self.api(method, path[len("/api/v5"):], query, form, upload)
                match = re.fullmatch(rf"/{OWNER_REPO}/releases/download/([^/]+)/(.+)", path)
                if match:
                    return self.download(match.group(1), urllib.parse.unquote(match.group(2)))
                self.reply(404)

            def api(self, method, path, query, form, upload):
                repo = f"/repos/{OWNER_REPO}"
                if path == repo:
                    return self.reply(200, {"default_branch": "master"})
                if path == f"{repo}/releases" and method == "GET":
                    return self.reply(200, outer.releases)
                if path == f"{repo}/releases" and method == "POST":
                    outer.counter += 1
                    release = {"id": outer.counter, "tag_name": form["tag_name"], "name": form["name"],
                               "body": form["body"], "created_at": f"2026-10-24T00:00:{outer.counter:02d}Z"}
                    outer.releases.append(release)
                    outer.files[release["id"]] = {}
                    if outer.fail_after_create_once:
                        outer.fail_after_create_once = False
                        return self.reply(503, {"message": "response lost"})
                    return self.reply(201, release)
                match = re.fullmatch(rf"{repo}/releases/tags/(.+)", path)
                if match:
                    found = next((r for r in outer.releases if r["tag_name"] == match.group(1)), None)
                    # Real Gitee answers 200 with `null` when the tag has no release.
                    return self.reply(200, found) if found else self.reply(200, raw=b"null")
                match = re.fullmatch(rf"{repo}/releases/(\d+)/attach_files", path)
                if match and method == "GET":
                    files = outer.files.get(int(match.group(1)), {})
                    items = [{"name": n, **({"size": len(b)} if outer.report_size else {})} for n, b in files.items()]
                    return self.reply(200, items)
                if match and method == "POST":
                    if outer.upload_status and not outer.fail_after_store_once:
                        return self.reply(outer.upload_status, {"message": "boom"})
                    outer.upload_count += 1
                    outer.files[int(match.group(1))][upload[0]] = upload[1]
                    if outer.fail_after_store_once:
                        outer.fail_after_store_once = False
                        return self.reply(503, {"message": "response lost"})
                    return self.reply(201, {"name": upload[0]})
                match = re.fullmatch(rf"{repo}/releases/(\d+)", path)
                if match and method == "DELETE":
                    rid = int(match.group(1))
                    outer.releases[:] = [r for r in outer.releases if r["id"] != rid]
                    outer.files.pop(rid, None)
                    return self.reply(204, raw=b"")
                self.reply(404)

            def download(self, tag, name):
                release = next((r for r in outer.releases if r["tag_name"] == tag), None)
                data = outer.files.get(release["id"], {}).get(name) if release else None
                if data is None:
                    return self.reply(404)
                self.reply(200, raw=data + b"X" if name in outer.corrupt else data)

            def do_GET(self):
                self.handle_any("GET")

            def do_POST(self):
                self.handle_any("POST")

            def do_DELETE(self):
                self.handle_any("DELETE")

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()

    def seed(self, tag, created_at):
        self.counter += 1
        self.releases.append({"id": self.counter, "tag_name": tag, "name": tag, "body": "x", "created_at": created_at})
        self.files[self.counter] = {}


class MirrorTest(unittest.TestCase):
    def setUp(self):
        self.fake = FakeGitee()
        self.addCleanup(self.fake.close)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        self.tag = "v1.2.3"
        self.build_assets()
        self.logs = []

    def build_assets(self):
        sums = []
        for name in mirror.ASSETS:
            if name in ("checksums.txt", "release.json"):
                continue
            (self.dir / name).write_bytes(f"payload of {name}".encode() * 3)
        (self.dir / "release.json").write_text(json.dumps({"version": self.tag, "commit": "a" * 40}))
        for name in mirror.ASSETS:
            if name != "checksums.txt":
                sums.append(hashlib.sha256((self.dir / name).read_bytes()).hexdigest() + "  " + name)
        (self.dir / "checksums.txt").write_text("\n".join(sums) + "\n")

    def client(self):
        base = f"http://127.0.0.1:{self.fake.port}"
        return mirror.Gitee(OWNER_REPO, TOKEN, base + "/api/v5", base, sleep=lambda _s: None)

    def run_sync(self, **kwargs):
        kwargs.setdefault("keep", 3)
        return mirror.sync(self.client(), self.dir, self.tag, "notes", log=self.logs.append, **kwargs)

    def test_publishes_every_asset_then_a_rerun_changes_nothing(self):
        self.run_sync()
        self.assertEqual(len(self.fake.releases), 1)
        self.assertEqual(sorted(self.fake.files[1]), sorted(mirror.ASSETS))
        self.assertEqual(self.fake.upload_count, len(mirror.ASSETS))
        self.run_sync()
        self.assertEqual(len(self.fake.releases), 1)
        self.assertEqual(self.fake.upload_count, len(mirror.ASSETS))
        self.assertEqual(self.fake.token_violations, [])

    def test_hash_mismatch_is_rejected_before_any_request(self):
        (self.dir / "easyagent-android.apk").write_bytes(b"tampered")
        with self.assertRaisesRegex(mirror.MirrorError, "SHA-256 mismatch"):
            self.run_sync()
        self.assertEqual((self.fake.releases, self.fake.upload_count), ([], 0))

    def test_missing_file_checksum_and_wrong_manifest_are_rejected(self):
        (self.dir / "easyagent-bridge-linux-arm64").unlink()
        with self.assertRaisesRegex(mirror.MirrorError, "missing asset"):
            self.run_sync()
        self.build_assets()
        lines = [l for l in (self.dir / "checksums.txt").read_text().splitlines() if "android.apk" not in l]
        (self.dir / "checksums.txt").write_text("\n".join(lines) + "\n")
        with self.assertRaisesRegex(mirror.MirrorError, "no checksum listed"):
            self.run_sync()
        self.build_assets()
        (self.dir / "release.json").write_text(json.dumps({"version": "v9.9.9"}))
        self.build_checksums_only()
        with self.assertRaisesRegex(mirror.MirrorError, "release.json does not describe"):
            self.run_sync()
        self.assertEqual(self.fake.upload_count, 0)

    def build_checksums_only(self):
        sums = [hashlib.sha256((self.dir / n).read_bytes()).hexdigest() + "  " + n for n in mirror.ASSETS if n != "checksums.txt"]
        (self.dir / "checksums.txt").write_text("\n".join(sums) + "\n")

    def test_prerelease_tags_are_not_mirrored(self):
        self.tag = "v1.2.3-rc.1"
        self.build_assets()
        with self.assertRaisesRegex(mirror.MirrorError, "only stable"):
            self.run_sync()

    def test_an_existing_asset_with_other_content_is_never_replaced(self):
        self.fake.seed(self.tag, "2026-10-24T00:00:01Z")
        self.fake.files[1]["easyagent-android.apk"] = b"different"
        with self.assertRaisesRegex(mirror.MirrorError, "never replaced"):
            self.run_sync()
        self.assertEqual(self.fake.upload_count, 0)

    def test_public_download_that_differs_from_github_is_caught(self):
        self.fake.corrupt = {"easyagent-linux-amd64"}
        with self.assertRaisesRegex(mirror.MirrorError, "different bytes"):
            self.run_sync()

    def test_pruning_keeps_the_newest_and_leaves_non_version_releases_alone(self):
        for index, tag in enumerate(["v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0"], start=1):
            self.fake.seed(tag, f"2026-10-0{index}T00:00:00Z")
        self.fake.seed("nightly", "2026-09-01T00:00:00Z")
        self.tag = "v0.5.0"
        self.build_assets()
        self.run_sync(keep=3)
        self.assertEqual(sorted(r["tag_name"] for r in self.fake.releases), ["nightly", "v0.3.0", "v0.4.0", "v0.5.0"])

    def test_pruning_is_skipped_when_disabled_and_after_a_failed_verification(self):
        for index, tag in enumerate(["v0.1.0", "v0.2.0"], start=1):
            self.fake.seed(tag, f"2026-10-0{index}T00:00:00Z")
        self.run_sync(keep=0)
        self.assertEqual(len(self.fake.releases), 3)
        self.fake.corrupt = {"release.json"}
        with self.assertRaises(mirror.MirrorError):
            self.run_sync(keep=1)
        self.assertEqual(len(self.fake.releases), 3)

    def test_a_retried_upload_does_not_create_a_duplicate(self):
        self.fake.fail_after_store_once = True
        self.run_sync()
        self.assertEqual(sorted(self.fake.files[1]), sorted(mirror.ASSETS))
        self.assertEqual(self.fake.upload_count, len(mirror.ASSETS))

    def test_a_release_created_but_not_acknowledged_is_reused(self):
        self.fake.fail_after_create_once = True
        self.run_sync()
        self.assertEqual(len(self.fake.releases), 1)
        self.assertEqual(sorted(self.fake.files[1]), sorted(mirror.ASSETS))

    def test_listing_without_sizes_still_skips_existing_assets(self):
        self.fake.report_size = False
        self.run_sync()
        self.run_sync()
        self.assertEqual(self.fake.upload_count, len(mirror.ASSETS))

    def cli(self, env_extra, *args):
        env = dict(os.environ, GITEE_API_BASE=f"http://127.0.0.1:{self.fake.port}/api/v5",
                   GITEE_WEB_BASE=f"http://127.0.0.1:{self.fake.port}", GITEE_MIRROR_NO_SLEEP="1", **env_extra)
        env.pop("GITEE_TOKEN", None) if "GITEE_TOKEN" not in env_extra else None
        script = Path(mirror.__file__)
        return subprocess.run([sys.executable, str(script), "--tag", self.tag, "--assets-dir", str(self.dir),
                               "--repo", OWNER_REPO, *args], capture_output=True, text=True, env=env)

    def test_cli_lists_exactly_the_mirrored_assets(self):
        listed = subprocess.run([sys.executable, str(Path(mirror.__file__)), "--list-assets"],
                                capture_output=True, text=True)
        self.assertEqual(listed.returncode, 0)
        self.assertEqual(listed.stdout.split(), list(mirror.ASSETS))

    def test_cli_requires_a_token_and_never_prints_it(self):
        self.assertEqual(self.cli({}).returncode, 2)
        self.fake.upload_status = 500
        failed = self.cli({"GITEE_TOKEN": TOKEN})
        self.assertEqual(failed.returncode, 1)
        self.assertNotIn(TOKEN, failed.stdout + failed.stderr)
        self.fake.upload_status = None
        ok = self.cli({"GITEE_TOKEN": TOKEN})
        self.assertEqual(ok.returncode, 0, ok.stderr)
        self.assertNotIn(TOKEN, ok.stdout + ok.stderr)
        self.assertEqual(self.fake.token_violations, [])


if __name__ == "__main__":
    unittest.main()
