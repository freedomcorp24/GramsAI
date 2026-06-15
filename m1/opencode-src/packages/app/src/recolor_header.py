#!/usr/bin/env python3
# Run on M1:  python3 recolor_header.py
# Top bar brand "ams" is text-v2-text-text-muted (dim) — brighten to match the
# sidebar (text-v2-text-text-base). Also brighten the header search placeholder
# from text-text-weak -> text-text-base. Color classes only; layout + wiring
# (download, review toggle, tabs) untouched. Idempotent.
TB = "/opt/gramsai/opencode-src/packages/app/src/components/titlebar.tsx"
SH = "/opt/gramsai/opencode-src/packages/app/src/components/session/session-header.tsx"

# --- titlebar: brand "ams" muted -> base (match sidebar) ---
t = open(TB).read(); tc = 0
a = '<span class="text-v2-text-text-muted">ams</span>'
b = '<span class="text-v2-text-text-base">ams</span>'
if a in t: t = t.replace(a, b); tc += 1
# make "gr" a touch bolder to match sidebar brand weight (optional, safe)
g_old = '<span class="text-[#3fb950]">gr</span>'
g_new = '<span class="text-[#3fb950] font-bold">gr</span>'
if g_old in t and g_new not in t: t = t.replace(g_old, g_new); tc += 1
if tc: open(TB, "w").write(t)
print("titlebar swaps:", tc)

# --- session-header: search text weak -> base, border weak -> base ---
s = open(SH).read(); sc = 0
p_old = 'class="flex-1 min-w-0 text-12-regular text-text-weak truncate text-left"'
p_new = 'class="flex-1 min-w-0 text-12-regular text-text-base truncate text-left"'
if p_old in s: s = s.replace(p_old, p_new); sc += 1
brd_old = 'rounded-md border border-border-weak-base bg-surface-panel shadow-none cursor-default"'
brd_new = 'rounded-md border border-border-base bg-surface-panel shadow-none cursor-default"'
if brd_old in s: s = s.replace(brd_old, brd_new); sc += 1
if sc: open(SH, "w").write(s)
print("session-header swaps:", sc)
print("DONE")
