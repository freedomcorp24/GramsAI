#!/usr/bin/env python3
"""
GramsAI browser-bridge (CDP edition, Alpine/musl compatible)
------------------------------------------------------------
Drives Alpine's chromium-browser directly over the Chrome DevTools Protocol
(CDP) using pure-python websockets — NO Playwright (Playwright ships glibc-only
binaries that will not install or run on Alpine/musl).

Runs INSIDE each per-user OpenCode container. External API is unchanged:
  - POST /browse        : navigate to a query/URL (called by tavily-search.js)
  - WS   /ws/stream     : CDP screencast frames (JPEG base64) to the frontend panel
  - WS   /ws/control    : pause/resume + user takeover (mouse/keyboard)
  - GET  /healthz       : liveness

How it works:
  * On first use, launch chromium-browser headless with --remote-debugging-port.
  * Discover the page target's WebSocket debugger URL via the /json HTTP endpoint.
  * Open ONE CDP websocket; send Page.navigate, Page.startScreencast,
    Input.dispatchMouseEvent / dispatchKeyEvent.
  * Forward screencast frames to all connected /ws/stream clients.
  * Single browser, reused across searches (warm). Idle-parked to free RAM.

Env:
  BRIDGE_HOST            (default 0.0.0.0)
  BRIDGE_PORT            (default 8088)
  BRIDGE_CHROMIUM_PATH   (default /usr/bin/chromium-browser)
  BRIDGE_CDP_PORT        (default 9222)
  BRIDGE_FPS             (default 8)
  BRIDGE_JPEG_QUALITY    (default 60)
  BRIDGE_IDLE_PARK_SECONDS (default 600)
"""

import asyncio
import contextlib
import json
import os
import time
from typing import Optional, Set
from urllib.parse import quote_plus

import aiohttp
from aiohttp import web, WSMsgType
import websockets

HOST = os.environ.get("BRIDGE_HOST", "0.0.0.0")
PORT = int(os.environ.get("BRIDGE_PORT", "8088"))
CHROMIUM = os.environ.get("BRIDGE_CHROMIUM_PATH", "/usr/bin/chromium-browser")
CDP_PORT = int(os.environ.get("BRIDGE_CDP_PORT", "9222"))
FPS = int(os.environ.get("BRIDGE_FPS", "8"))
JPEG_QUALITY = int(os.environ.get("BRIDGE_JPEG_QUALITY", "60"))
IDLE_PARK_SECONDS = int(os.environ.get("BRIDGE_IDLE_PARK_SECONDS", "600"))

VIEWPORT_W = 1280
VIEWPORT_H = 800


class CDP:
    """Minimal Chrome DevTools Protocol client over a single websocket."""

    def __init__(self, ws_url: str) -> None:
        self._ws_url = ws_url
        self._ws = None
        self._id = 0
        self._pending = {}
        self._event_cb = None  # callable(method, params)
        self._reader = None

    async def connect(self) -> None:
        self._ws = await websockets.connect(self._ws_url, max_size=None, ping_interval=None)
        self._reader = asyncio.create_task(self._read_loop())

    async def close(self) -> None:
        if self._reader:
            self._reader.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await self._reader
        if self._ws:
            with contextlib.suppress(Exception):
                await self._ws.close()
        self._ws = None

    def on_event(self, cb) -> None:
        self._event_cb = cb

    async def send(self, method: str, params: Optional[dict] = None) -> dict:
        if not self._ws:
            raise RuntimeError("CDP not connected")
        self._id += 1
        mid = self._id
        fut = asyncio.get_event_loop().create_future()
        self._pending[mid] = fut
        await self._ws.send(json.dumps({"id": mid, "method": method, "params": params or {}}))
        return await asyncio.wait_for(fut, timeout=45)

    def send_nowait(self, method: str, params: Optional[dict] = None) -> None:
        """Fire-and-forget (e.g. screencast ack) — no response awaited."""
        if not self._ws:
            return
        self._id += 1
        mid = self._id
        asyncio.create_task(self._ws.send(json.dumps({"id": mid, "method": method, "params": params or {}})))

    async def _read_loop(self) -> None:
        try:
            async for raw in self._ws:
                msg = json.loads(raw)
                mid = msg.get("id")
                if mid is not None and mid in self._pending:
                    fut = self._pending.pop(mid)
                    if not fut.done():
                        fut.set_result(msg.get("result", {}))
                elif "method" in msg and self._event_cb:
                    self._event_cb(msg["method"], msg.get("params", {}))
        except (websockets.ConnectionClosed, asyncio.CancelledError):
            pass


