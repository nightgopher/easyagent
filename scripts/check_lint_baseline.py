#!/usr/bin/env python3
"""Reject findings absent from the fixed legacy lint baseline (never refresh it)."""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import sys


def finding_key(issue):
    source = "\n".join(line.strip() for line in issue["SourceLines"])
    return (issue["FromLinter"], issue["Pos"]["Filename"], issue["Text"],
            hashlib.sha256(source.encode()).hexdigest())


def new_findings(report, baseline):
    allowed = Counter()
    for item in baseline["findings"]:
        key = (item["linter"], item["file"], item["message"], item["source_sha256"])
        allowed[key] += item["count"]
    unexpected = []
    for issue in report["Issues"] or []:
        key = finding_key(issue)
        if allowed[key] > 0:
            allowed[key] -= 1
        else:
            unexpected.append(issue)
    return unexpected


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path)
    parser.add_argument("--baseline", type=Path, default=Path(".golangci-baseline.json"))
    args = parser.parse_args()
    try:
        report = json.loads(args.report.read_text())
        baseline = json.loads(args.baseline.read_text())
        findings = new_findings(report, baseline)
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f"Cannot validate lint report: {error}", file=sys.stderr)
        return 2
    for issue in findings:
        pos = issue["Pos"]
        print(f'{pos["Filename"]}:{pos["Line"]}: {issue["FromLinter"]}: {issue["Text"]}')
    print(f"Lint: {len(report['Issues'] or [])} findings, {len(findings)} outside the fixed baseline")
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
