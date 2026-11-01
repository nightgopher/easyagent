#!/usr/bin/env python3
"""Keep the required check present while skipping documentation-only work."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

CHECKS = {"test", "web-scripts", "android", "ios", "lint", "race-coverage", "vulnerabilities"}
DOCS = {"AGENTS.md", "CHANGELOG.md", "CONTRIBUTING.md"}


def needs_checks(paths):
    def documentation(path):
        if path.startswith("internal/web/static/"):
            return False  # Embedded/served assets, even when named README.md.
        return (path.startswith("docs/") or path in DOCS
                or (Path(path).name.startswith("README") and path.endswith(".md")))
    # Unknown/empty diffs must not silently disable verification. Markdown
    # prompts inside sdk/ or internal/ are runtime inputs, not documentation.
    return not paths or not all(documentation(path) for path in paths)


def changed_paths(base, head):
    if not all(re.fullmatch(r"[0-9a-f]{40}", ref or "") and set(ref) != {"0"} for ref in (base, head)):
        return None
    result = subprocess.run(["git", "diff", "--no-renames", "--name-only", "-z", base, head, "--"],
                            capture_output=True, check=False)
    if result.returncode:
        return None
    return [p.decode("utf-8", "surrogateescape") for p in result.stdout.split(b"\0") if p]


def verified(needs):
    if set(needs) != CHECKS | {"changes"} or needs["changes"].get("result") != "success":
        return False
    full = needs["changes"].get("outputs", {}).get("full")
    if full not in ("true", "false"):
        return False
    expected = "success" if full == "true" else "skipped"
    return all(needs[name].get("result") == expected for name in CHECKS)


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in ("changes", "verify"):
        raise ValueError("usage: ci_gate.py changes|verify")
    if sys.argv[1] == "verify":
        needs = json.loads(os.environ["NEEDS"])
        print("Required checks:", {k: v.get("result") for k, v in needs.items()})
        return 0 if verified(needs) else 1
    paths = None
    if os.environ.get("FORCE_FULL") != "true":
        paths = changed_paths(os.environ.get("BASE_SHA"), os.environ.get("HEAD_SHA"))
    full = needs_checks(paths)
    print("Full verification" if full else "Documentation only; required verify check remains enabled")
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        output.write(f"full={str(full).lower()}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
