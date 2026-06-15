#!/usr/bin/env python3
# Run on M1:  python3 fix_strip_v2.py
# Feeds the files strip from the session's changed-files list (diffs()) — the
# same data the working Review panel uses — and adds Download (reads the file
# via the SDK, same resolution as View, then saves it). Anchored, idempotent.
SESS = "/opt/gramsai/opencode-src/packages/app/src/pages/session.tsx"
s = open(SESS).read()

if "onDownloadFile" in s:
    print(".. already on v2, nothing to do"); print("DONE"); raise SystemExit

# 1) add onDownloadFile right after the onViewFile block
a_old = '''    setActive: tabs().setActive,
  })'''
assert s.count(a_old) == 1, f"ABORT onViewFile-end anchor {s.count(a_old)}x"
a_new = a_old + '''
  const onDownloadFile = (p: string) => {
    sdk.client.file
      .read({ path: p })
      .then((x) => {
        const text = (x as any)?.data?.content ?? ""
        const blob = new Blob([text], { type: "application/octet-stream" })
        const url = URL.createObjectURL(blob)
        const a = document.createElement("a")
        a.href = url
        a.download = p.split("/").pop() || "file"
        document.body.appendChild(a)
        a.click()
        a.remove()
        URL.revokeObjectURL(url)
      })
      .catch(() => {})
  }'''
s = s.replace(a_old, a_new)

# 2) repoint the mount: diff list + onView + onDownload
m_old = '''          <Show when={params.id}>
            <ChatFilesStrip
              directory={chatDir}
              refreshKey={() => messages().length}
              onView={onViewFile}
            />
          </Show>'''
assert s.count(m_old) == 1, f"ABORT mount anchor {s.count(m_old)}x"
m_new = '''          <Show when={params.id}>
            <ChatFilesStrip
              files={() => diffs().map((d) => (d as any).file).filter((f: unknown): f is string => typeof f === "string")}
              onView={onViewFile}
              onDownload={onDownloadFile}
            />
          </Show>'''
s = s.replace(m_old, m_new)

open(SESS, "w").write(s)
print("OK  strip now uses diffs() + View/Download via the SDK")
print("DONE  -> rebuild frontend")
