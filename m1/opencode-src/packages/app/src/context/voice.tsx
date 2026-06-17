import { createSimpleContext } from "@opencode-ai/ui/context"
import { createSignal } from "solid-js"

// Voice chat lives ABOVE the router (mounted in the persistent app shell) so it
// survives route navigation. The live session composer registers its current
// session-bound sender here; the persistent overlay calls whatever is registered.
//
// activeSession: the session this voice conversation is bound to. Set when a
// voice turn creates a session from the home screen, so subsequent turns reuse
// it instead of spawning a new chat each time. Cleared when the overlay closes.
//
// pendingSession: the full route path to navigate to once the overlay closes
// (we never navigate mid-turn — that would remount the composer).
export const { use: useVoice, provider: VoiceProvider } = createSimpleContext({
  name: "Voice",
  init: () => {
    const [open, setOpen] = createSignal(false)
    const [pendingSession, setPendingSession] = createSignal<string | undefined>(undefined)
    const [activeSession, setActiveSession] = createSignal<string | undefined>(undefined)
    let sender: ((text: string, onChunk?: (s: string) => void) => Promise<string>) | undefined
    return {
      open,
      show: () => setOpen(true),
      hide: () => setOpen(false),
      pendingSession,
      setPendingSession: (id: string | undefined) => setPendingSession(id),
      activeSession,
      setActiveSession: (id: string | undefined) => setActiveSession(id),
      registerSender: (fn: ((text: string, onChunk?: (s: string) => void) => Promise<string>) | undefined) => { sender = fn },
      send: (text: string, onChunk?: (s: string) => void) => (sender ? sender(text, onChunk) : Promise.resolve("")),
      hasSender: () => !!sender,
    }
  },
})
