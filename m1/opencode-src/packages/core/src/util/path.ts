export function sanitizePath(input: string | undefined): string {
  if (!input) return input ?? ""
  return input
    .replace(/\/root\/\.local\/share\/opencode\/worktree\/[0-9a-fA-F]+\/[A-Za-z0-9._-]+/g, "~")
    .replace(/\/root\/\.local\/share\/opencode[^\s)"'`\]]*/g, "~")
    .replace(/\/workspace\/\.git\/worktrees\/[A-Za-z0-9._-]+/g, "~/.git")
    .replace(/\/root\/\.config\/opencode[^\s)"'`\]]*/g, "~/.config")
    .replace(/opencode\/worktree\/[0-9a-fA-F]+\/[A-Za-z0-9._-]+/g, "~")
    .replace(/\/workspace\b/g, "~")
}

export function getFilename(path: string | undefined) {
  if (!path) return ""
  const trimmed = path.replace(/[/\\]+$/, "")
  const parts = trimmed.split(/[/\\]/)
  return parts[parts.length - 1] ?? ""
}

export function getDirectory(path: string | undefined) {
  if (!path) return ""
  const trimmed = path.replace(/[/\\]+$/, "")
  const parts = trimmed.split(/[/\\]/)
  return parts.slice(0, parts.length - 1).join("/") + "/"
}

export function getFileExtension(path: string | undefined) {
  if (!path) return ""
  const parts = path.split(".")
  return parts[parts.length - 1]
}

export function getFilenameTruncated(path: string | undefined, maxLength: number = 20) {
  const filename = getFilename(path)
  if (filename.length <= maxLength) return filename
  const lastDot = filename.lastIndexOf(".")
  const ext = lastDot <= 0 ? "" : filename.slice(lastDot)
  const available = maxLength - ext.length - 1 // -1 for ellipsis
  if (available <= 0) return filename.slice(0, maxLength - 1) + "…"
  return filename.slice(0, available) + "…" + ext
}

export function truncateMiddle(text: string, maxLength: number = 20) {
  if (text.length <= maxLength) return text
  const available = maxLength - 1 // -1 for ellipsis
  const start = Math.ceil(available / 2)
  const end = Math.floor(available / 2)
  return text.slice(0, start) + "…" + text.slice(-end)
}