class BrowserManager:
    def __init__(self) -> None:
        self._proc = None
        self._cdp: Optional[CDP] = None
        self._lock = asyncio.Lock()
        self._stream_clients: Set[web.WebSocketResponse] = set()
        self._paused = False
        self._screencasting = False
        self._last_activity = time.monotonic()
        self._current_url = "about:blank"

    # ---- lifecycle -------------------------------------------------------
    async def ensure_started(self) -> None:
        async with self._lock:
            self._last_activity = time.monotonic()
            if self._cdp and self._cdp._ws:
                return
            await self._launch_locked()

    async def _launch_locked(self) -> None:
        self._proc = await asyncio.create_subprocess_exec(
            CHROMIUM,
            "--headless=new",
            "--no-sandbox",
            "--disable-dev-shm-usage",
            "--disable-gpu",
            "--hide-scrollbars",
            "--mute-audio",
            f"--remote-debugging-port={CDP_PORT}",
            f"--window-size={VIEWPORT_W},{VIEWPORT_H}",
            "--remote-allow-origins=*",
            "about:blank",
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL,
        )
        ws_url = await self._discover_ws_url()
        self._cdp = CDP(ws_url)
        await self._cdp.connect()
        self._cdp.on_event(self._on_cdp_event)
        await self._cdp.send("Page.enable")
        await self._cdp.send("Runtime.enable")
        await self._cdp.send("DOM.enable")

    async def _discover_ws_url(self) -> str:
        last_err = None
        for _ in range(50):  # ~10s
            try:
                async with aiohttp.ClientSession() as s:
                    async with s.get(f"http://127.0.0.1:{CDP_PORT}/json") as r:
                        targets = await r.json()
                        for t in targets:
                            if t.get("type") == "page" and t.get("webSocketDebuggerUrl"):
                                return t["webSocketDebuggerUrl"]
            except Exception as e:  # noqa
                last_err = e
            await asyncio.sleep(0.2)
        raise RuntimeError(f"could not find chromium page target: {last_err}")

    async def park(self) -> None:
        async with self._lock:
            await self._stop_screencast_locked()
            if self._cdp:
                await self._cdp.close()
                self._cdp = None
            if self._proc:
                with contextlib.suppress(Exception):
                    self._proc.terminate()
                    await asyncio.wait_for(self._proc.wait(), timeout=5)
                self._proc = None

    async def shutdown(self) -> None:
        await self.park()

    # ---- navigation ------------------------------------------------------
    async def browse(self, url: str) -> dict:
        await self.ensure_started()
        self._last_activity = time.monotonic()
        assert self._cdp
        await self._cdp.send("Page.navigate", {"url": url})
        await asyncio.sleep(1.2)  # let it render for the screencast
        self._current_url = url
        title = ""
        with contextlib.suppress(Exception):
            res = await self._cdp.send("Runtime.evaluate", {"expression": "document.title", "returnByValue": True})
            title = (res.get("result") or {}).get("value") or ""
        return {"ok": True, "url": url, "title": title, "status": None}

    async def search(self, query: str) -> dict:
        return await self.browse(f"https://duckduckgo.com/?q={quote_plus(query)}")

    async def extract_text(self) -> str:
        await self.ensure_started()
        assert self._cdp
        with contextlib.suppress(Exception):
            res = await self._cdp.send("Runtime.evaluate", {
                "expression": "document.body ? document.body.innerText : ''",
                "returnByValue": True,
            })
            return (res.get("result") or {}).get("value") or ""
        return ""

    # ---- screencast ------------------------------------------------------
    async def add_stream_client(self, ws: web.WebSocketResponse) -> None:
        self._stream_clients.add(ws)
        await self.ensure_started()
        await self._start_screencast()

    async def remove_stream_client(self, ws: web.WebSocketResponse) -> None:
        self._stream_clients.discard(ws)
        if not self._stream_clients:
            await self._stop_screencast()

    async def _start_screencast(self) -> None:
        async with self._lock:
            if self._screencasting or not self._cdp:
                return
            self._screencasting = True
            self._paused = False
            # everyNthFrame=1: Chrome only emits a frame when the page actually
            # changes visually, so this does not waste bandwidth — it just avoids
            # artificially dropping frames on low-change pages (which left the
            # panel blank). FPS is effectively capped by real visual change rate.
            await self._cdp.send("Page.startScreencast", {
                "format": "jpeg",
                "quality": JPEG_QUALITY,
                "maxWidth": VIEWPORT_W,
                "maxHeight": VIEWPORT_H,
                "everyNthFrame": 1,
            })

    async def _stop_screencast(self) -> None:
        async with self._lock:
            await self._stop_screencast_locked()

    async def _stop_screencast_locked(self) -> None:
        if not self._screencasting or not self._cdp:
            self._screencasting = False
            return
        with contextlib.suppress(Exception):
            await self._cdp.send("Page.stopScreencast")
        self._screencasting = False

    def _on_cdp_event(self, method: str, params: dict) -> None:
        if method != "Page.screencastFrame":
            return
        session_id = params.get("sessionId")
        if self._cdp and session_id is not None:
            self._cdp.send_nowait("Page.screencastFrameAck", {"sessionId": session_id})
        if self._paused:
            return
        data = params.get("data")
        if not data:
            return
        payload = json.dumps({"type": "frame", "data": data})
        for ws in list(self._stream_clients):
            if not ws.closed:
                asyncio.create_task(self._safe_send(ws, payload))

    async def _safe_send(self, ws: web.WebSocketResponse, payload: str) -> None:
        with contextlib.suppress(Exception):
            await ws.send_str(payload)

    def set_paused(self, paused: bool) -> None:
        self._paused = paused

    # ---- input / takeover ------------------------------------------------
    async def dispatch_mouse(self, ev: dict) -> None:
        if not self._cdp:
            return
        self._last_activity = time.monotonic()
        with contextlib.suppress(Exception):
            await self._cdp.send("Input.dispatchMouseEvent", {
                "type": ev.get("eventType", "mouseMoved"),
                "x": ev.get("x", 0),
                "y": ev.get("y", 0),
                "button": ev.get("button", "none"),
                "clickCount": ev.get("clickCount", 0),
                "deltaX": ev.get("deltaX", 0),
                "deltaY": ev.get("deltaY", 0),
            })

    async def dispatch_key(self, ev: dict) -> None:
        if not self._cdp:
            return
        self._last_activity = time.monotonic()
        with contextlib.suppress(Exception):
            await self._cdp.send("Input.dispatchKeyEvent", {
                "type": ev.get("eventType", "keyDown"),
                "key": ev.get("key", ""),
                "text": ev.get("text", ""),
                "code": ev.get("code", ""),
            })

    # ---- idle parking ----------------------------------------------------
    async def idle_watch(self) -> None:
        while True:
            await asyncio.sleep(30)
            if self._stream_clients:
                continue
            if self._cdp is None:
                continue
            if time.monotonic() - self._last_activity > IDLE_PARK_SECONDS:
                await self.park()


