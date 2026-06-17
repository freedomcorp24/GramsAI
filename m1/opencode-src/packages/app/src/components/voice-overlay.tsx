import { createEffect, createSignal, on, onCleanup, onMount, Show } from "solid-js"
import { MicVAD } from "@ricky0123/vad-web"
import * as ort from "onnxruntime-web"

type VoiceState = "idle" | "listening" | "transcribing" | "thinking" | "speaking"

export interface VoiceOverlayProps {
  open: boolean
  onClose: () => void
  sendToSession: (text: string, onChunk?: (sentence: string) => void) => Promise<string>
  voice?: string
}

function floatToWav(samples: Float32Array, sampleRate = 16000): Blob {
  const buffer = new ArrayBuffer(44 + samples.length * 2)
  const view = new DataView(buffer)
  const w = (off: number, s: string) => { for (let i = 0; i < s.length; i++) view.setUint8(off + i, s.charCodeAt(i)) }
  w(0, "RIFF"); view.setUint32(4, 36 + samples.length * 2, true); w(8, "WAVE")
  w(12, "fmt "); view.setUint32(16, 16, true); view.setUint16(20, 1, true)
  view.setUint16(22, 1, true); view.setUint32(24, sampleRate, true)
  view.setUint32(28, sampleRate * 2, true); view.setUint16(32, 2, true); view.setUint16(34, 16, true)
  w(36, "data"); view.setUint32(40, samples.length * 2, true)
  let off = 44
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]))
    view.setInt16(off, s < 0 ? s * 0x8000 : s * 0x7fff, true)
    off += 2
  }
  return new Blob([buffer], { type: "audio/wav" })
}

function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onloadend = () => {
      const res = r.result as string
      const c = res.indexOf(",")
      resolve(c >= 0 ? res.slice(c + 1) : res)
    }
    r.onerror = reject
    r.readAsDataURL(blob)
  })
}


// Curated Gemini TTS voices: friendly name + trait shown to the user, mapping to
// the real Gemini codename (value) which the gateway validates (speech.go).
const VOICE_OPTIONS: { name: string; trait: string; value: string; gender: "Female" | "Male" }[] = [
  { name: "Nora", trait: "Confident", value: "Kore", gender: "Female" },
  { name: "Aria", trait: "Warm", value: "Aoede", gender: "Female" },
  { name: "Skye", trait: "Bright", value: "Zephyr", gender: "Female" },
  { name: "Lily", trait: "Youthful", value: "Leda", gender: "Female" },
  { name: "Sofia", trait: "Welcoming", value: "Sulafat", gender: "Female" },
  { name: "Ava", trait: "Upbeat", value: "Autonoe", gender: "Female" },
  { name: "Cleo", trait: "Relaxed", value: "Callirrhoe", gender: "Female" },
  { name: "Daria", trait: "Smooth", value: "Despina", gender: "Female" },
  { name: "Elena", trait: "Precise", value: "Erinome", gender: "Female" },
  { name: "Grace", trait: "Mature", value: "Gacrux", gender: "Female" },
  { name: "Layla", trait: "Lively", value: "Laomedeia", gender: "Female" },
  { name: "Paula", trait: "Expressive", value: "Pulcherrima", gender: "Female" },
  { name: "Vera", trait: "Gentle", value: "Vindemiatrix", gender: "Female" },
  { name: "Anna", trait: "Soft", value: "Achernar", gender: "Female" },
  { name: "Carter", trait: "Professional", value: "Charon", gender: "Male" },
  { name: "Oscar", trait: "Decisive", value: "Orus", gender: "Male" },
  { name: "Max", trait: "Energetic", value: "Puck", gender: "Male" },
  { name: "Finn", trait: "Dynamic", value: "Fenrir", gender: "Male" },
  { name: "Adam", trait: "Friendly", value: "Achird", gender: "Male" },
  { name: "Alex", trait: "Smooth", value: "Algieba", gender: "Male" },
  { name: "Leo", trait: "Strong", value: "Alnilam", gender: "Male" },
  { name: "Ethan", trait: "Soft", value: "Enceladus", gender: "Male" },
  { name: "Ian", trait: "Articulate", value: "Iapetus", gender: "Male" },
  { name: "Ryan", trait: "Informative", value: "Rasalgethi", gender: "Male" },
  { name: "Sam", trait: "Animated", value: "Sadachbia", gender: "Male" },
  { name: "Theo", trait: "Authoritative", value: "Sadaltager", gender: "Male" },
  { name: "Gabe", trait: "Gravelly", value: "Algenib", gender: "Male" },
  { name: "Seth", trait: "Steady", value: "Schedar", gender: "Male" },
  { name: "Owen", trait: "Easy-going", value: "Umbriel", gender: "Male" },
  { name: "Zane", trait: "Casual", value: "Zubenelgenubi", gender: "Male" },
]

