#!/usr/bin/env python3
"""
Stage 4 — session.tsx: auto-switch to the Browser tab when the agent fires a
web search (tool name "search_web_search"). Works for desktop AND mobile.

Mechanism: a createEffect watches the reactive tool parts of the current
session's messages. When it sees a NEW search tool part (tracked by part id so
it fires once per search, not every render), it:
  - desktop: opens the side panel (view().reviewPanel.open()) and setActive("browser")
  - mobile:  setStore("mobileTab", "browser")

Inserted right after the messages() memo (line ~340) where messages + sync are
in scope. isDesktop(), view(), tabs(), setStore are all already in scope.

Idempotent: guarded by marker GRAMSAI_BROWSER_AUTOOPEN.
"""
import sys

PATH = "/opt/gramsai/opencode-src/packages/app/src/pages/session.tsx"
MARKER = "GRAMSAI_BROWSER_AUTOOPEN"

src = open(PATH).read()
if MARKER in src:
    print("session.tsx (auto-open): already patched — no change")
    sys.exit(0)

# Anchor: the messages() memo line. We insert our effect immediately after it.
anchor = '  const messages = createMemo(() => (params.id ? (sync.data.message[params.id] ?? []) : []))'

block = anchor + """

  // GRAMSAI_BROWSER_AUTOOPEN: when the agent fires a web search, jump to the
  // Browser tab automatically (desktop opens the side panel; mobile switches
  // mobileTab). Tracks the last-seen search part id so it fires once per search.
  let lastSearchPartID: string | undefined
  createEffect(() => {
    const msgs = messages()
    // find the most recent search_web_search tool part
    let latest: string | undefined
    for (let i = msgs.length - 1; i >= 0 && !latest; i--) {
      const parts = (sync.data.part[msgs[i].id] ?? []) as any[]
      for (let j = parts.length - 1; j >= 0; j--) {
        const p = parts[j]
        if (p?.type === "tool" && p?.tool === "search_web_search") {
          latest = p.id
          break
        }
      }
    }
    if (!latest) return
    if (latest === lastSearchPartID) return
    lastSearchPartID = latest
    // switch to the browser tab
    if (isDesktop()) {
      if (!view().reviewPanel.opened()) view().reviewPanel.open()
      tabs().setActive("browser")
    } else {
      setStore("mobileTab", "browser")
    }
  })"""

if anchor not in src:
    print("FAIL: messages() memo anchor not found")
    sys.exit(1)
src = src.replace(anchor, block, 1)

open(PATH, "w").write(src)
print("session.tsx (auto-open): patched OK (search -> browser tab, desktop+mobile)")
