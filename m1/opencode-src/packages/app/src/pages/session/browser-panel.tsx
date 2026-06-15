import { createSignal, onCleanup, onMount, Show } from "solid-js"

// GRAMSAI live browser panel.
// Renders the per-user container's Chromium screencast (proxied by M1 at
// /api/browser/ws/stream) into a canvas, with pause / live / take-control.
// Same-origin behind Cloudflare, so the session cookie authenticates the WS
// automatically (same reason /api/browser/healthz works in the browser).

function wsBase(): string {
  // ws:// for http, wss:// for https — match the page protocol.
  const proto = location.protocol === "https:" ? "wss:" : "ws:"
  return `${proto}//${location.host}`
}

export function BrowserPanel(props: { active: () => boolean }) {
  let canvas!: HTMLCanvasElement
  let streamWS: WebSocket | undefined
  let controlWS: WebSocket | undefined

  const [connected, setConnected] = createSignal(false)
  const [paused, setPaused] = createSignal(false)
  const [takeover, setTakeover] = createSignal(false)
  const [status, setStatus] = createSignal("connecting")

  // ---- frame rendering ----
  const paint = (b64: string) => {
    const ctx = canvas?.getContext("2d")
    if (!ctx) return
    const img = new Image()
    img.onload = () => {
      // size the canvas to the frame once, then draw
      if (canvas.width !== img.width || canvas.height !== img.height) {
        canvas.width = img.width
        canvas.height = img.height
      }
      ctx.drawImage(img, 0, 0)
    }
    img.src = "data:image/jpeg;base64," + b64
  }

  // ---- stream socket ----
  const connectStream = () => {
    const ws = new WebSocket(`${wsBase()}/api/browser/ws/stream`)
    streamWS = ws
    ws.onopen = () => {
      setConnected(true)
      setStatus("live")
    }
    ws.onmessage = (e) => {
      if (typeof e.data !== "string") return
      let msg: any
      try {
        msg = JSON.parse(e.data)
      } catch {
        return
      }
      if (msg.type === "frame" && !paused()) paint(msg.data)
    }
    ws.onclose = () => {
      setConnected(false)
      setStatus("disconnected")
      streamWS = undefined
    }
    ws.onerror = () => {
      setStatus("error")
    }
  }

  // ---- control socket (takeover) ----
  const connectControl = () => {
    const ws = new WebSocket(`${wsBase()}/api/browser/ws/control`)
    controlWS = ws
    ws.onclose = () => {
      controlWS = undefined
    }
  }

  const sendControl = (obj: any) => {
    if (controlWS && controlWS.readyState === WebSocket.OPEN) {
      controlWS.send(JSON.stringify(obj))
    }
  }

  // map a canvas pointer event to page coordinates (canvas may be CSS-scaled)
  const toPage = (e: PointerEvent) => {
    const rect = canvas.getBoundingClientRect()
    const sx = canvas.width / rect.width
    const sy = canvas.height / rect.height
    return {
      x: Math.round((e.clientX - rect.left) * sx),
      y: Math.round((e.clientY - rect.top) * sy),
    }
  }

  // ---- controls ----
  const doPause = () => {
    setPaused(true)
    setStatus("paused")
    if (streamWS?.readyState === WebSocket.OPEN) streamWS.send(JSON.stringify({ action: "pause" }))
  }
  const doLive = () => {
    setPaused(false)
    setStatus("live")
    if (streamWS?.readyState === WebSocket.OPEN) streamWS.send(JSON.stringify({ action: "live" }))
  }
  const toggleTakeover = () => {
    const next = !takeover()
    setTakeover(next)
    if (next && !controlWS) connectControl()
  }

  // ---- pointer/key handlers (only act when takeover is on) ----
  const onPointerMove = (e: PointerEvent) => {
    if (!takeover()) return
    const p = toPage(e)
    sendControl({ kind: "mouse", eventType: "mouseMoved", x: p.x, y: p.y })
  }
  const onPointerDown = (e: PointerEvent) => {
    if (!takeover()) return
    const p = toPage(e)
    sendControl({ kind: "mouse", eventType: "mousePressed", x: p.x, y: p.y, button: "left", clickCount: 1 })
  }
  const onPointerUp = (e: PointerEvent) => {
    if (!takeover()) return
    const p = toPage(e)
    sendControl({ kind: "mouse", eventType: "mouseReleased", x: p.x, y: p.y, button: "left", clickCount: 1 })
  }
  const onWheel = (e: WheelEvent) => {
    if (!takeover()) return
    e.preventDefault()
    const rect = canvas.getBoundingClientRect()
    sendControl({
      kind: "mouse",
      eventType: "mouseWheel",
      x: Math.round((e.clientX - rect.left) * (canvas.width / rect.width)),
      y: Math.round((e.clientY - rect.top) * (canvas.height / rect.height)),
      deltaX: e.deltaX,
      deltaY: e.deltaY,
    })
  }
  const onKeyDown = (e: KeyboardEvent) => {
    if (!takeover()) return
    sendControl({ kind: "key", eventType: "keyDown", key: e.key, code: e.code, text: e.key.length === 1 ? e.key : "" })
  }
  const onKeyUp = (e: KeyboardEvent) => {
    if (!takeover()) return
    sendControl({ kind: "key", eventType: "keyUp", key: e.key, code: e.code })
  }

  onMount(() => {
    connectStream()
    window.addEventListener("keydown", onKeyDown)
    window.addEventListener("keyup", onKeyUp)
  })

  onCleanup(() => {
    window.removeEventListener("keydown", onKeyDown)
    window.removeEventListener("keyup", onKeyUp)
    if (streamWS && streamWS.readyState !== WebSocket.CLOSED) streamWS.close(1000)
    if (controlWS && controlWS.readyState !== WebSocket.CLOSED) controlWS.close(1000)
  })

  return (
    <div class="flex flex-col h-full overflow-hidden bg-background-stronger contain-strict">
      {/* control bar */}
      <div class="shrink-0 flex items-center gap-2 px-3 py-2 border-b border-border-weaker-base">
        <button
          type="button"
          class="text-12-medium px-2 py-1 rounded-md border border-border-weak-base text-text-strong disabled:opacity-50"
          onClick={doPause}
          disabled={paused() || !connected()}
        >
          Pause
        </button>
        <button
          type="button"
          class="text-12-medium px-2 py-1 rounded-md border border-border-weak-base text-text-strong disabled:opacity-50"
          onClick={doLive}
          disabled={!paused() || !connected()}
        >
          Live
        </button>
        <button
          type="button"
          class="text-12-medium px-2 py-1 rounded-md border"
          classList={{
            "border-[#3fb950] text-[#3fb950]": takeover(),
            "border-border-weak-base text-text-strong": !takeover(),
          }}
          onClick={toggleTakeover}
          disabled={!connected()}
        >
          {takeover() ? "Release control" : "Take control"}
        </button>
        <div class="ml-auto flex items-center gap-1.5 text-11-regular text-text-weak">
          <span
            class="inline-block size-2 rounded-full"
            classList={{
              "bg-[#3fb950]": status() === "live",
              "bg-yellow-500": status() === "paused",
              "bg-text-weak": status() !== "live" && status() !== "paused",
            }}
          />
          {status()}
        </div>
      </div>

      {/* canvas viewport */}
      <div class="relative flex-1 min-h-0 overflow-auto flex items-start justify-center bg-black">
        <canvas
          ref={canvas}
          class="max-w-full h-auto"
          classList={{ "cursor-default": takeover(), "pointer-events-none": !takeover() }}
          onPointerMove={onPointerMove}
          onPointerDown={onPointerDown}
          onPointerUp={onPointerUp}
          onWheel={onWheel}
        />
        <Show when={!connected()}>
          <div class="absolute inset-0 flex items-center justify-center text-12-regular text-text-weak">
            {status() === "error" ? "browser unavailable" : "connecting to browser…"}
          </div>
        </Show>
      </div>
    </div>
  )
}
