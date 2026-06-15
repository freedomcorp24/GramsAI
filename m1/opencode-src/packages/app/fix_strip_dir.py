#!/usr/bin/env python3
# Run on M1:  python3 fix_strip_dir.py
# The files strip was pointed at params.dir (=/workspace), but sessions run in a
# worktree the URL doesn't reflect. Derive the real folder from the session's
# write/edit tool parts (they record the absolute path the AI wrote to), falling
# back to params.dir. Anchored, idempotent.
SESS = "/opt/gramsai/opencode-src/packages/app/src/pages/session.tsx"
s = open(SESS).read()

if "const chatDir = createMemo" in s:
    print(".. already fixed, nothing to do"); print("DONE"); raise SystemExit

# 1) add the chatDir memo right after the messages memo (so messages() is in scope)
m_old = '  const messages = createMemo(() => (params.id ? (sync.data.message[params.id] ?? []) : []))'
assert s.count(m_old) == 1, f"ABORT messages anchor {s.count(m_old)}x"
m_new = m_old + '''
  const chatDir = createMemo(() => {
    const msgs = messages()
    for (let i = msgs.length - 1; i >= 0; i--) {
      const parts = (sync.data.part[msgs[i].id] ?? []) as any[]
      for (const p of parts) {
        if (p?.type !== "tool") continue
        const fp = p?.state?.input?.filePath ?? p?.state?.input?.path
        if (typeof fp !== "string" || fp[0] !== "/") continue
        const wt = fp.match(/^(.*\\/worktree\\/[^/]+\\/[^/]+)(?:\\/|$)/)
        return wt ? wt[1] : fp.slice(0, fp.lastIndexOf("/"))
      }
    }
    return decode64(params.dir) ?? ""
  })'''
s = s.replace(m_old, m_new)

# 2) point the strip at chatDir instead of params.dir
d_old = '              directory={() => decode64(params.dir) ?? ""}'
d_new = '              directory={chatDir}'
assert s.count(d_old) == 1, f"ABORT directory-prop anchor {s.count(d_old)}x"
s = s.replace(d_old, d_new)

open(SESS, "w").write(s)
print("OK  strip now uses the session's real worktree (from write/edit parts)")
print("DONE  -> rebuild frontend")