export function VoiceOverlay(props: VoiceOverlayProps) {
  const [state, setState] = createSignal<VoiceState>("idle")
  const [amplitude, setAmplitude] = createSignal(0)
  const [error, setError] = createSignal<string | null>(null)
  const [replyText, setReplyText] = createSignal("")
  const [selectedVoice, setSelectedVoice] = createSignal(props.voice || "Kore")
  const [voiceMenuOpen, setVoiceMenuOpen] = createSignal(false)

  // The MicVAD instance + audio graph live for the WHOLE component lifetime.
  // We create them ONCE (lazily on first open) and toggle with start()/pause().
  // We only destroy() on real unmount. This is the library's intended lifecycle
  // (real-time-vad: start/pause/destroy) and avoids recreate-on-toggle, which
  // caused mid-turn teardown + freeze-on-reopen.
  let vad: MicVAD | null = null
  let audioCtx: AudioContext | null = null
  let currentSource: AudioBufferSourceNode | null = null
  let active = false          // overlay currently open + listening
  let unmounted = false       // component truly torn down
  let busy = false            // a turn is in flight
  let playing = false
  let cancelSpeak = false
  let micAnalyser: AnalyserNode | null = null

  const stopPlayback = () => {
    cancelSpeak = true
    try { currentSource?.stop() } catch {}
    currentSource = null
    playing = false
  }

  const speak = async (text: string) => {
    if (!text.trim() || !audioCtx) return
    const ctx = audioCtx
    try { if (ctx.state === "suspended") await ctx.resume() } catch {}
    cancelSpeak = false
    const resp = await fetch("/api/audio/speech", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ text, voice: selectedVoice() }),
    })
    console.log("[voice] TTS status:", resp.status)
    if (!resp.ok || cancelSpeak || unmounted) return
    const ctype = resp.headers.get("Content-Type") || ""
    const bytes = await resp.arrayBuffer()
    let buf: AudioBuffer
    if (ctype.includes("pcm")) {
      const mm = /rate=(\d+)/.exec(ctype)
      const rate = mm ? parseInt(mm[1]) : 24000
      const view = new DataView(bytes)
      const n = Math.floor(bytes.byteLength / 2)
      buf = ctx.createBuffer(1, n, rate)
      const ch = buf.getChannelData(0)
      for (let i = 0; i < n; i++) ch[i] = view.getInt16(i * 2, true) / 0x8000
    } else {
      buf = await ctx.decodeAudioData(bytes.slice(0))
    }
    if (cancelSpeak || unmounted) return
    const src = ctx.createBufferSource()
    src.buffer = buf
    const analyser = ctx.createAnalyser()
    analyser.fftSize = 256
    src.connect(analyser)
    analyser.connect(ctx.destination)
    const data = new Uint8Array(analyser.frequencyBinCount)
    const tick = () => {
      if (!playing) return
      analyser.getByteTimeDomainData(data)
      let peak = 0
      for (let i = 0; i < data.length; i++) peak = Math.max(peak, Math.abs(data[i] - 128))
      setAmplitude(Math.min(1, peak / 90))
      requestAnimationFrame(tick)
    }
    currentSource = src
    playing = true
    setState("speaking")
    tick()
    await new Promise<void>((resolve) => { src.onended = () => resolve(); src.start() })
    playing = false
    setAmplitude(0)
  }

  const handleUtterance = async (samples: Float32Array) => {
    if (!active || unmounted || busy) return
    busy = true
    try {
      setState("transcribing")
      const wav = floatToWav(samples, 16000)
      const b64 = await blobToBase64(wav)
      const sttResp = await fetch("/api/audio/transcribe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({ audio: b64, format: "wav" }),
      })
      if (!sttResp.ok) { if (active) setState("listening"); return }
      const { text } = await sttResp.json()
      console.log("[voice] transcribed:", JSON.stringify(text))
      if (!text || !text.trim()) { if (active) setState("listening"); return }

      setReplyText("")
      setState("thinking")
      const reply = await props.sendToSession(text.trim())
      console.log("[voice] reply len:", reply.length)
      if (active && !unmounted && reply && reply.trim()) {
        setReplyText(reply)
        await speak(reply)
      }
      if (active && !unmounted) setState("listening")
    } catch (e) {
      console.error("[voice] turn error", e)
      if (active) setState("listening")
    } finally {
      busy = false
    }
  }

  // Create the VAD + audio graph ONCE. Safe to call repeatedly (no-op if exists).
  const ensureVad = async () => {
    if (vad) return
    try {
      ort.env.wasm.wasmPaths = "/vad/"
      ort.env.wasm.numThreads = 1
      ort.env.wasm.simd = true
    } catch {}
    audioCtx = new (window.AudioContext || (window as any).webkitAudioContext)()
    try { if (audioCtx.state === "suspended") await audioCtx.resume() } catch {}

    vad = await MicVAD.new({
      baseAssetPath: "/vad/",
      onnxWASMBasePath: "/vad/",
      positiveSpeechThreshold: 0.85,
      negativeSpeechThreshold: 0.5,
      minSpeechFrames: 6,
      redemptionFrames: 12,
      onSpeechStart: () => {
        if (!active) return
        if (playing) stopPlayback()
        if (!busy) setState("listening")
      },
      onSpeechEnd: (audio: Float32Array) => {
        if (active) void handleUtterance(audio)
      },
    })

    // mic-amplitude meter for the visualizer (taps the VAD's own stream)
    try {
      const stream = (vad as any).stream as MediaStream | undefined
      if (stream && audioCtx) {
        const srcNode = audioCtx.createMediaStreamSource(stream)
        micAnalyser = audioCtx.createAnalyser()
        micAnalyser.fftSize = 256
        srcNode.connect(micAnalyser)
        const md = new Uint8Array(micAnalyser.frequencyBinCount)
        const micTick = () => {
          if (unmounted) return
          if (active && state() === "listening" && micAnalyser) {
            micAnalyser.getByteTimeDomainData(md)
            let peak = 0
            for (let i = 0; i < md.length; i++) peak = Math.max(peak, Math.abs(md[i] - 128))
            setAmplitude(Math.min(1, peak / 70))
          }
          requestAnimationFrame(micTick)
        }
        requestAnimationFrame(micTick)
      }
    } catch {}
  }

  const openLoop = async () => {
    setError(null)
    setReplyText("")
    cancelSpeak = false
    busy = false
    try {
      await ensureVad()
      active = true
      try { if (audioCtx && audioCtx.state === "suspended") await audioCtx.resume() } catch {}
      await vad!.start()
      setState("listening")
    } catch (e: any) {
      console.error("[voice] start failed:", e)
      setError("Voice init failed: " + (e?.message || String(e)))
      setState("idle")
    }
  }

  const closeLoop = async () => {
    active = false
    stopPlayback()
    setState("idle")
    // PAUSE (not destroy) — keeps the instance + worklet alive for instant reopen.
    try { await vad?.pause() } catch {}
    // suspend audio so the mic indicator goes quiet between sessions
    try { if (audioCtx && audioCtx.state === "running") await audioCtx.suspend() } catch {}
  }

  // React to open/close ONLY (on() => explicit single dependency). Never recreate.
  createEffect(on(() => props.open, (isOpen) => {
    if (isOpen) void openLoop()
    else void closeLoop()
  }))

  onMount(() => { if (props.open) void openLoop() })

  // Real teardown: destroy the instance + release the mic for good.
  onCleanup(() => {
    unmounted = true
    active = false
    stopPlayback()
    try { (vad as any)?.stream?.getTracks?.().forEach((t: MediaStreamTrack) => t.stop()) } catch {}
    try { vad?.destroy() } catch {}
    try { audioCtx?.close() } catch {}
    vad = null
    audioCtx = null
  })

  const close = () => { void closeLoop(); props.onClose() }

  const currentVoiceLabel = () => {
    const v = VOICE_OPTIONS.find((o) => o.value === selectedVoice())
    return v ? `${v.name} · ${v.trait}` : selectedVoice()
  }

  const statusText = () => {
    switch (state()) {
      case "listening": return "Listening\u2026"
      case "transcribing": return "\u2026"
      case "thinking": return "Thinking\u2026"
      case "speaking": return "Speaking\u2026"
      default: return "Tap to start"
    }
  }

  const BARS = 24
  const barHeight = (i: number) => {
    const amp = amplitude()
    const act = state() === "listening" || state() === "speaking"
    const base = act ? 0.12 : 0.06
    const center = 1 - Math.abs(i - (BARS - 1) / 2) / ((BARS - 1) / 2)
    const wobble = 0.5 + 0.5 * Math.sin(Date.now() / 120 + i * 0.7)
    const h = base + amp * (0.25 + 0.75 * center) * (0.6 + 0.4 * wobble)
    return Math.max(0.05, Math.min(1, h))
  }

  const [, setTick] = createSignal(0)
  onMount(() => {
    let raf = 0
    const loop = () => { if (unmounted) return; setTick((t) => (t + 1) % 100000); raf = requestAnimationFrame(loop) }
    raf = requestAnimationFrame(loop)
    onCleanup(() => cancelAnimationFrame(raf))
  })

  return (
    <Show when={props.open}>
      <div
        data-component="voice-overlay"
        class="fixed inset-0 z-[9999] flex flex-col items-center justify-center"
        style={{ background: "rgba(16,16,16,0.92)", "backdrop-filter": "blur(8px)" }}
      >
        <button
          type="button"
          onClick={close}
          aria-label="Close voice"
          class="absolute right-5 top-5 flex size-10 items-center justify-center rounded-full text-white/70 hover:text-white"
          style={{ background: "rgba(255,255,255,0.06)" }}
        >
          <svg width="20" height="20" viewBox="0 0 20 20"><path d="M5 5l10 10M15 5L5 15" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>
        </button>

        {/* Voice picker */}
        <div class="absolute left-5 top-5">
          <button
            type="button"
            onClick={() => setVoiceMenuOpen((v) => !v)}
            class="flex items-center gap-2 rounded-full px-4 py-2 text-[13px] font-medium text-white/85 hover:text-white"
            style={{ background: "rgba(255,255,255,0.06)", border: "1px solid rgba(255,255,255,0.1)" }}
          >
            <span style={{ color: "#3fb950" }}>●</span>
            {currentVoiceLabel()}
            <svg width="12" height="12" viewBox="0 0 20 20"><path d="M5 7l5 5 5-5" stroke="currentColor" stroke-width="1.8" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
          <Show when={voiceMenuOpen()}>
            <div
              class="absolute left-0 top-[110%] z-10 max-h-[60vh] w-64 overflow-y-auto rounded-xl p-1"
              style={{ background: "#181818", border: "1px solid rgba(255,255,255,0.12)", "box-shadow": "0 16px 40px -12px rgba(0,0,0,0.7)" }}
            >
              <div class="px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-white/40">Female</div>
              {VOICE_OPTIONS.filter((o) => o.gender === "Female").map((o) => (
                <button
                  type="button"
                  onClick={() => { setSelectedVoice(o.value); setVoiceMenuOpen(false) }}
                  class="flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-[14px] hover:bg-white/8"
                  style={{ color: o.value === selectedVoice() ? "#3fb950" : "rgba(255,255,255,0.9)" }}
                >
                  <span>{o.name}</span>
                  <span class="text-[12px] text-white/45">{o.trait}</span>
                </button>
              ))}
              <div class="mt-1 px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-white/40">Male</div>
              {VOICE_OPTIONS.filter((o) => o.gender === "Male").map((o) => (
                <button
                  type="button"
                  onClick={() => { setSelectedVoice(o.value); setVoiceMenuOpen(false) }}
                  class="flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-[14px] hover:bg-white/8"
                  style={{ color: o.value === selectedVoice() ? "#3fb950" : "rgba(255,255,255,0.9)" }}
                >
                  <span>{o.name}</span>
                  <span class="text-[12px] text-white/45">{o.trait}</span>
                </button>
              ))}
            </div>
          </Show>
        </div>

        <div class="flex h-40 items-center justify-center gap-[3px]">
          {Array.from({ length: BARS }).map((_, i) => (
            <div
              style={{
                width: "5px",
                "border-radius": "3px",
                height: `${barHeight(i) * 140}px`,
                background: state() === "speaking" ? "#3fb950" : "#e8e8e8",
                opacity: state() === "idle" ? "0.3" : "0.95",
                transition: "height 90ms ease-out, background 200ms",
              }}
            />
          ))}
        </div>

        <div class="mt-8 text-[15px] font-medium tracking-wide text-white/80">{statusText()}</div>

        <Show when={replyText()}>
          <div class="mt-5 max-h-[30vh] max-w-[min(640px,86vw)] overflow-y-auto px-4 text-center text-[17px] leading-relaxed text-white/90">
            {replyText()}
          </div>
        </Show>

        <Show when={error()}>
          <div class="mt-3 text-[13px] text-red-400">{error()}</div>
        </Show>

        <button
          type="button"
          onClick={() => { if (state() === "idle") void openLoop() }}
          class="mt-10 flex size-16 items-center justify-center rounded-full"
          style={{ background: "#3fb950", color: "#04210c", "box-shadow": "0 10px 30px -8px rgba(63,185,80,0.6)" }}
          aria-label="Microphone"
        >
          <svg width="26" height="26" viewBox="0 0 20 20">
            <path d="M10 12.5a2.5 2.5 0 0 0 2.5-2.5V5a2.5 2.5 0 0 0-5 0v5a2.5 2.5 0 0 0 2.5 2.5Z" fill="currentColor"/>
            <path d="M15 9.17V10a5 5 0 0 1-10 0v-.83M10 15v2.5M7.5 17.5h5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" fill="none"/>
          </svg>
        </button>
      </div>
    </Show>
  )
}
