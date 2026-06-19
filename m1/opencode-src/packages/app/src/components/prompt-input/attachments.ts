import { setPreview, dropPreview, setProgress, dropProgress } from "./preview-store"
import { createSignal, onMount } from "solid-js"
import { makeEventListener } from "@solid-primitives/event-listener"
import { showToast } from "@/utils/toast"
import { usePrompt, type ContentPart, type ImageAttachmentPart } from "@/context/prompt"
import { useLanguage } from "@/context/language"
import { uuid } from "@/utils/uuid"
import { getCursorPosition } from "./editor-dom"
import { attachmentMime } from "./files"
import { normalizePaste, pasteMode } from "./paste"

function dataUrl(file: File, mime: string) {
  return new Promise<string>((resolve) => {
    const reader = new FileReader()
    reader.addEventListener("error", () => resolve(""))
    reader.addEventListener("load", () => {
      const value = typeof reader.result === "string" ? reader.result : ""
      const idx = value.indexOf(",")
      if (idx === -1) {
        resolve(value)
        return
      }
      resolve(`data:${mime};base64,${value.slice(idx + 1)}`)
    })
    reader.readAsDataURL(file)
  })
}

type PromptAttachmentsInput = {
  editor: () => HTMLDivElement | undefined
  isDialogActive: () => boolean
  setDraggingType: (type: "image" | "@mention" | null) => void
  focusEditor: () => void
  addPart: (part: ContentPart) => boolean
  readClipboardImage?: () => Promise<File | null>
}

// Serial upload queue: only ONE /upload XHR runs at a time so concurrent
// large uploads never saturate the browser's per-host connection pool
// (which previously left uploads Queued for ~60s then timing out).
let uploadChain: Promise<void> = Promise.resolve()
function enqueueUpload(run: () => Promise<void>): Promise<void> {
  const next = uploadChain.then(run, run)
  uploadChain = next.catch(() => {})
  return next
}

