#!/usr/bin/env python3
"""Reject findings absent from the fixed legacy lint baseline (never refresh it).

The baseline only shrinks: entries the report no longer contains fail the check
until --prune removes them. Pruning never adds an entry.
"""
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


def baseline_key(item):
    return (item["linter"], item["file"], item["message"], item["source_sha256"])


def match(report, baseline):
    """Findings outside the baseline, and baseline allowances nothing used."""
    allowed = Counter()
    for item in baseline["findings"]:
        allowed[baseline_key(item)] += item["count"]
    unexpected = []
    for issue in report["Issues"] or []:
        key = finding_key(issue)
        if allowed[key] > 0:
            allowed[key] -= 1
        else:
            unexpected.append(issue)
    return unexpected, +allowed


def new_findings(report, baseline):
    return match(report, baseline)[0]


def pruned(baseline, unused):
    """The baseline without unused allowances; entry order is kept."""
    unused = Counter(unused)
    findings = []
    for item in baseline["findings"]:
        key = baseline_key(item)
        drop = min(unused[key], item["count"])
        unused[key] -= drop
        if item["count"] > drop:
            findings.append({**item, "count": item["count"] - drop})
    return {**baseline, "findings": findings}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path)
    parser.add_argument("--baseline", type=Path, default=Path(".golangci-baseline.json"))
    parser.add_argument("--prune", action="store_true", help="remove baseline entries the report no longer contains")
    args = parser.parse_args()
    try:
        report = json.loads(args.report.read_text())
        baseline = json.loads(args.baseline.read_text())
        findings, unused = match(report, baseline)
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f"Cannot validate lint report: {error}", file=sys.stderr)
        return 2
    for issue in findings:
        pos = issue["Pos"]
        print(f'{pos["Filename"]}:{pos["Line"]}: {issue["FromLinter"]}: {issue["Text"]}')
    print(f"Lint: {len(report['Issues'] or [])} findings, {len(findings)} outside the fixed baseline")
    stale = sum(unused.values())
    if stale and args.prune:
        args.baseline.write_text(json.dumps(pruned(baseline, unused), indent=2, ensure_ascii=False) + "\n")
        print(f"Pruned {stale} fixed findings from {args.baseline}")
        stale = 0
    elif stale:
        for (linter, file, message, _), count in sorted(unused.items()):
            print(f"{file}: {linter}: {message} (fixed x{count})")
        print(f"Baseline: {stale} fixed findings still allowed; remove them with --prune on the Linux CI report")
    return 1 if findings or stale else 0


if __name__ == "__main__":
    sys.exit(main())
