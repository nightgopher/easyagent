import copy
import unittest
from decimal import Decimal

from check_coverage import coverage
from check_lint_baseline import finding_key, match, new_findings, pruned


class QualityGateTests(unittest.TestCase):
    def test_statement_weighting_and_boundary(self):
        self.assertEqual(coverage("mode: atomic\na.go:1.1,2.1 7 5\na.go:3.1,4.1 3 0\n"), Decimal("70"))
        self.assertLess(coverage("mode: atomic\na.go:1.1,2.1 69 1\na.go:3.1,4.1 31 0\n"), Decimal("70"))

    def test_bad_profiles_fail_closed(self):
        for profile in ("", "mode: bad", "mode: atomic", "mode: count\nx 0 1", "mode: set\nx 1 -1", "mode: set\nx nope 0"):
            with self.subTest(profile=profile), self.assertRaises(ValueError):
                coverage(profile)

    def test_lint_baseline_does_not_allow_new_instances_or_changed_code(self):
        issue = {"FromLinter": "errcheck", "Text": "unchecked error", "SourceLines": [" f()"], "Pos": {"Filename": "a.go", "Line": 10}}
        linter, file, message, source = finding_key(issue)
        baseline = {"findings": [{"linter": linter, "file": file, "message": message, "source_sha256": source, "count": 1}]}
        moved = copy.deepcopy(issue)
        moved["Pos"]["Line"] = 20
        self.assertEqual(new_findings({"Issues": [moved]}, baseline), [])
        self.assertEqual(len(new_findings({"Issues": [issue, moved]}, baseline)), 1)
        changed = copy.deepcopy(issue)
        changed["SourceLines"] = [" g()"]
        self.assertEqual(len(new_findings({"Issues": [changed]}, baseline)), 1)
        self.assertEqual(new_findings({"Issues": None}, baseline), [])
        with self.assertRaises(KeyError):
            new_findings({}, baseline)

    def test_fixed_findings_must_leave_the_baseline(self):
        issue = {"FromLinter": "errcheck", "Text": "unchecked error", "SourceLines": [" f()"], "Pos": {"Filename": "a.go", "Line": 10}}
        linter, file, message, source = finding_key(issue)
        entry = {"linter": linter, "file": file, "message": message, "source_sha256": source, "line_at_baseline": 10, "count": 2}
        other = dict(entry, file="b.go", count=1)
        baseline = {"policy": "kept", "findings": [entry, other]}
        findings, unused = match({"Issues": [issue]}, baseline)
        self.assertEqual(findings, [])
        self.assertEqual(sum(unused.values()), 2)
        shrunk = pruned(baseline, unused)
        self.assertEqual(shrunk["policy"], "kept")
        self.assertEqual(shrunk["findings"], [dict(entry, count=1)])
        self.assertEqual(match({"Issues": [issue]}, shrunk), ([], {}))
        # Pruning cannot make room for a finding that was never allowed.
        self.assertEqual(len(new_findings({"Issues": [issue, issue]}, shrunk)), 1)


if __name__ == "__main__":
    unittest.main()
