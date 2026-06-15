#!/usr/bin/env python3
# Run on M1:  python3 fix_strip_v3.py
# The strip read session_diff (empty for live sessions). The working Review panel
# reads reviewDiffs() (git/VCS changes). Point the strip at the SAME source so it
# shows the same files; View already opens them in the right panel. Idempotent.
SESS = "/opt/gramsai/opencode-src/packages/app/src/pages/session.tsx"
s = open(SESS).read()

old = 'files={() => diffs().map((d) => (d as any).file).filter((f: unknown): f is string => typeof f === "string")}'
new = 'files={() => reviewDiffs().map((d) => (d as any).file).filter((f: unknown): f is string => typeof f === "string")}'

if new in s:
    print(".. already using reviewDiffs, nothing to do"); print("DONE"); raise SystemExit
assert s.count(old) == 1, f"ABORT anchor {s.count(old)}x (run fix_strip_v2.py first)"
open(SESS, "w").write(s.replace(old, new))
print("OK  strip now reads reviewDiffs() (git changes) — same as the working panel")
print("DONE  -> rebuild frontend")
