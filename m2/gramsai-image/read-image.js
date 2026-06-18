#!/usr/bin/env node
// read-image MCP server: read_image. Reads an image file from the session
// workspace, base64-encodes it, and calls the GramsAI gateway (model "Vision",
// which the gateway maps to meta-llama/llama-4-maverick) to get a text
// description. Returns the description as plain text so ANY active model
// (text-only deepseek, etc.) can reason about the image's contents.
const http = require("http");
const fs = require("fs");
const path = require("path");
const { URL } = require("url");

const TOKEN = process.env.GRAMSAI_TOKEN || "";
const GATEWAY = process.env.GRAMSAI_GATEWAY_V1 || "http://10.152.152.111/v1";
const WORKSPACE = process.cwd();
const MAX_B64_BYTES = 24 * 1024 * 1024;

const MIME = {
  ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
  ".webp": "image/webp", ".gif": "image/gif", ".bmp": "image/bmp",
  ".heic": "image/heic", ".heif": "image/heif",
};

function rpc(o) { process.stdout.write(JSON.stringify(o) + "\n"); }

function resolveImage(p) {
  if (!p) return null;
  const candidates = [];
  if (path.isAbsolute(p)) candidates.push(p);
  candidates.push(path.resolve(WORKSPACE, p));
  for (const c of candidates) {
    try { if (fs.statSync(c).isFile()) return c; } catch {}
  }
  const base = path.basename(p);
  return findByName(WORKSPACE, base, 6);
}

function findByName(dir, name, depth) {
  if (depth < 0) return null;
  let entries;
  try { entries = fs.readdirSync(dir, { withFileTypes: true }); } catch { return null; }
  for (const e of entries) {
    if (e.name === ".git" || e.name === "node_modules") continue;
    const full = path.join(dir, e.name);
    if (e.isFile() && e.name === name) return full;
    if (e.isDirectory()) {
      const r = findByName(full, name, depth - 1);
      if (r) return r;
    }
  }
  return null;
}

function describe(b64, mime, question, context) {
  const u = new URL(GATEWAY.replace(/\/+$/, "") + "/chat/completions");
  const userText = (context ? (context + "\n\n") : "") +
    (question || "Describe this image in detail.");
  const payload = {
    model: "Vision",
    stream: false,
    messages: [{
      role: "user",
      content: [
        { type: "text", text: userText },
        { type: "image_url", image_url: { url: `data:${mime};base64,${b64}` } },
      ],
    }],
  };
  const body = JSON.stringify(payload);
  return new Promise((resolve, reject) => {
    const req = http.request({
      host: u.hostname, port: u.port || 80, path: u.pathname, method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Content-Length": Buffer.byteLength(body),
        "Authorization": "Bearer " + TOKEN,
      },
    }, res => { let d = ""; res.on("data", c => d += c); res.on("end", () => resolve(d)); });
    req.on("error", reject);
    req.setTimeout(120000, () => req.destroy(new Error("vision request timeout")));
    req.write(body); req.end();
  });
}

function parse(raw) {
  let data; try { data = JSON.parse(raw); } catch { return { error: "bad gateway response" }; }
  if (data.error) return { error: (data.error.message || "gateway error") };
  const msg = ((data.choices || [])[0] || {}).message || {};
  const content = msg.content || "";
  if (!content) return { error: "no description in response" };
  return { text: content };
}

let buf = "";
process.stdin.on("data", async chunk => {
  buf += chunk; let i;
  while ((i = buf.indexOf("\n")) >= 0) {
    const line = buf.slice(0, i); buf = buf.slice(i + 1);
    if (!line.trim()) continue;
    let msg; try { msg = JSON.parse(line); } catch { continue; }
    if (msg.method === "initialize") {
      rpc({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "read-image", version: "1.0.0" } } });
    } else if (msg.method === "tools/list") {
      rpc({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
        name: "read_image",
        description: "Read and describe the contents of an image file. Use whenever the user uploads, attaches, references, or asks about ANY image, picture, photo, screenshot, or previously generated image - including 'what is in this image', 'describe this', 'what does it look like', or to inspect a generated image before regenerating a better version. Pass the image's filename or path. Returns a detailed text description you can then reason about and answer with. Works for uploaded images and images in the images/ folder.",
        inputSchema: { type: "object", properties: {
          path: { type: "string", description: "Filename or path of the image to read, e.g. 'SalmaSmurf.png' or 'images/foo.png'" },
          question: { type: "string", description: "Optional: what specifically to look for or answer about the image" },
          context: { type: "string", description: "Optional: relevant conversation context to ground the description" },
        }, required: ["path"] },
      }] } });
    } else if (msg.method === "tools/call") {
      try {
        const args = msg.params.arguments || {};
        const p = args.path;
        if (!p) { rpc({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "read_image error: no path provided" }], isError: true } }); continue; }
        const full = resolveImage(p);
        if (!full) { rpc({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "read_image error: file not found: " + p }], isError: true } }); continue; }
        const ext = path.extname(full).toLowerCase();
        const mime = MIME[ext];
        if (!mime) { rpc({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "read_image error: unsupported image type: " + ext }], isError: true } }); continue; }
        let b64;
        try { b64 = fs.readFileSync(full).toString("base64"); }
        catch (e) { rpc({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "read_image error: cannot read file: " + e.message }], isError: true } }); continue; }
        if (b64.length > MAX_B64_BYTES) { rpc({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "read_image error: image too large (over 24MB encoded)" }], isError: true } }); continue; }
        const raw = await describe(b64, mime, args.question, args.context);
        const r = parse(raw);
        if (r.error) {
          rpc({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "read_image failed: " + r.error }], isError: true } });
        } else {
          rpc({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: r.text }] } });
        }
      } catch (e) {
        rpc({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "read_image error: " + e.message }], isError: true } });
      }
    } else if (msg.method && msg.id !== undefined) {
      rpc({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "Method not found" } });
    }
  }
});
