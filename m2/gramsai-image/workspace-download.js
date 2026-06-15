#!/usr/bin/env node
// Minimal workspace download service for GramsAI containers.
// GET /dl            -> entire /workspace as a zip (git archive HEAD + untracked)
// GET /dl?path=a/b   -> single file from /workspace
// Listens on 127.0.0.1:5010 (proxied by Machine 1 nginx under /dl).
const http = require("http");
const { spawn } = require("child_process");
const fs = require("fs");
const path = require("path");

const WORKSPACE = process.env.GRAMSAI_WORKSPACE || "/workspace";
const PORT = parseInt(process.env.GRAMSAI_DL_PORT || "5010", 10);

function safeJoin(base, target) {
  const p = path.resolve(base, "." + path.sep + (target || ""));
  if (!p.startsWith(path.resolve(base))) return null; // path traversal guard
  return p;
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost");
  if (url.pathname !== "/dl") {
    res.writeHead(404); res.end("not found"); return;
  }

  const rel = url.searchParams.get("path");

  // Single file download
  if (rel) {
    const fp = safeJoin(WORKSPACE, rel);
    if (!fp || !fs.existsSync(fp) || !fs.statSync(fp).isFile()) {
      res.writeHead(404); res.end("file not found"); return;
    }
    res.writeHead(200, {
      "Content-Type": "application/octet-stream",
      "Content-Disposition": `attachment; filename="${path.basename(fp)}"`,
    });
    fs.createReadStream(fp).pipe(res);
    return;
  }

  // Whole-workspace zip via `git archive` (tracked) is clean but misses untracked;
  // use `zip -r` over the workspace excluding opencode state + .git for a full snapshot.
  res.writeHead(200, {
    "Content-Type": "application/zip",
    "Content-Disposition": 'attachment; filename="workspace.zip"',
  });
  const zip = spawn("sh", ["-c",
    `cd "${WORKSPACE}" && zip -r -q - . -x ".git/*" -x ".config/*" -x ".local/*" -x "*.db" -x "*.db-shm" -x "*.db-wal"`
  ]);
  zip.stdout.pipe(res);
  zip.stderr.on("data", () => {});
  zip.on("error", () => { try { res.end(); } catch {} });
});

server.listen(PORT, "0.0.0.0", () => {
  console.log(`workspace-download listening on ${PORT}`);
});
