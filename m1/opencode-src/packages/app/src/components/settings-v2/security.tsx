// components/settings-v2/security.tsx
// Privacy & Security tab (Step 1): encryption-at-rest info + memory management.
// Uses the same Tailwind utility conventions as account.tsx (v2 theme tokens).
// - Encryption status + "How it works" popup (at-rest + session/logout guidance)
// - Memory on/off toggle (/account/memory/toggle, /account/memory/status)
// - Unified facts + procedures list (view/add/edit/delete). Episodes hidden.
// Cookie-authed; talks to internal/memory/crud_handler.go.
import { Component, createSignal, onMount, For, Show } from "solid-js"
import "./settings-v2.css"

const inputCls =
  "w-full rounded-md border border-v2-border-border-muted bg-v2-background-bg-subtle px-3 py-2 text-[13px] text-v2-text-text-base placeholder:text-v2-text-text-muted focus:outline-none focus:border-v2-border-border-base"
const btnPrimary =
  "rounded-md bg-[#3fb950] px-3 py-1.5 text-[13px] font-medium text-black hover:opacity-90 disabled:opacity-40"
const btnGhost =
  "rounded-md border border-v2-border-border-muted px-3 py-1.5 text-[13px] text-v2-text-text-base hover:bg-v2-overlay-simple-overlay-hover"
const btnDanger =
  "rounded-md bg-[#da3633] px-3 py-1.5 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
const sectionTitle = "px-1 pb-2 pt-4 text-[11px] font-medium uppercase tracking-wide text-v2-text-text-muted"
const chipCls = "rounded-md border px-2.5 py-1 text-[12px] cursor-pointer"

type MemItem = {
  id: number
  type: string
  category: string
  content: string
  confidence: number
  created_at: string
}

const TYPE_LABEL: Record<string, string> = { fact: "Fact", procedure: "Procedure" }

