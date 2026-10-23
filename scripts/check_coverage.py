#!/usr/bin/env python3
"""Require the weighted Go statement coverage to meet the configured floor."""
import argparse
from decimal import Decimal
from pathlib import Path
import sys


def coverage(profile):
    lines = profile.splitlines()
    if not lines or lines[0] not in {"mode: atomic", "mode: count", "mode: set"}:
        raise ValueError("missing or invalid coverage mode")
    covered = total = 0
    for line in lines[1:]:
        _, statements, count = line.rsplit(None, 2)
        statements, count = int(statements), int(count)
        if statements < 0 or count < 0:
            raise ValueError("negative coverage counter")
        total += statements
        if count > 0:
            covered += statements
    if total == 0:
        raise ValueError("coverage profile contains no statements")
    return Decimal(covered) * 100 / Decimal(total)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("--minimum", type=Decimal, default=Decimal("70.0"))
    args = parser.parse_args()
    try:
        if not args.minimum.is_finite() or not 0 <= args.minimum <= 100:
            raise ValueError("minimum must be between 0 and 100")
        actual = coverage(args.profile.read_text())
    except (OSError, ValueError) as error:
        print(f"Cannot validate coverage: {error}", file=sys.stderr)
        return 2
    print(f"SDK/server statement coverage: {actual:.2f}% (minimum {args.minimum}%)")
    return 0 if actual >= args.minimum else 1


if __name__ == "__main__":
    sys.exit(main())
