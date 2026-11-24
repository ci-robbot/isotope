#!/usr/bin/env bash
# check-desktop-images-paths.sh
#
# desktop-images.yml picks the desktops to build from a pull request's files
# (the `case` in its plan job), but GitHub starts the workflow only when a file
# matches its pull_request `paths`. A rule the paths do not cover never runs:
# #181 changed only test/accessibility/, and no image job ran. This fails when a
# plan rule has no matching pull_request path.
set -euo pipefail
cd "$(dirname "$0")/../.."
python3 - .github/workflows/desktop-images.yml <<'PY'
import re, sys
s = open(sys.argv[1]).read()
pr = s[s.index("  pull_request:"):s.index("  push:")]
paths = re.findall(r'^      - "([^"]+)"', pr, re.M)
m = re.search(r'case "\$f" in\n(.*?)\n\s+for d in', s, re.S)
if not paths or not m:
    sys.exit("check-desktop-images-paths: cannot find the paths or the plan's case")
rules = [r for r in re.split(r"[|\\\s)]+", m.group(1)) if r]
def covered(rule):
    f = rule[:-1] + "x" if rule.endswith("*") else rule
    return any(f == p or (p.endswith("/**") and f.startswith(p[:-2])) for p in paths)
missing = [r for r in rules if not covered(r)]
if missing:
    print("desktop-images.yml: plan rules with no pull_request path (no image job would run):", file=sys.stderr)
    for r in missing: print("  " + r, file=sys.stderr)
    sys.exit(1)
print(f"PASS: all {len(rules)} plan rules of desktop-images.yml have a pull_request path")
PY
