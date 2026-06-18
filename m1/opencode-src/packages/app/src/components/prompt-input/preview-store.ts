// Ephemeral, NON-persisted stores for upload UI state (preview URL + progress %).
// Preview uses URL.createObjectURL (synchronous, instant, no base64, no file read)
// instead of FileReader.readAsDataURL: readAsDataURL loads the whole file into the JS
// heap and expands it ~33% as base64, then the <img> must DECODE that multi-MB string --
// doing that for several multi-MB images freezes the main thread (worst in an existing
// chat where the timeline/sync already compete for it). Object URLs are 10-100x cheaper.
// Revoke on drop to avoid leaks. Neither store is persisted or part of the prompt.
import { createStore } from "solid-js/store"

const previews = new Map<string, string>()
export function setPreview(id: string, url: string) { previews.set(id, url) }
export function getPreview(id: string): string { return previews.get(id) ?? "" }
export function dropPreview(id: string) {
  const u = previews.get(id)
  if (u && u.startsWith("blob:")) URL.revokeObjectURL(u)
  previews.delete(id)
}

const [progress, setProgressStore] = createStore<Record<string, number>>({})
export function setProgress(id: string, pct: number) { setProgressStore(id, pct) }
export function getProgress(id: string): number { const v = progress[id]; return v === undefined ? 100 : v }
export function dropProgress(id: string) { setProgressStore(id, undefined as unknown as number) }
