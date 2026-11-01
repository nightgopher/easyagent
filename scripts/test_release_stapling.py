"""Exercise the real workflow shell against a simulated Apple ticket service."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parent.parent
NETWORK_ERROR = 'Error Domain=NSURLErrorDomain Code=-1005 "The network connection was lost."'


class StaplingTests(unittest.TestCase):
    def exercise(self, results, validation=0):
        for name in ("release.yml", "release-check.yml"):
            with self.subTest(workflow=name), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp)
                workflow = (ROOT / ".github/workflows" / name).read_text()
                start = workflow.index("          for attempt in 1 2 3; do\n")
                last = '          xcrun stapler validate "$DMG"'
                end = workflow.index(last, start) + len(last)
                script = textwrap.dedent(workflow[start:end])
                command = directory / "xcrun"
                command.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
path = pathlib.Path(os.environ["CALLS"])
calls = json.loads(path.read_text()) if path.exists() else []
calls.append(sys.argv[2])
path.write_text(json.dumps(calls))
if sys.argv[2] == "validate":
    sys.exit(int(os.environ["VALIDATION"]))
results = json.loads(os.environ["RESULTS"])
code, message = results[calls.count("staple") - 1]
print(message)
sys.exit(code)
''')
                command.chmod(0o755)
                sleeper = directory / "sleep"
                sleeper.write_text('#!/bin/sh\nprintf "%s\\n" "$1" >> "$SLEEPS"\n')
                sleeper.chmod(0o755)
                calls, sleeps = directory / "calls.json", directory / "sleeps"
                env = dict(os.environ, PATH=str(directory) + os.pathsep + os.environ["PATH"],
                           DMG=str(directory / "release.dmg"), CALLS=str(calls), SLEEPS=str(sleeps),
                           RESULTS=json.dumps(results), VALIDATION=str(validation))
                run = subprocess.run(["bash", "-e", "-c", script], env=env, capture_output=True, text=True)
                yield run.returncode, json.loads(calls.read_text()), sleeps.read_text().splitlines() if sleeps.exists() else []

    def test_transient_disconnects_recover_without_rebuilding_or_resubmitting(self):
        for code, calls, sleeps in self.exercise([(68, NETWORK_ERROR), (68, NETWORK_ERROR), (0, "stapled")]):
            self.assertEqual((code, calls, sleeps), (0, ["staple", "staple", "staple", "validate"], ["5", "10"]))

    def test_permanent_ticket_errors_stop_immediately(self):
        for code, calls, sleeps in self.exercise([(68, "Ticket signature is invalid")]):
            self.assertEqual((code, calls, sleeps), (68, ["staple"], []))

    def test_transient_failures_are_bounded_and_still_fail_publication(self):
        for code, calls, sleeps in self.exercise([(68, NETWORK_ERROR)] * 3):
            self.assertEqual((code, calls, sleeps), (68, ["staple"] * 3, ["5", "10"]))

    def test_final_validation_failure_is_not_retried_or_ignored(self):
        for code, calls, sleeps in self.exercise([(0, "stapled")], validation=65):
            self.assertEqual((code, calls, sleeps), (65, ["staple", "validate"], []))


if __name__ == "__main__":
    unittest.main()
