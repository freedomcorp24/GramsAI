// components/settings-v2/billing.tsx
// Billing tab: plan + usage/storage meters, change plan (upgrade->pay,
// downgrade->schedule, cancel pending), and top-up. Self-contained: fetches
// /account/info itself. Moved out of the Account tab to declutter it.
import { Component, createSignal, onMount, createEffect, For, Show } from "solid-js"
import qrcode from "../../lib/qrcode"
import { SettingsListV2 } from "./parts/list"
import { SettingsRowV2 } from "./parts/row"
import "./settings-v2.css"

const GiB = 1024 ** 3

type Info = {
  username: string
  email: string
  tier: string
  status: string
  unmetered: boolean
  budget_micros: number
  used_micros: number
  daily_used_micros: number
  daily_limit_micros: number
  paid_until: string
  storage_used_bytes: number
  storage_extra_bytes: number
  storage_quota_bytes: number
  pending_tier: string
  pending_storage_bytes: number
}

const TIERS = [
  { value: "basic", label: "Basic", price: "$19/mo" },
  { value: "pro", label: "Pro", price: "$49/mo" },
  { value: "max", label: "Max", price: "$99/mo" },
  { value: "ultra", label: "Ultra", price: "$199/mo" },
]
const STORAGE = [
  { value: "", label: "1 GB (included)" },
  { value: "s5", label: "5 GB (+$5/mo)" },
  { value: "s25", label: "25 GB (+$15/mo)" },
  { value: "s100", label: "100 GB (+$30/mo)" },
]
const COINS = ["xmr", "btc", "eth", "ltc", "sol", "usdttrc20", "usdterc20"]
// Top-up packs (USD). Charged at 2x cost, added to compute budget, use-it-or-lose-it.
const TOPUPS = [
  { value: "t10", label: "$10 credit" },
  { value: "t25", label: "$25 credit" },
  { value: "t50", label: "$50 credit" },
  { value: "t100", label: "$100 credit" },
]

function tierLabel(t: string) {
  return TIERS.find((x) => x.value === t)?.label ?? t
}
function packForBytes(extra: number) {
  const g = extra / GiB
  if (g >= 100) return "s100"
  if (g >= 25) return "s25"
  if (g >= 5) return "s5"
  return ""
}
function storageLabel(pack: string) {
  return STORAGE.find((x) => x.value === pack)?.label ?? pack
}
function fmtGB(bytes: number) {
  const g = bytes / GiB
  return (Number.isInteger(g) ? g.toFixed(0) : g.toFixed(2)) + " GB"
}
function fmtDate(s: string) {
  if (!s) return "—"
  const d = new Date(s.replace(" ", "T"))
  return isNaN(d.getTime()) ? s : d.toLocaleDateString()
}

const inputCls =
  "rounded-md bg-v2-background-bg-base border border-v2-border-border-muted px-2.5 py-1.5 text-[13px] text-v2-text-text-base focus:outline-none focus:border-[#3fb950]"
const btnPrimary =
  "rounded-md bg-[#3fb950] px-3 py-1.5 text-[13px] font-medium text-black hover:opacity-90 disabled:opacity-40"
const btnGhost =
  "rounded-md border border-v2-border-border-muted px-3 py-1.5 text-[13px] text-v2-text-text-base hover:bg-v2-overlay-simple-overlay-hover"
const sectionTitle = "px-1 pb-2 pt-4 text-[11px] font-medium uppercase tracking-wide text-v2-text-text-muted"

