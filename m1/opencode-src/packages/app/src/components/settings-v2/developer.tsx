// components/settings-v2/developer.tsx
// Developer tab: user-created API tokens (multi, create/revoke) + how to call
// the API. Tokens bill against the same per-user budget as everything else.
import { Component, createSignal, onMount, For, Show } from "solid-js"
import { SettingsListV2 } from "./parts/list"
import { SettingsRowV2 } from "./parts/row"
import "./settings-v2.css"

type Tok = { id: number; name: string; masked: string; created: string; last_used: string }

const inputCls =
  "rounded-md bg-v2-background-bg-base border border-v2-border-border-muted px-2.5 py-1.5 text-[13px] text-v2-text-text-base focus:outline-none focus:border-[#3fb950]"
const btnPrimary =
  "rounded-md bg-[#3fb950] px-3 py-1.5 text-[13px] font-medium text-black hover:opacity-90 disabled:opacity-40"
const btnGhost =
  "rounded-md border border-v2-border-border-muted px-3 py-1.5 text-[13px] text-v2-text-text-base hover:bg-v2-overlay-simple-overlay-hover"
const btnDanger =
  "rounded-md bg-[#da3633] px-3 py-1.5 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
const sectionTitle = "px-1 pb-2 pt-4 text-[11px] font-medium uppercase tracking-wide text-v2-text-text-muted"

const API_BASE = "https://grams.chat/v1/chat/completions"

function fmtDate(s: string) {
  if (!s) return "never"
  const d = new Date(s.replace(" ", "T"))
  return isNaN(d.getTime()) ? s : d.toLocaleDateString()
}

export const SettingsDeveloperV2: Component = () => {
  const [tokens, setTokens] = createSignal<Tok[]>([])
  const [name, setName] = createSignal("")
  const [busy, setBusy] = createSignal(false)
  const [msg, setMsg] = createSignal("")
  const [fresh, setFresh] = createSignal("") // full token shown once after create

  async function load() {
    try {
      const r = await fetch("/account/tokens", { credentials: "include" })
      const j = await r.json().catch(() => ({}))
      if (r.ok) setTokens(Array.isArray(j.tokens) ? j.tokens : [])
    } catch {}
  }
  onMount(load)

  async function create() {
    setMsg("")
    setFresh("")
    setBusy(true)
    try {
      const r = await fetch("/account/tokens", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name().trim() }),
      })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) {
        setMsg(j.error || "Could not create token.")
        return
      }
      setFresh(j.token) // shown once
      setName("")
      load()
    } catch {
      setMsg("Network error.")
    } finally {
      setBusy(false)
    }
  }

  async function revoke(id: number) {
    try {
      const r = await fetch("/account/tokens/revoke", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id }),
      })
      if (r.ok) load()
    } catch {}
  }

  const curlExample = () =>
    `curl ${API_BASE} \\
  -H "Authorization: Bearer YOUR_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"general","messages":[{"role":"user","content":"Hello"}]}'`

  return (
    <div class="flex flex-col gap-1 overflow-y-auto">
      {/* ---- API tokens ---- */}
      <div class={sectionTitle}>API tokens</div>
      <SettingsListV2>
        <SettingsRowV2
          title="Create a token"
          description="Use it to call the grams API from your own code. It spends your plan's budget and limits, just like the app."
        >
          <div class="flex items-center gap-2">
            <input
              class={inputCls}
              placeholder="name (e.g. my-script)"
              value={name()}
              onInput={(e) => setName(e.currentTarget.value)}
            />
            <button class={btnPrimary} disabled={busy()} onClick={create}>
              {busy() ? "Creating…" : "Create"}
            </button>
          </div>
        </SettingsRowV2>

        <Show when={msg()}>
          <div class="px-1 pt-1 text-[12px] text-[#da3633]">{msg()}</div>
        </Show>

        {/* full token shown ONCE */}
        <Show when={fresh()}>
          <div class="mx-1 mt-1 flex flex-col gap-2 rounded-md border border-[#3fb950]/40 bg-[#3fb950]/[0.06] p-3">
            <div class="text-[13px] font-medium text-v2-text-text-base">Copy your token now</div>
            <div class="text-[12px] text-v2-text-text-muted">It won't be shown again.</div>
            <div class="flex items-center gap-2">
              <code class="flex-1 break-all rounded bg-v2-background-bg-base px-2 py-1 text-[12px]">{fresh()}</code>
              <button class={btnGhost} onClick={() => navigator.clipboard?.writeText(fresh())}>Copy</button>
            </div>
          </div>
        </Show>

        {/* existing tokens */}
        <For each={tokens()}>
          {(t) => (
            <SettingsRowV2
              title={t.name || "API token"}
              description={`${t.masked} · created ${fmtDate(t.created)} · last used ${fmtDate(t.last_used)}`}
            >
              <button class={btnDanger} onClick={() => revoke(t.id)}>Revoke</button>
            </SettingsRowV2>
          )}
        </For>
        <Show when={tokens().length === 0 && !fresh()}>
          <div class="px-1 py-2 text-[13px] text-v2-text-text-muted">No API tokens yet.</div>
        </Show>
      </SettingsListV2>

      {/* ---- Using the API ---- */}
      <div class={sectionTitle}>Using the API</div>
      <SettingsListV2>
        <SettingsRowV2 title="Endpoint" description="OpenAI-compatible chat completions.">
          <div class="flex items-center gap-2">
            <code class="rounded bg-v2-background-bg-base px-2 py-1 text-[12px]">{API_BASE}</code>
            <button class={btnGhost} onClick={() => navigator.clipboard?.writeText(API_BASE)}>Copy</button>
          </div>
        </SettingsRowV2>
        <div class="mx-1 mt-1 rounded-md border border-v2-border-border-muted p-3">
          <div class="mb-2 text-[12px] text-v2-text-text-muted">Example request</div>
          <pre class="overflow-x-auto whitespace-pre rounded bg-v2-background-bg-base p-2 text-[12px] leading-relaxed text-v2-text-text-base">{curlExample()}</pre>
          <button class={`${btnGhost} mt-2`} onClick={() => navigator.clipboard?.writeText(curlExample())}>Copy example</button>
        </div>
      </SettingsListV2>
    </div>
  )
}
