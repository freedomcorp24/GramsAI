import { createEffect, createMemo, createSignal, For, Show, onMount, onCleanup } from "solid-js"
import { useNavigate, useParams } from "@solidjs/router"
import { base64Encode } from "@opencode-ai/core/util/encode"
import type { Session } from "@opencode-ai/sdk/v2/client"
import { useLayout, type LocalProject } from "@/context/layout"
import { useServerSync, onGramsaiSessionChanged } from "@/context/server-sync"
import { useServer } from "@/context/server"
import { useServerSDK } from "@/context/server-sdk"
import { sessionTitle } from "@/utils/session-title"
import { pathKey } from "@/utils/path-key"
import { sortedRootSessions, projectForSession } from "@/pages/layout/helpers"

const DEFAULT_WORKSPACE = "/workspace"

// Short relative timestamp for chat rows (Today HH:MM / Yesterday HH:MM / Mon D).
function relTime(ms: number): string {
  if (!ms) return ""
  const d = new Date(ms)
  const now = new Date()
  const hm = d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
  const sameDay = d.toDateString() === now.toDateString()
  const y = new Date(now)
  y.setDate(now.getDate() - 1)
  const yest = d.toDateString() === y.toDateString()
  if (sameDay) return `Today, ${hm}`
  if (yest) return `Yesterday, ${hm}`
  return d.toLocaleDateString([], { month: "short", day: "numeric" }) + `, ${hm}`
}

/**
 * Left navigation: brand, New chat, CHAT HISTORY list (newest first) with
 * timestamps and an active-glow state, an Upgrade card, a user row, Settings
 * and Logout. Data wiring (chats/openChat/newChat/onOpenSettings) is unchanged.
 * The right-hand session panel is untouched.
 */
