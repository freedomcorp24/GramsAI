// components/chat-files-strip.tsx
// Claude-style inline file strip. Fed the chat's changed-files list (the same
// data the right-hand Review panel uses), each row has View (opens OpenCode's
// in-app viewer) and Download (saves the file). Dependency-light: solid-js +
// v2 token classes; all data/actions come in via props.
import { Component, createSignal, For, Show } from "solid-js"

export const ChatFilesStrip: Component<{
  files: () => string[]
  onView: (path: string) => void
  onDownload: (path: string) => void
}> = (props) => {
  const [open, setOpen] = createSignal(true)
  const base = (p: string) => p.split("/").pop() || p

  return (
    <Show when={props.files().length > 0}>
      <div class="mx-auto w-full max-w-3xl px-4 pb-2">
        <div class="rounded-lg border border-v2-border-border-muted bg-v2-background-bg-base">
          <button
            type="button"
            class="flex w-full items-center gap-2 px-3 py-2 text-[13px] font-medium text-v2-text-text-base"
            onClick={() => setOpen((v) => !v)}
          >
            <span>Files</span>
            <span class="text-[11px] text-v2-text-text-muted">{props.files().length}</span>
          </button>
          <Show when={open()}>
            <div class="max-h-56 overflow-y-auto border-t border-v2-border-border-muted">
              <For each={props.files()}>
                {(p) => (
                  <div class="flex items-center gap-3 px-3 py-1.5 hover:bg-v2-overlay-simple-overlay-hover">
                    <button
                      type="button"
                      class="min-w-0 flex-1 truncate text-left text-[13px] text-v2-text-text-base"
                      title={p}
                      onClick={() => props.onView(p)}
                    >
                      {base(p)}
                    </button>
                    <button
                      type="button"
                      class="shrink-0 text-[12px] text-v2-text-text-muted hover:text-v2-text-text-base"
                      onClick={() => props.onView(p)}
                    >
                      View
                    </button>
                    <button
                      type="button"
                      class="shrink-0 text-[12px] text-v2-text-text-muted hover:text-v2-text-text-base"
                      onClick={() => props.onDownload(p)}
                    >
                      Download
                    </button>
                  </div>
                )}
              </For>
            </div>
          </Show>
        </div>
      </div>
    </Show>
  )
}
