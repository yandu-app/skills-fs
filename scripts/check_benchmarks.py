#!/usr/bin/env python3
"""Parse benchstat output and fail on significant time regressions."""

import os
import re
import shutil
import subprocess
import sys

THRESHOLD = 0.30  # 30% regression


def main():
    if len(sys.argv) < 3:
        print(
            f"Usage: {sys.argv[0]} <base.txt> <pr.txt> [threshold]",
            file=sys.stderr,
        )
        sys.exit(1)

    base_file, pr_file = sys.argv[1:3]
    threshold = float(sys.argv[3]) if len(sys.argv) > 3 else THRESHOLD

    benchstat = shutil.which("benchstat")
    if benchstat is None:
        gopath = subprocess.run(
            ["go", "env", "GOPATH"],
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
        benchstat = os.path.join(gopath, "bin", "benchstat")
    result = subprocess.run(
        [benchstat, base_file, pr_file],
        capture_output=True,
        text=True,
    )

    print(result.stdout)
    if result.returncode != 0:
        print(result.stderr, file=sys.stderr)
        sys.exit(result.returncode)

    regressions = []
    for line in result.stdout.splitlines():
        # Match delta like +22.0% before the p-value annotation.
        match = re.search(r"([+-]\d+\.?\d*)%\s+\(p=", line)
        if match:
            delta = float(match.group(1))
            if delta > 0 and delta / 100 > threshold:
                regressions.append((line.strip(), delta))

    if regressions:
        print(
            f"\nERROR: {len(regressions)} benchmark(s) regressed > {threshold*100:.0f}%:",
            file=sys.stderr,
        )
        for line, delta in regressions:
            print(f"  {delta:+.1f}%  {line}", file=sys.stderr)
        sys.exit(1)

    print(f"\nOK: No benchmark regressed > {threshold*100:.0f}%.")


if __name__ == "__main__":
    main()