export function createPromptAttachments(input: PromptAttachmentsInput) {
  const prompt = usePrompt()
  const language = useLanguage()
  const [pendingCount, setPendingCount] = createSignal(0)
  const isUploading = () =>
    pendingCount() > 0 ||
    prompt.current().some((p) => p.type === "image" && !p.uploadedPath)

  const warn = () => {
    showToast({
      title: language.t("prompt.toast.pasteUnsupported.title"),
      description: language.t("prompt.toast.pasteUnsupported.description"),
    })
  }

  const patchAttachment = (id: string, patch: Partial<ImageAttachmentPart>) => {
    prompt.patch(id, patch)
  }

  // Mirror of the agent's server-side filename sanitizer so the client can
  // reconstruct the saved path when a successful (2xx) upload response body
  // gets cut off by connection churn (NS_BINDING_ABORTED). The file IS on disk
  // in that case; we must not yank the attachment.
  const serverPathFor = (filename: string) => {
    const base = filename.split(/[\\/]/).pop() ?? filename
    const safe = base.replace(/[^a-zA-Z0-9._-]/g, "_")
    return "/workspace/uploads/" + safe
  }
  const uploadToWorkspace = (file: File, id: string) =>
    new Promise<void>((resolve) => {
      const xhr = new XMLHttpRequest()
      // Resolve the session id from the URL (/:dir/session/:id) so the upload lands
      // in THIS chat's worktree, not shared master. Reading our own URL is safe
      // (unlike a server-side Referer, which is client-spoofable).
      const m = window.location.pathname.split("/session/")
      const chat = m.length > 1 ? (m[1].split("/")[0] || "") : ""
      // New-chat fallback: when there is no session id yet, the URL's first path
      // segment is the base64 worktree dir (/<b64dir>/session/...). Decode it and
      // send &dir= so uploads land in THAT worktree, not master /workspace.
      let dir = ""
      if (!chat) {
        try {
          const seg = window.location.pathname.split("/").filter(Boolean)[0] || ""
          if (seg) {
            let b = seg.replace(/-/g, "+").replace(/_/g, "/")
            while (b.length % 4) b += "="
            dir = atob(b)
          }
        } catch {
          dir = ""
        }
      }
      const uploadUrl =
        "/upload?name=" + encodeURIComponent(file.name) +
        (chat ? "&chat=" + encodeURIComponent(chat) : "") +
        (!chat && dir ? "&dir=" + encodeURIComponent(dir) : "")
      xhr.open("POST", uploadUrl, true)
      xhr.withCredentials = true
      xhr.setRequestHeader("Content-Type", "application/octet-stream")
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) {
          setProgress(id, Math.round((e.loaded / e.total) * 100))
        }
      }
      const fail = (why: string) => {
        showToast({ title: "Upload failed", description: why })
        // Clear the stuck attachment so it does not freeze at N% and stays removable.
        dropPreview(id)
        dropProgress(id)
        const current = prompt.current()
        const next = current.filter((part) => part.type !== "image" || part.id !== id)
        prompt.set(next, prompt.cursor())
        resolve()
      }
      xhr.onload = () => {
        if (xhr.status < 200 || xhr.status >= 300) { fail("server returned " + xhr.status); return }
        let res: any = null
        try { res = JSON.parse(xhr.responseText) } catch { res = null }
        if (res && typeof res.path === "string") {
          setProgress(id, 100); patchAttachment(id, { uploadedPath: res.path }); resolve(); return
        }
        if (res && res.error) { fail(String(res.error)); return }
        // 2xx but body empty/unparseable (response cut off mid-stream): the file
        // is on disk server-side. Reconstruct the path instead of falsely failing.
        setProgress(id, 100); patchAttachment(id, { uploadedPath: serverPathFor(file.name) }); resolve()
      }
      xhr.onerror = () => fail("network error")
      xhr.ontimeout = () => fail("timed out")
      xhr.timeout = 120000
      xhr.send(file)
    })

  const add = async (file: File, toast = true) => {
    const mime = await attachmentMime(file)
    if (!mime) {
      if (toast) warn()
      return false
    }
    const editor = input.editor()
    if (!editor) return false
    const url = URL.createObjectURL(file)
    const id = uuid()
    setPreview(id, url)
    setProgress(id, 0)
    const attachment: ImageAttachmentPart = {
      type: "image",
      id,
      filename: file.name,
      mime,
      dataUrl: "",
      uploadProgress: 0,
    }
    const cursor = prompt.cursor() ?? getCursorPosition(editor)
    prompt.set([...prompt.current(), attachment], cursor)
    setPendingCount((n) => n + 1)
    void enqueueUpload(() => uploadToWorkspace(file, id)).finally(() => {
      setPendingCount((n) => Math.max(0, n - 1))
    })
    return true
  }

  const addAttachment = (file: File) => add(file)

  const addAttachments = async (files: File[], toast = true) => {
    let found = false

    for (const file of files) {
      const ok = await add(file, false)
      if (ok) found = true
    }

    if (!found && files.length > 0 && toast) warn()
    return found
  }

  const removeAttachment = (id: string) => {
    dropPreview(id)
    dropProgress(id)
    const current = prompt.current()
    const next = current.filter((part) => part.type !== "image" || part.id !== id)
    prompt.set(next, prompt.cursor())
  }

  const handlePaste = async (event: ClipboardEvent) => {
    const clipboardData = event.clipboardData
    if (!clipboardData) return

    event.preventDefault()
    event.stopPropagation()

    const files = Array.from(clipboardData.items).flatMap((item) => {
      if (item.kind !== "file") return []
      const file = item.getAsFile()
      return file ? [file] : []
    })

    if (files.length > 0) {
      await addAttachments(files)
      return
    }

    const plainText = clipboardData.getData("text/plain") ?? ""

    // Desktop: Browser clipboard has no images and no text, try platform's native clipboard for images
    if (input.readClipboardImage && !plainText) {
      const file = await input.readClipboardImage()
      if (file) {
        await addAttachment(file)
        return
      }
    }

    if (!plainText) return

    const text = normalizePaste(plainText)

    const put = () => {
      if (input.addPart({ type: "text", content: text, start: 0, end: 0 })) return true
      input.focusEditor()
      return input.addPart({ type: "text", content: text, start: 0, end: 0 })
    }

    if (pasteMode(text) === "manual") {
      put()
      return
    }

    const inserted = typeof document.execCommand === "function" && document.execCommand("insertText", false, text)
    if (inserted) return

    put()
  }

  const handleGlobalDragOver = (event: DragEvent) => {
    if (input.isDialogActive()) return

    event.preventDefault()
    const hasFiles = event.dataTransfer?.types.includes("Files")
    const hasText = event.dataTransfer?.types.includes("text/plain")
    if (hasFiles) {
      input.setDraggingType("image")
    } else if (hasText) {
      input.setDraggingType("@mention")
    }
  }

  const handleGlobalDragLeave = (event: DragEvent) => {
    if (input.isDialogActive()) return
    if (!event.relatedTarget) {
      input.setDraggingType(null)
    }
  }

  const handleGlobalDrop = async (event: DragEvent) => {
    if (input.isDialogActive()) return

    event.preventDefault()
    input.setDraggingType(null)

    const plainText = event.dataTransfer?.getData("text/plain")
    const filePrefix = "file:"
    if (plainText?.startsWith(filePrefix)) {
      const filePath = plainText.slice(filePrefix.length)
      input.focusEditor()
      input.addPart({ type: "file", path: filePath, content: "@" + filePath, start: 0, end: 0 })
      return
    }

    const dropped = event.dataTransfer?.files
    if (!dropped) return

    await addAttachments(Array.from(dropped))
  }

  onMount(() => {
    makeEventListener(document, "dragover", handleGlobalDragOver)
    makeEventListener(document, "dragleave", handleGlobalDragLeave)
    makeEventListener(document, "drop", handleGlobalDrop)
  })

  return {
    addAttachment,
    addAttachments,
    removeAttachment,
    handlePaste,
    isUploading,
  }
}
