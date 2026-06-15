// pages/layout/plan-banner.tsx
// Site-wide plan banner. Same logic as the Billing tab warning: within 7 days of
// expiry -> solid yellow "renews in N days" (dismissible per session); expired
// and within the 7-day grace -> solid red "expired, N days until data removed"
// (NOT dismissible). Hidden for unmetered/active accounts.
import { createSignal, onMount, Show } from "solid-js"

export function PlanBanner() {
  const [info, setInfo] = createSignal<any | null>(null)
  const [dismissed, setDismissed] = createSignal(false)

  onMount(async () => {
    try {
      const r = await fetch("/account/info", { credentials: "same-origin" })
      if (r.ok) setInfo(await r.json())
    } catch {}
  })

  // identical state machine to billing.tsx subState()
  const state = () => {
    const i = info()
    if (!i || i.unmetered || !i.paid_until) return { key: "active", days: 0 }
    const iso = String(i.paid_until)
      .replace(" ", "T").replace(/\.\d+/, "")
      .replace(/\+00$/, "+00:00").replace(/([+-]\d\d)$/, "$1:00")
    const end = new Date(iso).getTime()
    if (isNaN(end)) return { key: "active", days: 0 }
    const dayMs = 86400000
    const daysLeft = Math.ceil((end - Date.now()) / dayMs)
    if (daysLeft > 7) return { key: "active", days: daysLeft }
    if (daysLeft > 0) return { key: "expiring", days: daysLeft }
    const graceLeft = 7 + daysLeft
    if (graceLeft > 2) return { key: "grace", days: graceLeft }
    if (graceLeft > 0) return { key: "prepurge", days: graceLeft }
    return { key: "lapsed", days: 0 }
  }

  const show = () => {
    const k = state().key
    if (k === "active") return false
    if (k === "expiring" && dismissed()) return false
    return true
  }
  const isWarn = () => state().key === "expiring" // yellow vs red

  return (
    <Show when={show()}>
      <div
        class="relative flex w-full shrink-0 items-center justify-center px-4 py-2.5 text-[13px] font-bold"
        style={{
          background: isWarn() ? "#e3a008" : "#da3633",
          color: isWarn() ? "#000000" : "#ffffff",
        }}
      >
        <div class="flex items-center gap-3">
          <span style={{ color: isWarn() ? "#000000" : "#ffffff" }}>
            <Show when={state().key === "expiring"}>Your plan renews in {state().days} day(s). Renew to avoid interruption.</Show>
            <Show when={state().key === "grace"}>Your plan has expired. {state().days} day(s) left before your workspace data is removed.</Show>
            <Show when={state().key === "prepurge"}>Final warning: {state().days} day(s) until your workspace data is permanently deleted.</Show>
            <Show when={state().key === "lapsed"}>Your plan has lapsed.</Show>
          </span>
          <button
            type="button"
            onClick={() => { window.location.href = "/subscribe" }}
            class="rounded-md px-2.5 py-1 text-[12px] font-bold"
            style={{
              background: isWarn() ? "rgba(0,0,0,0.22)" : "rgba(255,255,255,0.22)",
              color: isWarn() ? "#000000" : "#ffffff",
            }}
          >
            Renew
          </button>
        </div>
        <Show when={state().key === "expiring"}>
          <button
            type="button"
            onClick={() => setDismissed(true)}
            class="absolute right-4 text-[18px] leading-none opacity-80 hover:opacity-100"
            style={{ color: "#000000" }}
            aria-label="Dismiss"
          >
            ×
          </button>
        </Show>
      </div>
    </Show>
  )
}