export function LeftNav(props: { onOpenSettings: (tab?: string) => void }) {
  const layout = useLayout()
  const sync = useServerSync()
  const server = useServer()
  const serverSDK = useServerSDK()
  const navigate = useNavigate()
  const params = useParams()

  // current user (tier label + handle) from the gateway — no hardcoding.
  const [me, setMe] = createSignal<{ username?: string; tier?: string } | null>(null)
  onMount(async () => {
    try {
      const r = await fetch("/auth/me", { credentials: "same-origin" })
      if (r.ok) setMe(await r.json())
    } catch {
      /* ignore — row just shows generic */
    }
  })
  const planLabel = createMemo(() => {
    const t = me()?.tier
    if (!t) return ""
    return t.charAt(0).toUpperCase() + t.slice(1) + " Plan"
  })
  const handle = createMemo(() => me()?.username || "account")
  const initial = createMemo(() => (handle()[0] || "g").toUpperCase())

  const logout = async () => {
    try {
      await fetch("/auth/logout", { method: "POST", credentials: "same-origin" })
    } catch {
      /* ignore */
    }
    window.location.href = "/"
  }

  const projects = createMemo(() => {
    try {
      return layout.projects.list()
    } catch {
      return [] as LocalProject[]
    }
  })

  const projectByID = createMemo(
    () => new Map(projects().flatMap((p) => (p.id ? [[p.id, p] as const] : []))),
  )

  const directories = (p: LocalProject) => [p.worktree, ...(p.sandboxes ?? [])]
  const projectDirectories = createMemo(() => projects().flatMap(directories))

  // GRAMSAI_DIRECT_FETCH: load chats via a direct same-origin call to /session
  // (NO directory param -> container returns the global newest across all
  // worktrees). The SDK/store path mis-handles empty directory, so bypass it.
  const [chatList, setChatList] = createSignal<Session[]>([])
  const [chatLimit, setChatLimit] = createSignal(10)
  const [chatLoading, setChatLoading] = createSignal(false)
  const loadChats = async () => {
    setChatLoading(true)
    try {
      const r = await fetch(`/session?roots=true&limit=${chatLimit()}`, { credentials: "same-origin" })
      if (r.ok) {
        const data = await r.json()
        const items = (Array.isArray(data) ? data : [])
          .filter((x: any) => !!x?.id && !x?.time?.archived)
          .sort((a: any, b: any) => (b.time?.updated ?? b.time?.created ?? 0) - (a.time?.updated ?? a.time?.created ?? 0))
        setChatList(items as Session[])
      }
    } catch {
      /* ignore */
    } finally {
      setChatLoading(false)
    }
  }
  onMount(() => {
    if (projects().length === 0) {
      try { layout.projects.open(DEFAULT_WORKSPACE) } catch { /* ignore */ }
    }
    void loadChats()
  })

  // Keep the sidebar list reactive to server-driven session changes. The chat
  // list is loaded via a direct /session fetch (the SDK store mis-handles the
  // empty-directory global view), so the list is NOT auto-reconciled. Subscribe
  // to session lifecycle events and refetch (debounced) so new chats appear,
  // deleted/archived chats disappear (no stale 404 clicks), and renames/moves
  // reflect immediately — without a manual page refresh.
  onMount(() => {
    let t: ReturnType<typeof setTimeout> | undefined
    const refetch = () => { clearTimeout(t); t = setTimeout(() => void loadChats(), 150) }
    // Refetch the (direct-fetch) chat list whenever ANY session.* event fires
    // anywhere — fires before the per-directory store gate, so new/delete/
    // delete-all/rename/archive/project-move all update the sidebar live.
    const off = onGramsaiSessionChanged(refetch)
    onCleanup(() => { clearTimeout(t); off() })
  })

  const chats = createMemo<Session[]>(() => chatList())

  // Load-more: bump the limit by 10 and refetch from /session.
  const [loadingMore, setLoadingMore] = createSignal(false)
  const loadMore = async () => {
    setLoadingMore(true)
    try {
      setChatLimit((n) => n + 10)
      await loadChats()
    } finally {
      setLoadingMore(false)
    }
  }

  const openChat = (s: Session) => {
    try {
      const project = projectForSession(s, projects(), projectByID())
      layout.projects.open(project?.worktree ?? s.directory)
      server.projects.touch(project?.worktree ?? s.directory)
      navigate(`/${base64Encode(s.directory)}/session/${s.id}`)
      layout.mobileSidebar.hide()
    } catch {
      /* ignore */
    }
  }

  const newChat = () => {
    navigate("/")
    layout.mobileSidebar.hide()
  }

  return (
    <div class="flex h-full w-full min-w-0 flex-col overflow-hidden pt-2">
      <div class="flex min-h-0 w-full flex-1 flex-col overflow-hidden bg-v2-background-bg-base">

        {/* New chat */}
        <div class="shrink-0 px-3 pt-2 pb-2">
          <button
            type="button"
            onClick={newChat}
            class="flex w-full items-center gap-2 rounded-lg border border-v2-border-border-muted bg-v2-background-bg-subtle px-3 py-2.5 text-left text-[13px] [font-weight:560] text-v2-text-text-base transition-colors hover:border-[#3fb950]/50 hover:bg-v2-overlay-simple-overlay-hover focus-visible:outline-none"
          >
            <span class="text-[18px] leading-none text-[#3fb950]">+</span>
            <span>New chat</span>
          </button>
        </div>

        {/* CHAT HISTORY label */}
        <div class="shrink-0 px-4 pt-2 pb-1">
          <span class="text-[11px] [font-weight:600] uppercase tracking-[0.12em] text-v2-text-text-muted">Chat history</span>
        </div>

        {/* Chat list */}
        <div class="min-h-0 flex-1 overflow-y-auto no-scrollbar px-2 pb-2">
          <Show
            when={chats().length > 0}
            fallback={<div class="px-2 py-3 text-[12px] text-v2-text-text-muted">No chats yet</div>}
          >
            <div class="flex flex-col gap-1">
              <For each={chats()}>
                {(s) => {
                  const active = () => params.id === s.id
                  return (
                    <button
                      type="button"
                      onClick={() => openChat(s)}
                      title={sessionTitle(s.title) || s.id}
                      classList={{
                        "w-full rounded-lg px-3 py-2 text-left transition-colors focus-visible:outline-none": true,
                        "border border-[#3fb950] bg-[#3fb950]/10 shadow-[0_0_0_1px_rgba(63,185,80,0.25),0_0_22px_-8px_rgba(63,185,80,0.6)]":
                          active(),
                        "border border-transparent hover:bg-v2-overlay-simple-overlay-hover": !active(),
                      }}
                    >
                      <div class="truncate text-[13px] [font-weight:500] text-v2-text-text-base">
                        {sessionTitle(s.title) || "Untitled chat"}
                      </div>
                      <div class="mt-0.5 text-[11px] text-v2-text-text-muted">
                        {relTime(s.time.updated ?? s.time.created)}
                      </div>
                    </button>
                  )
                }}
              </For>
              {/* GRAMSAI_LOADMORE_BTN */}
              <Show when={chats().length >= 10}>
                <button
                  type="button"
                  onClick={loadMore}
                  disabled={loadingMore()}
                  class="mt-1 w-full rounded-lg px-3 py-2 text-center text-[12px] [font-weight:500] text-v2-text-text-muted hover:bg-v2-overlay-simple-overlay-hover hover:text-v2-text-text-base disabled:opacity-50"
                >
                  {loadingMore() ? "Loading…" : "Load more"}
                </button>
              </Show>
            </div>
          </Show>
        </div>

        {/* GRAMSAI_HEADER_UPGRADE: upgrade card removed; button moved to user row */}

        {/* User row + Settings + Logout */}
        <div class="shrink-0 border-t border-v2-border-border-muted px-2 py-2">
          <div class="flex items-center gap-2 rounded-lg px-2 py-1.5">
            <span class="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-v2-background-bg-subtle text-[12px] [font-weight:700] text-v2-text-text-base">
              {initial()}
            </span>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-[13px] [font-weight:500] text-v2-text-text-base">{handle()}</span>
              <Show when={planLabel()}>
                <span class="block truncate text-[11px] [font-weight:600] text-[#3fb950]">{planLabel()}</span>
              </Show>
            </span>
            <button
              type="button"
              onClick={() => props.onOpenSettings("billing")}
              class="shrink-0 rounded-md border border-[#3fb950]/40 bg-[#3fb950]/[0.07] px-2.5 py-1 text-[12px] [font-weight:600] text-[#3fb950] transition-colors hover:bg-[#3fb950]/[0.14]"
            >
              Upgrade
            </button>
          </div>
          <div class="mt-1 flex items-center gap-1">
            <button
              type="button"
              onClick={() => props.onOpenSettings()}
              class="flex flex-1 items-center gap-2 rounded-md px-2 py-2 text-left text-[13px] [font-weight:440] text-v2-text-text-muted hover:bg-v2-overlay-simple-overlay-hover hover:text-v2-text-text-base focus-visible:outline-none"
            >
              <span>Settings</span>
            </button>
            <button
              type="button"
              onClick={logout}
              class="flex items-center gap-2 rounded-md px-2 py-2 text-left text-[13px] [font-weight:440] text-v2-text-text-muted hover:bg-v2-overlay-simple-overlay-hover hover:text-v2-text-text-base focus-visible:outline-none"
            >
              <span>Logout</span>
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
