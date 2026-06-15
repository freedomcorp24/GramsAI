// packages/core/src/database/encrypted-column.ts
//
// GramsAI: transparent at-rest encryption for conversation content.
//
// A custom Drizzle column type that encrypts the JSON payload with the user's
// session DEK (AES-256-GCM) on write and decrypts on read. Applied to the
// content-bearing columns (message/part/session-message `data`, session `title`,
// todo `content`). Indexes are on metadata columns only, so encrypting content
// does not break any index.
//
// The DEK is injected into the container at spawn via the GRAMSAI_DEK env var
// (base64, 32 bytes). While the session is live the key is present and content
// decrypts transparently; once the container stops the key is gone and the
// SQLite file on disk is ciphertext.
//
// Fail behavior: if no DEK is present, we fall back to storing/reading plaintext
// (so a misconfigured container still works) — but log a warning. In production
// the DEK is always injected.

import { customType } from "drizzle-orm/sqlite-core"
import { createCipheriv, createDecipheriv, randomBytes } from "node:crypto"

const MAGIC = "GENC1:" // marks an encrypted value so we can detect plaintext rows

function loadDEK(): Buffer | null {
  const b64 = process.env.GRAMSAI_DEK
  if (!b64) return null
  try {
    const k = Buffer.from(b64, "base64")
    return k.length === 32 ? k : null
  } catch {
    return null
  }
}

const DEK = loadDEK()
if (!DEK) {
  // eslint-disable-next-line no-console
  console.warn("[gramsai] GRAMSAI_DEK not set — session content stored UNENCRYPTED")
}

function encrypt(plain: string): string {
  if (!DEK) return plain // fail-open to plaintext if no key
  const iv = randomBytes(12)
  const cipher = createCipheriv("aes-256-gcm", DEK, iv)
  const ct = Buffer.concat([cipher.update(plain, "utf8"), cipher.final()])
  const tag = cipher.getAuthTag()
  // MAGIC + base64(iv|tag|ciphertext)
  return MAGIC + Buffer.concat([iv, tag, ct]).toString("base64")
}

function decrypt(stored: string): string {
  if (!stored.startsWith(MAGIC)) return stored // plaintext (pre-encryption row or no-key write)
  if (!DEK) throw new Error("[gramsai] encrypted row but no DEK present")
  const raw = Buffer.from(stored.slice(MAGIC.length), "base64")
  const iv = raw.subarray(0, 12)
  const tag = raw.subarray(12, 28)
  const ct = raw.subarray(28)
  const decipher = createDecipheriv("aes-256-gcm", DEK, iv)
  decipher.setAuthTag(tag)
  return Buffer.concat([decipher.update(ct), decipher.final()]).toString("utf8")
}

/**
 * encryptedJson — drop-in replacement for text({ mode: "json" }).
 * Stores arbitrary JSON-serializable values, encrypted at rest.
 */
export const encryptedJson = <TData>(name?: string) =>
  customType<{ data: TData; driverData: string }>({
    dataType() {
      return "text"
    },
    toDriver(value: TData): string {
      return encrypt(JSON.stringify(value))
    },
    fromDriver(value: string): TData {
      return JSON.parse(decrypt(value)) as TData
    },
  })(name as any)

/**
 * encryptedText — drop-in replacement for text() for plain string columns
 * (e.g. session title, todo content).
 */
export const encryptedText = (name?: string) =>
  customType<{ data: string; driverData: string }>({
    dataType() {
      return "text"
    },
    toDriver(value: string): string {
      return encrypt(value)
    },
    fromDriver(value: string): string {
      return decrypt(value)
    },
  })(name as any)