manager = BrowserManager()


# ---- HTTP / WS handlers --------------------------------------------------
async def handle_health(_req: web.Request) -> web.Response:
    return web.json_response({"ok": True, "fps": FPS})


async def handle_browse(req: web.Request) -> web.Response:
    try:
        body = await req.json()
    except Exception:
        return web.json_response({"ok": False, "error": "invalid json"}, status=400)

    url = body.get("url")
    query = body.get("query")
    extract = bool(body.get("extract", False))
    if not url and not query:
        return web.json_response({"ok": False, "error": "url or query required"}, status=400)

    # Fire-and-forget mode (default): return immediately and navigate in the
    # background. The search tool (tavily-search.js) calls this purely to update
    # the live panel and must NEVER block — otherwise rapid searches serialize on
    # the browser lock and the agent's tool call hits "upstream idle timeout".
    # Only when extract=true (caller wants the page text back) do we await.
    if not extract:
        async def _bg():
            try:
                if url:
                    await manager.browse(url)
                else:
                    await manager.search(query)
            except Exception:
                pass  # never surface background nav errors
        asyncio.create_task(_bg())
        return web.json_response({"ok": True, "queued": True})

    try:
        if url:
            result = await manager.browse(url)
        else:
            result = await manager.search(query)
        result["text"] = (await manager.extract_text())[:20000]
        return web.json_response(result)
    except Exception as e:  # noqa
        return web.json_response({"ok": False, "error": str(e)}, status=500)


async def handle_stream(req: web.Request) -> web.WebSocketResponse:
    ws = web.WebSocketResponse(heartbeat=20, max_msg_size=0)
    await ws.prepare(req)
    await manager.add_stream_client(ws)
    try:
        async for msg in ws:
            if msg.type == WSMsgType.TEXT:
                try:
                    data = json.loads(msg.data)
                except Exception:
                    continue
                action = data.get("action")
                if action == "pause":
                    manager.set_paused(True)
                elif action in ("resume", "play", "live"):
                    manager.set_paused(False)
            elif msg.type == WSMsgType.ERROR:
                break
    finally:
        await manager.remove_stream_client(ws)
    return ws


async def handle_control(req: web.Request) -> web.WebSocketResponse:
    ws = web.WebSocketResponse(heartbeat=20)
    await ws.prepare(req)
    try:
        async for msg in ws:
            if msg.type != WSMsgType.TEXT:
                continue
            try:
                ev = json.loads(msg.data)
            except Exception:
                continue
            kind = ev.get("kind")
            if kind == "mouse":
                await manager.dispatch_mouse(ev)
            elif kind == "key":
                await manager.dispatch_key(ev)
    finally:
        pass
    return ws


async def on_startup(app: web.Application) -> None:
    app["idle_task"] = asyncio.create_task(manager.idle_watch())


async def on_cleanup(app: web.Application) -> None:
    task = app.get("idle_task")
    if task:
        task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await task
    await manager.shutdown()


def make_app() -> web.Application:
    app = web.Application()
    app.router.add_get("/healthz", handle_health)
    app.router.add_post("/browse", handle_browse)
    app.router.add_get("/ws/stream", handle_stream)
    app.router.add_get("/ws/control", handle_control)
    app.on_startup.append(on_startup)
    app.on_cleanup.append(on_cleanup)
    return app


if __name__ == "__main__":
    web.run_app(make_app(), host=HOST, port=PORT)