export const SettingsSecurityV2: Component = () => {
  const [enabled, setEnabled] = createSignal(true)
  const [items, setItems] = createSignal<MemItem[]>([])
  const [loading, setLoading] = createSignal(true)
  const [status, setStatus] = createSignal("")
  const [filter, setFilter] = createSignal<"all" | "fact" | "procedure">("all")
  const [showEnc, setShowEnc] = createSignal(false)
  const [exporting, setExporting] = createSignal(false)
  const [showDelAll, setShowDelAll] = createSignal(false)
  const [delBusy, setDelBusy] = createSignal(false)
  const [editing, setEditing] = createSignal<MemItem | null>(null)
  const [draftType, setDraftType] = createSignal<"fact" | "procedure">("fact")
  const [draftContent, setDraftContent] = createSignal("")
  const [showForm, setShowForm] = createSignal(false)

  async function loadStatus() {
    try {
      const r = await fetch("/account/memory/status", { credentials: "include" })
      if (r.ok) { const j = await r.json(); setEnabled(!!j.enabled) }
    } catch {}
  }
  async function loadItems() {
    setLoading(true)
    try {
      const r = await fetch("/account/memory", { credentials: "include" })
      if (r.ok) { const j = await r.json(); setItems(j.items || []) }
    } catch { setStatus("Failed to load memory") }
    setLoading(false)
  }
  onMount(() => { loadStatus(); loadItems() })

  async function toggleMemory() {
    const next = !enabled()
    setEnabled(next)
    try {
      const r = await fetch("/account/memory/toggle", {
        method: "POST", credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: next }),
      })
      if (!r.ok) { setEnabled(!next); setStatus("Failed to update") }
      else { setStatus(next ? "Memory enabled" : "Memory disabled"); setTimeout(() => setStatus(""), 2000) }
    } catch { setEnabled(!next) }
  }

  function openAdd() { setEditing(null); setDraftType("fact"); setDraftContent(""); setShowForm(true) }
  function openEdit(it: MemItem) {
    setEditing(it); setDraftType(it.type === "procedure" ? "procedure" : "fact")
    setDraftContent(it.content); setShowForm(true)
  }

  async function saveDraft() {
    const content = draftContent().trim()
    if (!content) return
    const body = JSON.stringify({ type: draftType(), category: "general", content })
    try {
      let r: Response
      if (editing()) {
        r = await fetch(`/account/memory/${editing()!.id}`, {
          method: "PATCH", credentials: "include",
          headers: { "Content-Type": "application/json" }, body,
        })
      } else {
        r = await fetch("/account/memory", {
          method: "POST", credentials: "include",
          headers: { "Content-Type": "application/json" }, body,
        })
      }
      if (r.ok) { setShowForm(false); await loadItems() }
      else { const j = await r.json().catch(() => ({})); setStatus(j.error || "Save failed") }
    } catch { setStatus("Save failed") }
  }

  async function deleteItem(it: MemItem) {
    if (!confirm("Delete this item? This cannot be undone.")) return
    try {
      const r = await fetch(`/account/memory/${it.id}?type=${it.type}`, { method: "DELETE", credentials: "include" })
      if (r.ok) await loadItems(); else setStatus("Delete failed")
    } catch { setStatus("Delete failed") }
  }

  async function exportData() {
    setExporting(true)
    try {
      const r = await fetch("/account/export", { credentials: "include" })
      if (!r.ok) { setStatus("Export failed"); setExporting(false); return }
      const blob = await r.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      a.download = "gramsai-export.json"
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch { setStatus("Export failed") }
    setExporting(false)
  }

  async function deleteAllChats() {
    setDelBusy(true)
    try {
      const r = await fetch("/account/chats/delete-all", { method: "POST", credentials: "include" })
      const j = await r.json().catch(() => ({}))
      if (r.ok && j.ok) {
        setShowDelAll(false)
        setStatus("All chats deleted")
        await loadItems() // episodes gone; facts/procedures remain
        setTimeout(() => setStatus(""), 3000)
      } else {
        setStatus(j.error || "Delete failed")
      }
    } catch { setStatus("Delete failed") }
    setDelBusy(false)
  }

  const visible = () => { const f = filter(); return items().filter((it) => f === "all" || it.type === f) }

  return (
    <div class="flex flex-col gap-1 overflow-y-auto">
      {/* Encryption status */}
      <div class={sectionTitle}>Encryption</div>
      <div class="mx-1 mb-2 flex items-center justify-between gap-3 rounded-lg border border-v2-border-border-muted bg-v2-background-bg-subtle p-3">
        <div class="min-w-0">
          <div class="text-[14px] font-medium text-v2-text-text-base">Encryption at rest</div>
          <div class="text-[12px] text-v2-text-text-muted">Your conversations and memory are encrypted on disk with a key tied to your session. Logged out = sealed.</div>
        </div>
        <button class={btnGhost} style={{ "white-space": "nowrap" }} onClick={() => setShowEnc(true)}>How it works</button>
      </div>

      {/* Memory toggle */}
      <div class={sectionTitle}>Memory</div>
      <div class="mx-1 mb-2 flex items-center justify-between gap-3 rounded-lg border border-v2-border-border-muted bg-v2-background-bg-subtle p-3">
        <div class="min-w-0">
          <div class="text-[14px] font-medium text-v2-text-text-base">Remember across chats</div>
          <div class="text-[12px] text-v2-text-text-muted">When on, the assistant remembers facts and technical context to personalize responses.</div>
        </div>
        <button
          class={"rounded-md px-3 py-1.5 text-[13px] font-medium " + (enabled() ? "bg-[#3fb950] text-black" : "border border-v2-border-border-muted text-v2-text-text-base")}
          onClick={toggleMemory}
        >
          {enabled() ? "On" : "Off"}
        </button>
      </div>

      {/* Memory list */}
      <div class="mx-1 mb-1 flex items-center justify-between">
        <div class="flex gap-1.5">
          <For each={["all", "fact", "procedure"] as const}>
            {(f) => (
              <button
                class={chipCls + " " + (filter() === f ? "border-[#3fb950] text-[#3fb950]" : "border-v2-border-border-muted text-v2-text-text-muted")}
                onClick={() => setFilter(f)}
              >
                {f === "all" ? "All" : TYPE_LABEL[f] + "s"}
              </button>
            )}
          </For>
        </div>
        <button class={btnGhost} onClick={openAdd}>+ Add</button>
      </div>

      <Show when={!loading()} fallback={<div class="px-1 py-3 text-[13px] text-v2-text-text-muted">Loading…</div>}>
        <Show
          when={visible().length > 0}
          fallback={<div class="mx-1 rounded-lg border border-v2-border-border-muted bg-v2-background-bg-subtle px-3 py-4 text-center text-[13px] text-v2-text-text-muted">Nothing stored yet. The assistant will learn as you chat, or add items manually.</div>}
        >
          <div class="mx-1 flex flex-col gap-1.5">
            <For each={visible()}>
              {(it) => (
                <div class="flex items-start gap-2.5 rounded-lg border border-v2-border-border-muted bg-v2-background-bg-subtle px-3 py-2.5">
                  <span class={"mt-0.5 shrink-0 rounded border px-1.5 py-0.5 text-[10px] uppercase tracking-wide " + (it.type === "procedure" ? "border-[#7fb3ff] text-[#7fb3ff]" : "border-[#8fd6a0] text-[#8fd6a0]")}>
                    {TYPE_LABEL[it.type] || it.type}
                  </span>
                  <div class="flex-1 break-words text-[13px] leading-relaxed text-v2-text-text-base">{it.content}</div>
                  <div class="flex shrink-0 gap-1">
                    <button class="rounded px-2 py-1 text-[12px] text-v2-text-text-muted hover:bg-v2-overlay-simple-overlay-hover" onClick={() => openEdit(it)}>Edit</button>
                    <button class="rounded px-2 py-1 text-[12px] text-[#da3633] hover:bg-v2-overlay-simple-overlay-hover" onClick={() => deleteItem(it)}>Delete</button>
                  </div>
                </div>
              )}
            </For>
          </div>
        </Show>
      </Show>

      <Show when={status()}>
        <div class="mx-1 mt-2 text-[12px] text-v2-text-text-muted">{status()}</div>
      </Show>

      {/* Add/Edit modal */}
      <Show when={showForm()}>
        <div onClick={() => setShowForm(false)} style={{ position: "fixed", inset: "0", "z-index": "320", background: "rgba(0,0,0,.6)", display: "flex", "align-items": "center", "justify-content": "center", padding: "24px" }}>
          <div onClick={(e) => e.stopPropagation()} style={{ background: "#16181d", border: "1px solid #2a2f37", "border-radius": "14px", "max-width": "520px", width: "100%" }}>
            <div style={{ display: "flex", "align-items": "center", "justify-content": "space-between", padding: "16px 20px", "border-bottom": "1px solid #2a2f37" }}>
              <div style={{ "font-weight": "600", "font-size": "15px", color: "#e8eaed" }}>{editing() ? "Edit item" : "Add to memory"}</div>
              <button onClick={() => setShowForm(false)} style={{ background: "transparent", border: "0", color: "#8b939c", "font-size": "13px", cursor: "pointer" }}>Close</button>
            </div>
            <div style={{ padding: "18px 20px", display: "flex", "flex-direction": "column", gap: "14px" }}>
              <div class="flex gap-2">
                <For each={["fact", "procedure"] as const}>
                  {(t) => (
                    <button class={chipCls + " " + (draftType() === t ? "border-[#3fb950] text-[#3fb950]" : "border-v2-border-border-muted text-v2-text-text-muted")} onClick={() => setDraftType(t)}>
                      {TYPE_LABEL[t]}
                    </button>
                  )}
                </For>
              </div>
              <textarea class={inputCls} value={draftContent()} onInput={(e) => setDraftContent(e.currentTarget.value)} rows={4}
                placeholder={draftType() === "fact" ? "e.g. I prefer concise answers." : "e.g. My project builds with: bun run build, then ship to server."}
                style={{ resize: "vertical", "font-family": "inherit" }} />
              <div class="flex justify-end gap-2">
                <button class={btnGhost} onClick={() => setShowForm(false)}>Cancel</button>
                <button class={btnPrimary} onClick={saveDraft}>{editing() ? "Save" : "Add"}</button>
              </div>
            </div>
          </div>
        </div>
      </Show>

      {/* Data section */}
      <div class={sectionTitle}>Your data</div>
      <div class="mx-1 mb-2 flex items-center justify-between gap-3 rounded-lg border border-v2-border-border-muted bg-v2-background-bg-subtle p-3">
        <div class="min-w-0">
          <div class="text-[14px] font-medium text-v2-text-text-base">Export my data</div>
          <div class="text-[12px] text-v2-text-text-muted">Download your memory, account info, and chat history as a JSON file.</div>
        </div>
        <button class={btnGhost} disabled={exporting()} onClick={exportData} style={{ "white-space": "nowrap" }}>
          {exporting() ? "Exporting…" : "Export"}
        </button>
      </div>
      <div class="mx-1 mb-2 flex items-center justify-between gap-3 rounded-lg border border-v2-border-border-muted bg-v2-background-bg-subtle p-3">
        <div class="min-w-0">
          <div class="text-[14px] font-medium text-v2-text-text-base">Delete all chats</div>
          <div class="text-[12px] text-v2-text-text-muted">Permanently delete all conversations and their summaries. Your saved facts and procedures are kept.</div>
        </div>
        <button class={btnDanger} onClick={() => setShowDelAll(true)} style={{ "white-space": "nowrap" }}>
          Delete all
        </button>
      </div>

      {/* Delete-all confirm modal */}
      <Show when={showDelAll()}>
        <div onClick={() => !delBusy() && setShowDelAll(false)} style={{ position: "fixed", inset: "0", "z-index": "320", background: "rgba(0,0,0,.6)", display: "flex", "align-items": "center", "justify-content": "center", padding: "24px" }}>
          <div onClick={(e) => e.stopPropagation()} style={{ background: "#16181d", border: "1px solid #2a2f37", "border-radius": "14px", "max-width": "460px", width: "100%" }}>
            <div style={{ padding: "20px 22px" }}>
              <div style={{ "font-weight": "600", "font-size": "16px", color: "#e8eaed", "margin-bottom": "10px" }}>Delete all chats?</div>
              <div style={{ "font-size": "13px", color: "#c8ccd1", "line-height": "1.55", "margin-bottom": "18px" }}>
                This permanently deletes all your conversations and their summaries. This cannot be undone. Your saved facts and procedures will be kept.
              </div>
              <div class="flex justify-end gap-2">
                <button class={btnGhost} disabled={delBusy()} onClick={() => setShowDelAll(false)}>Cancel</button>
                <button class={btnDanger} disabled={delBusy()} onClick={deleteAllChats}>{delBusy() ? "Deleting…" : "Delete all chats"}</button>
              </div>
            </div>
          </div>
        </div>
      </Show>

      {/* Encryption explainer popup */}
      <Show when={showEnc()}>
        <div onClick={() => setShowEnc(false)} style={{ position: "fixed", inset: "0", "z-index": "320", background: "rgba(0,0,0,.6)", display: "flex", "align-items": "center", "justify-content": "center", padding: "24px" }}>
          <div onClick={(e) => e.stopPropagation()} style={{ background: "#16181d", border: "1px solid #2a2f37", "border-radius": "14px", "max-width": "640px", width: "100%", "max-height": "82vh", display: "flex", "flex-direction": "column" }}>
            <div style={{ display: "flex", "align-items": "center", "justify-content": "space-between", padding: "16px 20px", "border-bottom": "1px solid #2a2f37" }}>
              <div style={{ "font-weight": "600", "font-size": "16px", color: "#e8eaed" }}>How your data is protected</div>
              <button onClick={() => setShowEnc(false)} style={{ background: "transparent", border: "0", color: "#8b939c", "font-size": "13px", cursor: "pointer" }}>Close</button>
            </div>
            <div style={{ overflow: "auto", padding: "18px 22px", "font-size": "13.5px", color: "#c8ccd1", "line-height": "1.6" }}>
              <p style={{ "margin-top": "0" }}>Your conversations and memory are <strong style={{ color: "#e8eaed" }}>encrypted at rest</strong> using AES-256. The key is derived from your account at login and held only for your active session.</p>
              <p><strong style={{ color: "#e8eaed" }}>What this means:</strong></p>
              <ul style={{ "padding-left": "18px", margin: "0 0 14px" }}>
                <li style={{ "margin-bottom": "6px" }}>While logged in, your session reads and writes your data normally.</li>
                <li style={{ "margin-bottom": "6px" }}>When you log out, the key is discarded and stored data becomes unreadable ciphertext on disk.</li>
                <li style={{ "margin-bottom": "6px" }}>The operator cannot read your stored conversations or memory while you are logged out.</li>
              </ul>
              <p><strong style={{ color: "#e8eaed" }}>Your responsibility:</strong></p>
              <ul style={{ "padding-left": "18px", margin: "0 0 14px" }}>
                <li style={{ "margin-bottom": "6px" }}>To keep data sealed, <strong style={{ color: "#e8eaed" }}>log out when done</strong> — especially on shared or untrusted devices.</li>
                <li style={{ "margin-bottom": "6px" }}>Review active sessions in the Account tab and revoke any you don't recognize. An open session keeps your data unsealed.</li>
              </ul>
              <p style={{ "margin-bottom": "0", "font-size": "12.5px", color: "#8b939c" }}>Note: while a session is active, content is decrypted in memory so the assistant can process it. Encryption at rest protects data on disk, not while actively in use.</p>
            </div>
          </div>
        </div>
      </Show>
    </div>
  )
}
