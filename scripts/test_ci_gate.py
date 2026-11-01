import copy
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from ci_gate import CHECKS, changed_paths, needs_checks, verified


class CIGateTests(unittest.TestCase):
    def test_git_rename_cannot_disguise_deleted_code_as_documentation(self):
        original = Path.cwd()
        with tempfile.TemporaryDirectory() as temporary:
            try:
                os.chdir(temporary)
                def git(*args):
                    return subprocess.run(["git", "-c", "commit.gpgsign=false", *args],
                                          check=True, capture_output=True, text=True).stdout.strip()
                git("init", "-q")
                git("config", "user.name", "CI gate test")
                git("config", "user.email", "ci@example.invalid")
                Path("code.go").write_text("package fixture\n")
                git("add", "code.go")
                git("commit", "-qm", "source")
                base = git("rev-parse", "HEAD")
                Path("docs").mkdir()
                git("mv", "code.go", "docs/README.md")
                git("commit", "-qm", "rename")
                paths = changed_paths(base, git("rev-parse", "HEAD"))
                self.assertEqual(set(paths), {"code.go", "docs/README.md"})
                self.assertTrue(needs_checks(paths))
                self.assertIsNone(changed_paths("0" * 40, base))
                self.assertIsNone(changed_paths("f" * 40, base))
            finally:
                os.chdir(original)

    def test_only_documentation_skips_expensive_checks(self):
        self.assertFalse(needs_checks(["README.md", "README.zh-CN.md", "sdk/README.md", "docs/guide.md", "docs/image.png"]))
        for paths in (None, [], ["internal/prompts/system.md"], ["internal/web/static/README.md"],
                      ["README.md", "go.mod"], [".github/workflows/verify.yml"],
                      ["cmd/deleted.go", "docs/renamed.md"]):
            with self.subTest(paths=paths):
                self.assertTrue(needs_checks(paths))

    def test_required_check_accepts_only_the_expected_complete_results(self):
        for full, result in (("true", "success"), ("false", "skipped")):
            needs = {name: {"result": result} for name in CHECKS}
            needs["changes"] = {"result": "success", "outputs": {"full": full}}
            self.assertTrue(verified(needs))
            for name in CHECKS | {"changes"}:
                for bad in ("failure", "cancelled"):
                    broken = copy.deepcopy(needs)
                    broken[name]["result"] = bad
                    self.assertFalse(verified(broken))
                missing = copy.deepcopy(needs)
                del missing[name]
                self.assertFalse(verified(missing))
            broken = copy.deepcopy(needs)
            broken["test"]["result"] = "skipped" if full == "true" else "success"
            self.assertFalse(verified(broken))
            for output in ({}, {"full": ""}, {"full": "unknown"}):
                broken = copy.deepcopy(needs)
                broken["changes"]["outputs"] = output
                self.assertFalse(verified(broken))


if __name__ == "__main__":
    unittest.main()