export const SettingsBillingV2: Component = () => {
  const [info, setInfo] = createSignal<Info | null>(null)
  const [infoErr, setInfoErr] = createSignal("")
  // change plan
  const [tier, setTier] = createSignal("basic")
  const [storage, setStorage] = createSignal("")
  const [coin, setCoin] = createSignal(COINS[0])
  const [needCoin, setNeedCoin] = createSignal(false)
  const [planMsg, setPlanMsg] = createSignal("")
  const [payment, setPayment] = createSignal<any | null>(null)
  const [planBusy, setPlanBusy] = createSignal(false)
  // top-up
  const [topAmt, setTopAmt] = createSignal(TOPUPS[0].value)
  const [topCoin, setTopCoin] = createSignal(COINS[0])
  const [topMsg, setTopMsg] = createSignal("")
  const [topPay, setTopPay] = createSignal<any | null>(null)
  const [topBusy, setTopBusy] = createSignal(false)

  // GRAMSAI_TOPUP_QR: draw an address (or coin URI) into a canvas element.
  function drawQR(cv: HTMLCanvasElement | undefined, text: string) {
    if (!cv || !text) return
    try {
      const q = (qrcode as any)(0, "M")
      q.addData(text)
      q.make()
      const n = q.getModuleCount(), cell = 5, margin = 4
      const size = (n + margin * 2) * cell
      cv.width = size; cv.height = size
      const ctx = cv.getContext("2d")
      if (!ctx) return
      ctx.fillStyle = "#ffffff"; ctx.fillRect(0, 0, size, size)
      ctx.fillStyle = "#000000"
      for (let r = 0; r < n; r++)
        for (let c = 0; c < n; c++)
          if (q.isDark(r, c)) ctx.fillRect((c + margin) * cell, (r + margin) * cell, cell, cell)
    } catch (_) {}
  }
  let upgQR: HTMLCanvasElement | undefined
  let topQR: HTMLCanvasElement | undefined
  // GRAMSAI_RENEW: history + renewal
  const [history, setHistory] = createSignal<any[]>([])
  const [renewal, setRenewal] = createSignal<any | null>(null)
  const [renewCoin, setRenewCoin] = createSignal(COINS[0])
  const [renewMsg, setRenewMsg] = createSignal("")
  const [renewBusy, setRenewBusy] = createSignal(false)
  const [renewPay, setRenewPay] = createSignal<any | null>(null)
  let renewQR: HTMLCanvasElement | undefined
  // renewTarget: what "Renew" re-pays. Prefer the last completed subscription;
  // otherwise fall back to the current tier (monthly, no extra storage) so a
  // lapsed/never-completed user can still renew their current plan.
  const renewTarget = () => {
    const rn = renewal()
    if (rn) return rn
    const i = info()
    if (i && i.tier && i.tier !== "basic") return { tier: i.tier, period: "monthly", storage_bytes: i.storage_extra_bytes || 0 }
    if (i && i.tier) return { tier: i.tier, period: "monthly", storage_bytes: 0 }
    return null
  }
  createEffect(() => { const p = renewPay(); if (p) drawQR(renewQR, p.pay_address) })

  async function loadHistory() {
    try {
      const r = await fetch("/account/payments", { credentials: "include" })
      const j = await r.json().catch(() => ({}))
      if (r.ok) {
        setHistory(Array.isArray(j.payments) ? j.payments : [])
        setRenewal(j.renewal || null)
      }
    } catch {}
  }

  // Subscription state from paid_until.
  const subState = () => {
    const i = info()
    if (!i || !i.paid_until) return { key: "none", days: 0 }
    // normalize PG timestamp: "2026-06-15 20:27:12.230396+00" -> ISO the browser parses
    const iso = i.paid_until.replace(" ", "T").replace(/\.\d+/, "").replace(/\+00$/, "+00:00").replace(/([+-]\d\d)$/, "$1:00")
    const end = new Date(iso).getTime()
    const now = Date.now()
    const dayMs = 86400000
    const daysLeft = Math.ceil((end - now) / dayMs)
    if (daysLeft > 7) return { key: "active", days: daysLeft }
    if (daysLeft > 0) return { key: "expiring", days: daysLeft }
    // expired -> grace (7 days)
    const graceLeft = 7 + daysLeft // daysLeft is <=0
    if (graceLeft > 2) return { key: "grace", days: graceLeft }
    if (graceLeft > 0) return { key: "prepurge", days: graceLeft }
    return { key: "lapsed", days: 0 }
  }

  async function quickRenew() {
    const rn = renewTarget()
    if (!rn) { setRenewMsg("No previous plan to renew."); return }
    setRenewMsg(""); setRenewPay(null); setRenewBusy(true)
    try {
      const r = await fetch("/pay/create", {
        method: "POST", credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          kind: "subscription",
          tier: rn.tier,
          period: rn.period,
          storage: packForBytes(rn.storage_bytes || 0),
          coin: renewCoin(),
        }),
      })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) { setRenewMsg(j.error || "Could not create renewal."); return }
      setRenewPay(j)
    } catch {
      setRenewMsg("Network error.")
    } finally { setRenewBusy(false) }
  }

  createEffect(() => { const p = payment(); if (p) drawQR(upgQR, p.pay_address) })
  createEffect(() => { const p = topPay(); if (p) drawQR(topQR, p.pay_address) })

  async function loadInfo() {
    setInfoErr("")
    try {
      const r = await fetch("/account/info", { credentials: "include" })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) {
        setInfoErr(j.error || "Could not load account.")
        return
      }
      setInfo(j)
      setTier(j.tier)
      setStorage(packForBytes(j.storage_extra_bytes || 0))
    } catch {
      setInfoErr("Network error loading account.")
    }
  }

  onMount(() => {
    loadInfo()
    loadHistory()
  })

  const computePct = () => {
    const i = info()
    if (!i || i.budget_micros <= 0) return 0
    return Math.min(100, Math.round((i.used_micros / i.budget_micros) * 100))
  }
  const dailyPct = () => {
    const i = info()
    if (!i || i.daily_limit_micros <= 0) return 0
    return Math.min(100, Math.round((i.daily_used_micros / i.daily_limit_micros) * 100))
  }
  const storagePct = () => {
    const i = info()
    if (!i || i.storage_quota_bytes <= 0) return 0
    return Math.min(100, Math.round((i.storage_used_bytes / i.storage_quota_bytes) * 100))
  }

  async function applyPlan(withCoin: boolean) {
    setPlanBusy(true)
    setPlanMsg("")
    setPayment(null)
    try {
      const body: any = { tier: tier(), storage: storage() }
      if (withCoin) body.coin = coin()
      const r = await fetch("/account/change", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) {
        if (j.error === "coin required") {
          setNeedCoin(true)
          setPlanMsg("This is an upgrade — choose a coin to pay the prorated amount.")
        } else if (j.type === "no_active_sub") {
          setPlanMsg("No active subscription. Subscribe from checkout first.")
        } else {
          setPlanMsg(j.error || "Change failed.")
        }
        return
      }
      if (j.upgrade) {
        setPayment(j)
        setNeedCoin(false)
        setPlanMsg("")
      } else if (j.scheduled) {
        setNeedCoin(false)
        setPlanMsg(`Scheduled to take effect at renewal (${fmtDate(j.effective_at || "")}).`)
        loadInfo()
      } else {
        setNeedCoin(false)
        setPlanMsg(j.note || "Plan updated.")
        loadInfo()
      }
    } catch {
      setPlanMsg("Network error.")
    } finally {
      setPlanBusy(false)
    }
  }

  async function cancelPending() {
    try {
      const r = await fetch("/account/change", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ cancel: true }),
      })
      if (r.ok) {
        setPlanMsg("Pending change cancelled.")
        loadInfo()
      }
    } catch {
      /* ignore */
    }
  }

  async function buyTopup() {
    setTopMsg("")
    setTopPay(null)
    setTopBusy(true)
    try {
      const r = await fetch("/pay/create", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ kind: "topup", pack: topAmt(), coin: topCoin() }),
      })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) {
        setTopMsg(j.error || "Could not create top-up.")
        return
      }
      setTopPay(j)
    } catch {
      setTopMsg("Network error.")
    } finally {
      setTopBusy(false)
    }
  }

  return (
    <div class="flex flex-col gap-1 overflow-y-auto">
      <Show when={infoErr()}>
        <div class="px-1 py-2 text-[13px] text-[#da3633]">{infoErr()}</div>
      </Show>

      {/* GRAMSAI_RENEW: subscription status + quick renew */}
      <Show when={info() && !info()!.unmetered && subState().key !== "active"}>
        {/* BILLING_SOLID_WARN */}
        <div
          class="mx-1 mb-1 mt-1 rounded-lg p-3"
          style={{
            background: subState().key === "expiring" ? "#d29922" : "#da3633",
            color: subState().key === "expiring" ? "#1a1205" : "#ffffff",
          }}
        >
          <div class="text-[13px] font-medium" style={{ color: subState().key === "expiring" ? "#1a1205" : "#ffffff" }}>
            <Show when={subState().key === "expiring"}>Your plan renews in {subState().days} day(s).</Show>
            <Show when={subState().key === "grace"}>Your plan expired. {subState().days} day(s) left in the grace period before your workspace data is removed.</Show>
            <Show when={subState().key === "prepurge"}>Final warning: {subState().days} day(s) until your workspace data is permanently deleted.</Show>
            <Show when={subState().key === "lapsed"}>Your plan has lapsed.</Show>
          </div>
        </div>
      </Show>

      <div class={sectionTitle}>Renew</div>
      <SettingsListV2>
        <SettingsRowV2
          title="Renew your plan"
          description={
            renewTarget()
              ? `Re-pay for ${tierLabel(renewTarget().tier)} · ${renewTarget().period}. Extends your access by another period.`
              : "Subscribe from checkout to start a plan."
          }
        >
          <Show when={renewTarget()} fallback={<span class="text-[12px] text-v2-text-text-muted">—</span>}>
            <div class="flex items-center gap-2">
              <select class={inputCls} value={renewCoin()} onInput={(e) => setRenewCoin(e.currentTarget.value)}>
                <For each={COINS}>{(c) => <option value={c}>{c.toUpperCase()}</option>}</For>
              </select>
              <button class={btnPrimary} disabled={renewBusy()} onClick={quickRenew}>
                {renewBusy() ? "Creating…" : "Renew"}
              </button>
            </div>
          </Show>
        </SettingsRowV2>
        <Show when={renewMsg()}>
          <div class="px-1 pt-1 text-[12px] text-v2-text-text-muted">{renewMsg()}</div>
        </Show>
        <Show when={renewPay()}>
          {(p) => (
            <div class="mx-1 mt-1 rounded-md border border-v2-border-border-muted p-3 text-[13px]">
              <div class="mb-1 font-medium text-v2-text-text-base">
                Send {p().pay_amount} {String(p().pay_currency).toUpperCase()} (≈ ${p().price_usd})
              </div>
              <div class="mb-2 text-[12px] text-v2-text-text-muted">Your access extends automatically once the payment confirms.</div>
              <div class="flex justify-center mb-2">
                <canvas ref={renewQR} class="rounded-md bg-white p-2" style={{ "image-rendering": "pixelated" }} />
              </div>
              <div class="flex items-center gap-2">
                <code class="flex-1 break-all rounded bg-v2-background-bg-base px-2 py-1 text-[12px]">{p().pay_address}</code>
                <button class={btnGhost} onClick={() => navigator.clipboard?.writeText(p().pay_address)}>Copy</button>
              </div>
              <button class={`${btnGhost} mt-2`} onClick={() => { loadInfo(); loadHistory() }}>I&apos;ve paid — refresh</button>
            </div>
          )}
        </Show>
      </SettingsListV2>

      {/* ---- Plan & usage ---- */}
      <div class={sectionTitle}>Plan &amp; usage</div>
      <SettingsListV2>
        <SettingsRowV2
          title="Current plan"
          description={
            <Show when={info()} fallback="Loading…">
              {(i) => (
                <span>
                  {i().unmetered ? "Unmetered" : tierLabel(i().tier)} · {i().status}
                  {i().paid_until ? ` · renews ${fmtDate(i().paid_until)}` : " · no active subscription"}
                </span>
              )}
            </Show>
          }
        >
          <span class="text-[13px] font-medium text-v2-text-text-base">
            {info() ? tierLabel(info()!.tier) : "—"}
          </span>
        </SettingsRowV2>

        <SettingsRowV2
          title="Monthly compute"
          description={info() ? `${computePct()}% used this cycle` : "Loading…"}
        >
          <div class="flex w-40 flex-col gap-1">
            <div class="h-1.5 w-full overflow-hidden rounded-full bg-v2-background-bg-base">
              <div class="h-full rounded-full bg-[#3fb950]" style={{ width: `${computePct()}%` }} />
            </div>
            <span class="text-right text-[11px] text-v2-text-text-muted">{computePct()}%</span>
          </div>
        </SettingsRowV2>

        <SettingsRowV2
          title="Daily compute"
          description={
            info() ? (info()!.daily_limit_micros > 0 ? `${dailyPct()}% used today` : "No daily limit") : "Loading…"
          }
        >
          <Show
            when={info() && info()!.daily_limit_micros > 0}
            fallback={<span class="text-[12px] text-v2-text-text-muted">—</span>}
          >
            <div class="flex w-40 flex-col gap-1">
              <div class="h-1.5 w-full overflow-hidden rounded-full bg-v2-background-bg-base">
                <div class="h-full rounded-full bg-[#3fb950]" style={{ width: `${dailyPct()}%` }} />
              </div>
              <span class="text-right text-[11px] text-v2-text-text-muted">{dailyPct()}%</span>
            </div>
          </Show>
        </SettingsRowV2>

        <SettingsRowV2
          title="Storage"
          description={
            info()
              ? `${fmtGB(info()!.storage_used_bytes)} used of ${fmtGB(info()!.storage_quota_bytes)}`
              : "Loading…"
          }
        >
          <div class="flex w-40 flex-col gap-1">
            <div class="h-1.5 w-full overflow-hidden rounded-full bg-v2-background-bg-base">
              <div class="h-full rounded-full bg-[#3fb950]" style={{ width: `${storagePct()}%` }} />
            </div>
            <span class="text-right text-[11px] text-v2-text-text-muted">{storagePct()}%</span>
          </div>
        </SettingsRowV2>

        <Show when={info() && info()!.pending_tier}>
          <SettingsRowV2
            title="Scheduled change"
            description={`At renewal: ${tierLabel(info()!.pending_tier)} · ${storageLabel(
              packForBytes(info()!.pending_storage_bytes),
            )}`}
          >
            <button class={btnGhost} onClick={cancelPending}>
              Cancel
            </button>
          </SettingsRowV2>
        </Show>
      </SettingsListV2>

      {/* ---- Change plan ---- */}
      <div class={sectionTitle}>Change plan</div>
      <SettingsListV2>
        <SettingsRowV2 title="Tier" description="Upgrades are prorated and charged now; downgrades apply at renewal.">
          <select class={inputCls} value={tier()} onInput={(e) => setTier(e.currentTarget.value)}>
            <For each={TIERS}>{(t) => <option value={t.value}>{`${t.label} — ${t.price}`}</option>}</For>
          </select>
        </SettingsRowV2>
        <SettingsRowV2 title="Storage" description="Extra storage is recurring and shares your renewal date.">
          <select class={inputCls} value={storage()} onInput={(e) => setStorage(e.currentTarget.value)}>
            <For each={STORAGE}>{(s) => <option value={s.value}>{s.label}</option>}</For>
          </select>
        </SettingsRowV2>

        <Show when={needCoin()}>
          <SettingsRowV2 title="Pay with" description="The prorated upgrade amount is charged in crypto.">
            <select class={inputCls} value={coin()} onInput={(e) => setCoin(e.currentTarget.value)}>
              <For each={COINS}>{(c) => <option value={c}>{c.toUpperCase()}</option>}</For>
            </select>
          </SettingsRowV2>
        </Show>

        <div class="flex items-center gap-2 px-1 pt-2">
          <Show
            when={needCoin()}
            fallback={
              <button class={btnPrimary} disabled={planBusy()} onClick={() => applyPlan(false)}>
                {planBusy() ? "Applying…" : "Apply change"}
              </button>
            }
          >
            <button class={btnPrimary} disabled={planBusy()} onClick={() => applyPlan(true)}>
              {planBusy() ? "Creating…" : "Create payment"}
            </button>
          </Show>
          <Show when={planMsg()}>
            <span class="text-[12px] text-v2-text-text-muted">{planMsg()}</span>
          </Show>
        </div>

        <Show when={payment()}>
          {(p) => (
            <div class="mx-1 mt-3 rounded-md border border-v2-border-border-muted p-3 text-[13px]">
              <div class="mb-1 font-medium text-v2-text-text-base">
                Send {p().pay_amount} {String(p().pay_currency).toUpperCase()} (≈ ${p().price_usd})
              </div>
              <div class="mb-2 text-[12px] text-v2-text-text-muted">
                Prorated for {p().days_remaining} day(s) remaining. Your plan upgrades automatically once the payment
                confirms.
              </div>
              <div class="flex justify-center mb-2">
                <canvas ref={upgQR} class="rounded-md bg-white p-2" style={{ "image-rendering": "pixelated" }} />
              </div>
              <div class="flex items-center gap-2">
                <code class="flex-1 break-all rounded bg-v2-background-bg-base px-2 py-1 text-[12px]">
                  {p().pay_address}
                </code>
                <button class={btnGhost} onClick={() => navigator.clipboard?.writeText(p().pay_address)}>
                  Copy
                </button>
              </div>
              <button class={`${btnGhost} mt-2`} onClick={loadInfo}>
                I&apos;ve paid — refresh
              </button>
            </div>
          )}
        </Show>
      </SettingsListV2>

      {/* ---- Top-up ---- */}
      <div class={sectionTitle}>Top up credit</div>
      <SettingsListV2>
        <SettingsRowV2
          title="Add compute credit"
          description="Extra credit on top of your monthly budget. Charged at 2× cost, and it does not roll over — use it before your next renewal."
        >
          <select class={inputCls} value={topAmt()} onInput={(e) => setTopAmt(e.currentTarget.value)}>
            <For each={TOPUPS}>{(t) => <option value={t.value}>{t.label}</option>}</For>
          </select>
        </SettingsRowV2>
        <SettingsRowV2 title="Pay with" description="Top-ups are crypto only.">
          <select class={inputCls} value={topCoin()} onInput={(e) => setTopCoin(e.currentTarget.value)}>
            <For each={COINS}>{(c) => <option value={c}>{c.toUpperCase()}</option>}</For>
          </select>
        </SettingsRowV2>

        <div class="flex items-center gap-2 px-1 pt-2">
          <button class={btnPrimary} disabled={topBusy()} onClick={buyTopup}>
            {topBusy() ? "Creating…" : "Buy credit"}
          </button>
          <Show when={topMsg()}>
            <span class="text-[12px] text-v2-text-text-muted">{topMsg()}</span>
          </Show>
        </div>

        <Show when={topPay()}>
          {(p) => (
            <div class="mx-1 mt-3 rounded-md border border-v2-border-border-muted p-3 text-[13px]">
              <div class="mb-1 font-medium text-v2-text-text-base">
                Send {p().pay_amount} {String(p().pay_currency).toUpperCase()} (≈ ${p().price_usd})
              </div>
              <div class="mb-2 text-[12px] text-v2-text-text-muted">
                Your credit is added automatically once the payment confirms.
              </div>
              <div class="flex justify-center mb-2">
                <canvas ref={topQR} class="rounded-md bg-white p-2" style={{ "image-rendering": "pixelated" }} />
              </div>
              <div class="flex items-center gap-2">
                <code class="flex-1 break-all rounded bg-v2-background-bg-base px-2 py-1 text-[12px]">
                  {p().pay_address}
                </code>
                <button class={btnGhost} onClick={() => navigator.clipboard?.writeText(p().pay_address)}>
                  Copy
                </button>
              </div>
              <button class={`${btnGhost} mt-2`} onClick={loadInfo}>
                I&apos;ve paid — refresh
              </button>
            </div>
          )}
        </Show>
      </SettingsListV2>

      {/* GRAMSAI_RENEW: payment history */}
      <div class={sectionTitle}>Payment history</div>
      <SettingsListV2>
        <Show when={history().length > 0} fallback={<div class="px-1 py-2 text-[13px] text-v2-text-text-muted">No payments yet.</div>}>
          <For each={history()}>
            {(p) => (
              <SettingsRowV2
                title={p.kind === "topup" ? "Top-up" : (p.kind === "upgrade" ? "Upgrade" : `${tierLabel(p.tier)} ${p.period}`)}
                description={`${fmtDate(p.created)} · ${String(p.coin || "").toUpperCase()} · ${p.status}`}
              >
                <span class={
                  "text-[13px] font-medium " +
                  (p.status === "finished" || p.status === "applied" || p.status === "confirmed"
                    ? "text-[#3fb950]" : (p.status === "failed" || p.status === "expired" ? "text-[#da3633]" : "text-v2-text-text-muted"))
                }>${p.price_usd?.toFixed ? p.price_usd.toFixed(2) : p.price_usd}</span>
              </SettingsRowV2>
            )}
          </For>
        </Show>
      </SettingsListV2>
    </div>
  )
}
