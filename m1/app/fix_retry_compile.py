#!/usr/bin/env python3
"""
Fix compile error: 'err redeclared in this block'. The retry block did
  var resp *http.Response
  var err error
but err already exists earlier in the handler (from lookupUserByToken). Declare
only resp with var, and declare err alongside it WITHOUT redeclaring — use a
single var block that doesn't conflict. Simplest: declare resp via var, and
reuse the outer err by assigning (not :=). We change 'var err error' to nothing
and ensure the loop uses '=' (it already does: 'resp, err = http.DefaultClient.Do').

Idempotent.
"""
import sys
PATH = "/opt/gramsai/app/internal/proxy/llm.go"
src = open(PATH).read()

old = '\t\tvar resp *http.Response\n\t\tvar err error\n\t\tconst maxAttempts = 3'
new = '\t\tvar resp *http.Response\n\t\tconst maxAttempts = 3'

if new in src and 'var err error' not in src.split('GRAMSAI_UPSTREAM_RETRY')[1][:200]:
    print("already fixed — no change")
    sys.exit(0)

if old not in src:
    print("FAIL: retry var block not found (expected 'var resp' + 'var err error')")
    sys.exit(1)

src = src.replace(old, new, 1)
open(PATH, "w").write(src)
print("fixed: removed redundant 'var err error' (reuses existing err)")
