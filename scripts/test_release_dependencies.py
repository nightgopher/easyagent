import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("release_dependencies", Path(__file__).with_name("release-dependencies.py"))
deps = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deps)


class ReleaseDependencyTests(unittest.TestCase):
    url = "https://github.com/owner/project/releases/download/v1.2/asset.zip"
    content = b"verified official release bytes"
    sha = hashlib.sha256(content).hexdigest()

    def fake_gh(self, arguments, output=None):
        if output is None:
            self.assertEqual(arguments, ["repos/owner/project/releases/tags/v1.2"])
            return json.dumps({"assets": [{"name": "asset.zip", "id": 123}]}).encode()
        self.assertEqual(arguments[-1], "repos/owner/project/releases/assets/123")
        output.write(self.content)

    def test_fetch_verifies_and_reuses_cache(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(deps, "gh", side_effect=self.fake_gh) as call:
            destination = Path(directory) / "cache" / "asset.zip"
            deps.fetch_asset(self.url, self.sha, destination)
            self.assertEqual(destination.read_bytes(), self.content)
            self.assertEqual(call.call_count, 2)
            deps.fetch_asset(self.url, self.sha, destination)
            self.assertEqual(call.call_count, 2)

    def test_hash_mismatch_preserves_existing_file(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(deps, "gh", side_effect=self.fake_gh):
            destination = Path(directory) / "asset.zip"
            destination.write_bytes(b"previous")
            with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
                deps.fetch_asset(self.url, "0" * 64, destination)
            self.assertEqual(destination.read_bytes(), b"previous")
            self.assertEqual([p.name for p in Path(directory).iterdir()], ["asset.zip"])

    def test_rejects_untrusted_or_malformed_urls(self):
        for url in [
            "https://example.com/owner/project/releases/download/v1/asset.zip",
            "https://github.com/owner/project/releases/download/../asset.zip",
            self.url + "?token=secret",
            "https://github.com/owner/project/releases/download/v1%2Fbad/asset.zip",
        ]:
            with self.subTest(url=url), self.assertRaises(ValueError):
                deps.asset_parts(url)

    def test_native_cache_uses_pinned_plugin_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            plugin = root / "module" / "plugins" / "terminal" / "mygo-plugin.json"
            plugin.parent.mkdir(parents=True)
            plugin.write_text(json.dumps({"libraries": [{"name": "libghostty-vt", "files": {
                "darwin-arm64": {"name": "asset.zip", "url": self.url, "sha256": self.sha},
            }}]}))
            with patch.object(deps.subprocess, "run", return_value=SimpleNamespace(stdout=json.dumps({"Dir": str(root / "module")}).encode())), \
                    patch.object(deps, "gh", side_effect=self.fake_gh):
                deps.prepare_natives(["darwin-arm64"], root / "cache")
            self.assertEqual((root / "cache" / "mygo" / "natives" / self.sha / "asset.zip").read_bytes(), self.content)


if __name__ == "__main__":
    unittest.main()
