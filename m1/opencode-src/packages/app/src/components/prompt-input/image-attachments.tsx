import { getPreview, getProgress } from "./preview-store"
import { Component, For, Show } from "solid-js"
import { Icon } from "@opencode-ai/ui/icon"
import { Tooltip } from "@opencode-ai/ui/tooltip"
import type { ImageAttachmentPart } from "@/context/prompt"

type PromptImageAttachmentsProps = {
  attachments: ImageAttachmentPart[]
  onOpen: (attachment: ImageAttachmentPart) => void
  onRemove: (id: string) => void
  removeLabel: string
}

const fallbackClass = "size-16 rounded-md bg-surface-base flex items-center justify-center border border-border-base"
const imageClass =
  "size-16 rounded-md object-cover border border-border-base hover:border-border-strong-base transition-colors"
const removeClass =
  "absolute -top-1.5 -right-1.5 size-5 rounded-full bg-surface-raised-stronger-non-alpha border border-border-base flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity hover:bg-surface-raised-base-hover"
const nameClass = "absolute bottom-0 left-0 right-0 px-1 py-0.5 bg-black/50 rounded-b-md"
const uploadingOverlayClass =
  "absolute inset-0 rounded-md bg-black/60 flex flex-col items-center justify-center gap-1"
const progressTrackClass = "w-12 h-1 rounded-full bg-white/25 overflow-hidden"
const progressFillClass = "h-full bg-[#3fb950] transition-[width] duration-150"

export const PromptImageAttachments: Component<PromptImageAttachmentsProps> = (props) => {
  return (
    <Show when={props.attachments.length > 0}>
      <div class="flex flex-wrap gap-2 px-3 pt-3">
        <For each={props.attachments}>
          {(attachment) => (
            <Tooltip value={attachment.filename} placement="top" contentClass="break-all">
              <div class="relative group">
                <Show
                  when={attachment.mime.startsWith("image/")}
                  fallback={
                    <div class={fallbackClass}>
                      <Icon name="folder" class="size-6 text-text-weak" />
                    </div>
                  }
                >
                  <img
                    src={getPreview(attachment.id)}
                    alt={attachment.filename}
                    class={imageClass}
                    onClick={() => props.onOpen(attachment)}
                  />
                </Show>
                <button
                  type="button"
                  onClick={() => props.onRemove(attachment.id)}
                  class={removeClass}
                  aria-label={props.removeLabel}
                >
                  <Icon name="close" class="size-3 text-text-weak" />
                </button>
                <Show when={getProgress(attachment.id) < 100}>
                  <div class={uploadingOverlayClass}>
                    <div class={progressTrackClass}>
                      <div class={progressFillClass} style={{ width: `${getProgress(attachment.id)}%` }} />
                    </div>
                    <span class="text-10-regular text-white">{getProgress(attachment.id)}%</span>
                  </div>
                </Show>
                <div class={nameClass}>
                  <span class="text-10-regular text-white truncate block">{attachment.filename}</span>
                </div>
              </div>
            </Tooltip>
          )}
        </For>
      </div>
    </Show>
  )
}
