// components/settings-v2/account.tsx
// Self-service Account tab: plan + usage/storage, change plan (upgrade->pay,
// downgrade->schedule, cancel pending), change password, active sessions, and
// a danger-zone delete. Talks to the gateway endpoints in internal/auth/account.go
// and internal/pay (account change). Dependency-light: plain fetch + inline status.
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

type Session = {
  token: string
  current: boolean
  user_agent: string
  ip: string
  created_at: string
  last_seen: string
  expires_at: string
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
const btnDanger =
  "rounded-md bg-[#da3633] px-3 py-1.5 text-[13px] font-medium text-white hover:opacity-90 disabled:opacity-40"
const sectionTitle = "px-1 pb-2 pt-4 text-[11px] font-medium uppercase tracking-wide text-v2-text-text-muted"

export const SettingsAccountV2: Component = () => {
  const [info, setInfo] = createSignal<Info | null>(null)
  const [infoErr, setInfoErr] = createSignal("")
  // GRAMSAI_ACCT_HEADER: identity + linked providers
  const [acctName, setAcctName] = createSignal("")
  const [conns, setConns] = createSignal<{ provider: string; email: string }[]>([])
  async function loadConnections() {
    try {
      const r = await fetch("/account/connections", { credentials: "include" })
      const j = await r.json().catch(() => ({}))
      if (r.ok) {
        setAcctName(j.username || "")
        setConns(Array.isArray(j.connections) ? j.connections : [])
      }
    } catch {}
  }
  function hasProvider(p: string) { return conns().some((c) => c.provider === p) }
  function connect(p: string) { window.location.href = "/auth/oauth/" + p + "/start" }

  // plan change
  const [tier, setTier] = createSignal("basic")
  const [storage, setStorage] = createSignal("")
  const [coin, setCoin] = createSignal(COINS[0])
  const [needCoin, setNeedCoin] = createSignal(false)
  const [planMsg, setPlanMsg] = createSignal("")
  const [payment, setPayment] = createSignal<any | null>(null)
  const [planBusy, setPlanBusy] = createSignal(false)

  // password
  const [oldPw, setOldPw] = createSignal("")
  const [newPw, setNewPw] = createSignal("")
  const [confPw, setConfPw] = createSignal("")
  const [pwMsg, setPwMsg] = createSignal("")
  const [pwBusy, setPwBusy] = createSignal(false)

  // sessions
  const [sessions, setSessions] = createSignal<Session[]>([])

  // delete
  const [delConfirm, setDelConfirm] = createSignal("")
  const [delMsg, setDelMsg] = createSignal("")
  const [delBusy, setDelBusy] = createSignal(false)

  async function loadInfo() {
    try {
      const r = await fetch("/account/info", { credentials: "include" })
      if (!r.ok) throw new Error()
      const j: Info = await r.json()
      setInfo(j)
      setTier(j.tier || "basic")
      setStorage(packForBytes(j.storage_extra_bytes))
      setInfoErr("")
    } catch {
      setInfoErr("Could not load account info.")
    }
  }
  async function loadSessions() {
    try {
      const r = await fetch("/account/sessions", { credentials: "include" })
      if (!r.ok) return
      const j = await r.json()
      setSessions(j.sessions || [])
    } catch {
      /* ignore */
    }
  }
  onMount(() => {
    loadInfo()
    loadSessions()
    load2FA()
    loadConnections()
  })

  // ---- 2FA state ----
  const [tfaEnabled, setTfaEnabled] = createSignal(false)
  const [tfaRecoveryLeft, setTfaRecoveryLeft] = createSignal(0)
  const [tfaSecret, setTfaSecret] = createSignal("")      // shown during setup
  const [tfaOtpauth, setTfaOtpauth] = createSignal("")
  const [tfaCode, setTfaCode] = createSignal("")          // confirm/disable input
  const [tfaCodes, setTfaCodes] = createSignal<string[]>([]) // recovery codes shown once
  const [tfaMsg, setTfaMsg] = createSignal("")
  const [tfaBusy, setTfaBusy] = createSignal(false)
  let qrCanvas: HTMLCanvasElement | undefined  // GRAMSAI_2FA_QR
  // Draw the otpauth URI into the canvas whenever it changes (same algorithm
  // as the payment-page QR: kazuhikoarase generator -> isDark grid -> fillRect).
  createEffect(() => {
    const uri = tfaOtpauth()
    const cv = qrCanvas
    if (!uri || !cv) return
    try {
      const q = (qrcode as any)(0, "M")
      q.addData(uri)
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
    } catch (_) { /* fallback: secret string is still shown below */ }
  })

  async function load2FA() {
    try {
      const r = await fetch("/account/2fa/status", { credentials: "include" })
      const j = await r.json().catch(() => ({}))
      if (r.ok) {
        setTfaEnabled(!!j.enabled)
        setTfaRecoveryLeft(j.recovery_remaining ?? 0)
      }
    } catch {}
  }

  async function tfaSetup() {
    setTfaMsg(""); setTfaBusy(true)
    try {
      const r = await fetch("/account/2fa/setup", { method: "POST", credentials: "include" })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) { setTfaMsg(j.error || "Could not start setup."); return }
      setTfaSecret(j.secret || "")
      setTfaOtpauth(j.otpauth || "")
      setTfaCodes([])
    } finally { setTfaBusy(false) }
  }

  async function tfaEnable() {
    setTfaMsg("")
    if (!tfaCode().trim()) { setTfaMsg("Enter the 6-digit code."); return }
    setTfaBusy(true)
    try {
      const r = await fetch("/account/2fa/enable", {
        method: "POST", credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ code: tfaCode().trim() }),
      })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) { setTfaMsg(j.error || "Invalid code."); return }
      setTfaCodes(j.recovery_codes || [])
      setTfaSecret(""); setTfaOtpauth(""); setTfaCode("")
      setTfaEnabled(true)
      setTfaRecoveryLeft((j.recovery_codes || []).length)
      setTfaMsg("Two-factor authentication is now enabled. Save your recovery codes below.")
    } finally { setTfaBusy(false) }
  }

  async function tfaDisable() {
    setTfaMsg("")
    if (!tfaCode().trim()) { setTfaMsg("Enter a code (or recovery code) to disable."); return }
    setTfaBusy(true)
    try {
      const r = await fetch("/account/2fa/disable", {
        method: "POST", credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ code: tfaCode().trim() }),
      })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) { setTfaMsg(j.error || "Invalid code."); return }
      setTfaEnabled(false); setTfaRecoveryLeft(0); setTfaCode("")
      setTfaCodes([]); setTfaSecret(""); setTfaOtpauth("")
      setTfaMsg("Two-factor authentication disabled.")
    } finally { setTfaBusy(false) }
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

  async function changePassword() {
    setPwMsg("")
    if (newPw().length < 8) {
      setPwMsg("New password must be at least 8 characters.")
      return
    }
    if (newPw() !== confPw()) {
      setPwMsg("New passwords do not match.")
      return
    }
    setPwBusy(true)
    try {
      const r = await fetch("/account/password", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ old: oldPw(), new: newPw() }),
      })
      const j = await r.json().catch(() => ({}))
      if (!r.ok) {
        setPwMsg(j.error || "Could not change password.")
        return
      }
      setPwMsg("Password changed. Your other sessions were signed out.")
      setOldPw("")
      setNewPw("")
      setConfPw("")
      loadSessions()
    } catch {
      setPwMsg("Network error.")
    } finally {
      setPwBusy(false)
    }
  }

  async function revoke(token: string) {
    try {
      await fetch("/account/sessions/revoke", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      })
      loadSessions()
    } catch {
      /* ignore */
    }
  }
  async function revokeOthers() {
    try {
      await fetch("/account/sessions/revoke-others", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
      })
      loadSessions()
    } catch {
      /* ignore */
    }
  }

  async function deleteAccount() {
    if (delConfirm() !== "DELETE") {
      setDelMsg('Type DELETE (in capitals) to confirm.')
      return
    }
    setDelBusy(true)
    setDelMsg("")
    try {
      const r = await fetch("/account/delete", { method: "POST", credentials: "include" })
      if (r.ok) {
        window.location.href = "/"
        return
      }
      const j = await r.json().catch(() => ({}))
      setDelMsg(j.error || "Could not delete account.")
    } catch {
      setDelMsg("Network error.")
    } finally {
      setDelBusy(false)
    }
  }

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

  return (
    <div class="flex flex-col gap-1 overflow-y-auto">
      {/* GRAMSAI_ACCT_HEADER */}
      <div class="mx-1 mb-2 flex items-center justify-between gap-3 rounded-lg border border-v2-border-border-muted bg-v2-background-bg-subtle p-3">
        <div class="flex items-center gap-3 min-w-0">
          <div class="grid size-9 shrink-0 place-items-center rounded-full bg-[#3fb950]/15 text-[15px] font-semibold text-[#3fb950]">
            {(acctName()[0] || "?").toUpperCase()}
          </div>
          <div class="min-w-0">
            <div class="truncate text-[14px] font-medium text-v2-text-text-base">{acctName() || "—"}</div>
            <div class="truncate text-[12px] text-v2-text-text-muted">
              <Show when={conns().length > 0} fallback="No connected accounts">
                {`Connected: ${conns().map((c) => c.provider).join(", ")}`}
              </Show>
            </div>
          </div>
        </div>
        <div class="flex shrink-0 items-center gap-2">
          <Show when={!hasProvider("github")}>
            <button class={btnGhost} onClick={() => connect("github")}>Connect GitHub</button>
          </Show>
          <Show when={!hasProvider("google")}>
            <button class={btnGhost} onClick={() => connect("google")}>Connect Google</button>
          </Show>
        </div>
      </div>
      <Show when={infoErr()}>
        <div class="px-1 py-2 text-[13px] text-[#da3633]">{infoErr()}</div>
      </Show>

      {/* BILLING_MOVED: Plan & usage + Change plan now live in billing.tsx */}
      {/* ---- Password ---- */}
      <div class={sectionTitle}>Password</div>
      <SettingsListV2>
        <SettingsRowV2 title="Current password" description="Verify it's you before changing.">
          <input
            type="password"
            class={inputCls}
            value={oldPw()}
            autocomplete="current-password"
            onInput={(e) => setOldPw(e.currentTarget.value)}
          />
        </SettingsRowV2>
        <SettingsRowV2 title="New password" description="At least 8 characters.">
          <input
            type="password"
            class={inputCls}
            value={newPw()}
            autocomplete="new-password"
            onInput={(e) => setNewPw(e.currentTarget.value)}
          />
        </SettingsRowV2>
        <SettingsRowV2 title="Confirm new password" description="Re-enter the new password.">
          <input
            type="password"
            class={inputCls}
            value={confPw()}
            autocomplete="new-password"
            onInput={(e) => setConfPw(e.currentTarget.value)}
          />
        </SettingsRowV2>
        <div class="flex items-center gap-2 px-1 pt-2">
          <button class={btnPrimary} disabled={pwBusy()} onClick={changePassword}>
            {pwBusy() ? "Saving…" : "Change password"}
          </button>
          <Show when={pwMsg()}>
            <span class="text-[12px] text-v2-text-text-muted">{pwMsg()}</span>
          </Show>
        </div>
      </SettingsListV2>

      {/* GRAMSAI_2FA_SECTION ---- Two-factor authentication ---- */}
      <div class={sectionTitle}>Two-factor authentication</div>
      <SettingsListV2>
        <Show
          when={tfaEnabled()}
          fallback={
            <>
              <SettingsRowV2
                title="Authenticator app (TOTP)"
                description="Protect your account with a time-based code from an app like Aegis, Google Authenticator, or 1Password."
              >
                <Show
                  when={tfaSecret()}
                  fallback={
                    <button class={btnPrimary} disabled={tfaBusy()} onClick={tfaSetup}>
                      {tfaBusy() ? "Starting…" : "Enable"}
                    </button>
                  }
                >
                  <span class="text-[12px] text-v2-text-text-muted">Scan or paste the key below</span>
                </Show>
              </SettingsRowV2>

              <Show when={tfaSecret()}>
                <div class="mx-1 mt-1 flex flex-col gap-3 rounded-md border border-v2-border-border-muted p-3">
                  <div class="text-[13px] text-v2-text-text-base">
                    1. Scan this QR with your authenticator app:
                  </div>
                  <div class="flex justify-center">
                    {/* GRAMSAI_2FA_QR */}
                    <canvas ref={qrCanvas} class="rounded-md bg-white p-2" style={{ "image-rendering": "pixelated" }} />
                  </div>
                  <div class="text-[13px] text-v2-text-text-base">
                    Or paste this secret manually:
                  </div>
                  <div class="flex items-center gap-2">
                    <code class="flex-1 break-all rounded bg-v2-background-bg-base px-2 py-1 text-[12px]">{tfaSecret()}</code>
                    <button class={btnGhost} onClick={() => navigator.clipboard?.writeText(tfaSecret())}>Copy</button>
                  </div>
                  <div class="flex items-center gap-2">
                    <code class="flex-1 break-all rounded bg-v2-background-bg-base px-2 py-1 text-[11px] text-v2-text-text-muted">{tfaOtpauth()}</code>
                    <button class={btnGhost} onClick={() => navigator.clipboard?.writeText(tfaOtpauth())}>Copy URI</button>
                  </div>
                  <div class="text-[13px] text-v2-text-text-base">2. Enter the 6-digit code to confirm:</div>
                  <div class="flex items-center gap-2">
                    <input
                      class={inputCls}
                      inputmode="numeric"
                      autocomplete="one-time-code"
                      placeholder="123456"
                      value={tfaCode()}
                      onInput={(e) => setTfaCode(e.currentTarget.value)}
                    />
                    <button class={btnPrimary} disabled={tfaBusy()} onClick={tfaEnable}>
                      {tfaBusy() ? "Verifying…" : "Confirm"}
                    </button>
                  </div>
                </div>
              </Show>
            </>
          }
        >
          <SettingsRowV2
            title={<span>Authenticator app <span class="ml-2 text-[11px] text-[#3fb950]">Enabled</span></span>}
            description={`Recovery codes remaining: ${tfaRecoveryLeft()}.`}
          >
            <div class="flex items-center gap-2">
              <input
                class={inputCls}
                inputmode="text"
                placeholder="Code to disable"
                value={tfaCode()}
                onInput={(e) => setTfaCode(e.currentTarget.value)}
              />
              <button class={btnDanger} disabled={tfaBusy()} onClick={tfaDisable}>
                {tfaBusy() ? "…" : "Disable"}
              </button>
            </div>
          </SettingsRowV2>
        </Show>

        {/* recovery codes shown ONCE after enabling */}
        <Show when={tfaCodes().length > 0}>
          <div class="mx-1 mt-1 flex flex-col gap-2 rounded-md border border-[#3fb950]/40 bg-[#3fb950]/[0.06] p-3">
            <div class="text-[13px] font-medium text-v2-text-text-base">Save these recovery codes</div>
            <div class="text-[12px] text-v2-text-text-muted">
              Each can be used once if you lose your authenticator. They won't be shown again.
            </div>
            <div class="grid grid-cols-2 gap-1 font-mono text-[12px]">
              <For each={tfaCodes()}>{(c) => <code class="rounded bg-v2-background-bg-base px-2 py-1">{c}</code>}</For>
            </div>
            <button class={`${btnGhost} mt-1 w-fit`} onClick={() => navigator.clipboard?.writeText(tfaCodes().join("\n"))}>
              Copy all
            </button>
          </div>
        </Show>

        <Show when={tfaMsg()}>
          <div class="px-1 pt-2 text-[12px] text-v2-text-text-muted">{tfaMsg()}</div>
        </Show>
      </SettingsListV2>

      {/* ---- Sessions ---- */}
      <div class={sectionTitle}>Active sessions</div>
      <SettingsListV2>
        <For
          each={sessions()}
          fallback={<div class="px-1 py-2 text-[13px] text-v2-text-text-muted">No active sessions.</div>}
        >
          {(s) => (
            <SettingsRowV2
              title={
                <span>
                  {s.user_agent || "Unknown device"}
                  {s.current ? <span class="ml-2 text-[11px] text-[#3fb950]">(this device)</span> : null}
                </span>
              }
              description={`${s.ip || "—"} · last seen ${fmtDate(s.last_seen)} · expires ${fmtDate(s.expires_at)}`}
            >
              <Show when={!s.current} fallback={<span class="text-[12px] text-v2-text-text-muted">current</span>}>
                <button class={btnGhost} onClick={() => revoke(s.token)}>
                  Revoke
                </button>
              </Show>
            </SettingsRowV2>
          )}
        </For>
        <Show when={sessions().some((s) => !s.current)}>
          <div class="px-1 pt-2">
            <button class={btnGhost} onClick={revokeOthers}>
              Sign out all other sessions
            </button>
          </div>
        </Show>
      </SettingsListV2>

      {/* ---- Danger zone ---- */}
      <div class={`${sectionTitle} text-[#da3633]`}>Danger zone</div>
      <SettingsListV2>
        <SettingsRowV2
          title="Delete account"
          description="Permanently deletes your account, container, and all data. This cannot be undone."
        >
          <div class="flex items-center gap-2">
            <input
              type="text"
              placeholder="Type DELETE"
              class={inputCls}
              value={delConfirm()}
              onInput={(e) => setDelConfirm(e.currentTarget.value)}
            />
            <button class={btnDanger} disabled={delBusy() || delConfirm() !== "DELETE"} onClick={deleteAccount}>
              {delBusy() ? "Deleting…" : "Delete"}
            </button>
          </div>
        </SettingsRowV2>
        <Show when={delMsg()}>
          <div class="px-1 py-1 text-[12px] text-[#da3633]">{delMsg()}</div>
        </Show>
      </SettingsListV2>
    </div>
  )
}
