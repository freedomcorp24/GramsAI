# GramsAI — Session Handoff (for the next chat)

This is the live STATE + PLAN doc. Read `ARCHITECTURE.md` and `PITFALLS.md` alongside it.
This document is the one to update at the end of every session.

---

## CURRENT STATE (as of 2026-06-18, end of vision + uploads session)

### Multimodal progress (the current big push)
- **Image generation — DONE+PROVEN** (earlier sessions). `generate_image` MCP tool
  (`/usr/local/bin/image-gen.js`) → gateway `model:"Image Gen"` → flux → writes PNG to
  worktree `images/` → returns short markdown `/dl` link that renders inline. Button modifier,
  works in any specialty.
- **Image ANALYSIS (vision) — DONE+PROVEN+PUSHED this session** (commit `79b04b9`):
  - New `read_image` MCP tool (`/usr/local/bin/read-image.js`, mirror of image-gen.js).
    Reads an image file from disk, base64-encodes, POSTs gateway `model:"Vision"`
    (→ `meta-llama/llama-4-maverick` via Novita), returns the description text. Works in ANY
    agent (it makes a SEPARATE Vision request; never puts a tools/image array on the
    deepseek request). Args: `path` (required), `question`, `context`.
    Registered in `opencode.json` `mcp` block + enabled on all 15 agents (count 16 = 1 mcp + 15).
  - PROVEN in the live UI: under the General agent it described on-disk generated smurf
    images correctly (`Called read_image_read_image /workspace/images/...`).
  - **Why a tool and not opencode's native vision:** opencode's native `file://` image path
    is BROKEN for custom OpenAI-compatible providers (GitHub issues #11306, #20802, #15728).
    The working path is `image_url` + base64 data URL straight to `/chat/completions` —
    proven by curl from inside oc-user-4 (5.4MB PNG → real description, ~3028 prompt tokens,
    ~$0.00085).
  - nginx `client_max_body_size 25m;` added at **http{} level** (covers the auth_request
    subrequest too) so large image POSTs aren't 413'd. (commit `79b04b9`)

- **Image UPLOAD pipeline — BUILT + DEPLOYED this session, NOT YET COMMITTED TO GIT:**
  The problem: uploaded images never hit disk (they live only as `GENC1:` encrypted blobs in
  the container SQLite `part` table + transiently in opencode memory, and are stripped before
  a text-only model). So `read_image` (path-based) couldn't see uploads. Fix built:
  - **Agent `/upload`** (`gramsai-agent/main.go`, mux route): `handleUpload` streams the POST
    body → `/data/opencode/user-N/workspace/uploads/<sanitized-name>`, traversal-guarded,
    returns `{ok,path:"/workspace/uploads/<name>",bytes}`. PROVEN by direct curl (HTTP 200,
    file on disk). Binary built on M1, shipped to M2, both `main.go` md5 = `e444118a`.
  - **Gateway `/upload` proxy** (`internal/router/upload.go` `HandleUpload`, registered
    `r.Post("/upload", rtr.HandleUpload(getUID))` in `cmd/api/main.go`). Resolves uid →
    `control_url` from DB, streams to agent `/upload` with `X-Agent-Secret`. Mirrors
    `download.go`. Built, gateway restarted, healthy. (Agent half proven; full cookie-authed
    browser path not yet end-to-end tested.)
  - **Frontend** (`packages/app/src/components/prompt-input/attachments.ts`): `add()` reads
    dataUrl for preview, pushes the attachment immediately with `uploadProgress:0`, then
    XHR-uploads to `/upload?name=` (real `upload.onprogress` → updates `uploadProgress`, sets
    `uploadedPath` on success). `pendingCount`/`isUploading` signal stays elevated during the
    XHR. `patchAttachment` + `uploadToWorkspace` helpers added.
  - `ImageAttachmentPart` type extended with `uploadedPath?`/`uploadProgress?`
    (`context/prompt.tsx`).
  - **Progress bar UI** (`image-attachments.tsx`): green bar + % overlay while
    `uploadProgress < 100`.
  - **Path note to model** (`build-request-parts.ts`): synthetic text part injected listing
    uploaded `/workspace/uploads/...` paths so the model knows what to pass to `read_image`.
  - **Send-gate** (`prompt-input.tsx`): Enter-key gate (`if (isUploading()) return`) + send
    button `disabled={(!working() && blank()) || isUploading()}` (×2 buttons).
  - All type-check clean; frontend built + deployed to `/opt/gramsai/web` at 17:01:13.

### ⚠️ LIVE BUGS reported at end of session (THE NEXT JOB — fix these first)
1. **Send fires while upload still running.** Upload "takes ages" and you can still press
   send mid-upload. The `isUploading()` gate isn't actually blocking the real submit path —
   verify it covers EVERY send path (Enter, button, and any other), and that `pendingCount`
   is truly >0 during the XHR. Test with a large file.
2. **Storage reports "full" well under the 1GB limit.** Quota counter is wrong/stale/not
   recomputing. NOTE: a prior session (2026-06-17) already fixed storage over-counting once
   (1.21GB→198MB by EXCLUDING `config/` + `.git` from the `du`). This is a regression OR the
   new `/workspace/uploads/` dir OR snapshot/worktree bloat. **MEASURE** `du -sb /data/opencode/user-4`
   on M2 vs what the gateway/Postgres thinks before touching anything. Do NOT guess.
3. **Deleting all chats doesn't refresh the sidebar.** The sidebar uses a direct
   `fetch("/session?roots=true&limit=N")` into a local signal — wipe must re-pull it.
4. **New chat STILL says "at storage limit"** — same root cause as #2 (quota not recomputed).

### How chat-delete / wipe actually works (from 2026-06-17 session — important)
- Chats live in the container SQLite `opencode-dev.db` (no flat session JSON). Worktrees in
  `/root/.local/share/opencode/worktree/<hash>/<name>/`.
- The CLEAN wipe path (reclaims worktrees) is the opencode HTTP API **inside the container**
  on port 5002: `GET /session?roots=true&limit=500` → loop `DELETE /session/{id}`. Proven:
  wiped 55 sessions → 0.
- **MUST also update Postgres** — John flagged that the wipe needs to sync the account-layer
  DB too, not just the container. Verify the wipe path keeps Postgres + container + sidebar
  all consistent.

### Done & proven in EARLIER sessions (carry-forward, still true)
- Container security COMPLETE+PROVEN: steering `ABSOLUTE CONFIDENTIALITY` in all agents +
  network isolation (`gramsai-iso` + DOCKER-USER iptables). DEK preserved on recreate.
- SSH tool part 1 (openssh-client + sshpass in image; chat-driven ssh to user's own servers).
- Live browser panel (bridge.py CDP screencast → Browser tab).
- Subscribe/paywall fix + Upgrade-in-settings (billing tab).
- Payment layer: NOWPayments white-label, hard paywall (403, NOT 402 — nginx auth_request),
  per-tier specialty locking, QR via kazuhikoarase qrcode-generator canvas-to-PNG.
- 3-tier encrypted memory (facts/episodes/procedures) + Privacy & Security settings tab.
- GitHub deploy repo (gramsai-deploy.sh + push.sh) + the 4 docs.
- Chat-loading sidebar fix (direct `fetch /session?roots=true` bypassing the SDK).
- Download URL path-hiding (gateway `chatDirectory()` resolves worktree from chat id).
- Render-layer path sanitization (`sanitizePath()` in core/util/path.ts).
- VOICE: Phase 1 (mic→transcribe→prompt) DONE+PROVEN. Phase 2 (full voice overlay:
  VAD→STT→session→LLM→TTS) built; per-sentence streaming TTS; dedicated Voice LLM
  (deepseek-v4-flash, reasoning off); 30-voice picker; sidebar reactive via session-event
  emitter. PROVED streaming+uncensored TTS is impossible on OpenRouter (only OpenAI
  gpt-audio streams and it's censored) — kept Gemini non-streaming + pipelined per-sentence.
  NOTE there is a PRE-EXISTING unrelated tsc error at `prompt-input.tsx:1227` (voice
  `session.create({body:...})`); harmless to `vite build`, clean up eventually.

---

## LIST 1 — OPEN BUGS / ISSUES TO FIX
1. Send fires before uploads finish (gate not covering the real submit path). [TOP]
2. Storage shows "full" well under 1GB — quota wrong/stale/not resetting. [TOP]
3. Wipe-chats doesn't refresh the sidebar (local fetch signal not re-pulled). [TOP]
4. New chat still reports storage limit (same root cause as #2). [TOP]
5. **Uncommitted:** commit + push the upload pipeline (agent /upload, gateway upload.go,
   frontend upload/progress/path-note) — live on servers, NOT in git.
6. Gateway `/upload` not yet cookie-tested end-to-end through the browser.
7. Model doesn't reliably auto-call `read_image` on uploads (opencode #6149: first message
   with an image defaults to native vision; tool used from 2nd message). Path-note nudge in,
   unverified in app.
8. Non-image uploads (PDF/docs) get NO progress bar and are NOT written to disk — only the
   image `add()` path was wired; the `FileAttachmentPart` (doc) path is separate + untouched.
9. NOWPayments live IPN end-to-end test (Cloudflare may block POST /pay/ipn; needs real XMR).
10. Rotate compromised GitHub + Google OAuth client secrets.
11. NOWPayments `partially_paid` handling unimplemented.
12. Dead-code cleanup: manager.go.
13. Parked: URL-bar base64 path leak; path leaks in bash stdout + thinking/reasoning render.

## LIST 2 — BUILD BACKLOG (designed/wanted, not yet built)
1. Account self-service layer (launch-blocking): password change endpoint, billing
   self-service UI, usage view, notifications, support mechanism.
2. 2FA: TOTP baseline + WebAuthn/passkeys premium.
3. Chat encryption at rest expansion: password-derived DEK wrapped by Argon2id — designed,
   deferred (cross-cutting: login, sessions, password change, recovery, chat read/write).
4. Multimodal remaining: (c) audio record → Whisper transcription; (d) full speech-in/out
   voice mode (voice overlay exists — finish/harden it).
5. Doc/file upload-to-disk + progress for NON-image attachments (extend the image pipeline to
   the FileAttachmentPart path so PDFs/docs land on disk for native Read / pdftotext /
   tesseract). [follows bug #8]
6. Projects: sidebar folders grouping chats (opencode worktree provisions vs custom DB table).
7. Sharing (depends on stable shareable id — pair with the UUID refactor).
8. Clean UUID URLs — FAILED TWICE (PITFALLS #5); needs deep refactor, pair with Sharing.
9. Connectors phase: file connector + GitHub (Drive dropped — conflicts with no-KYC) + saved
   SSH (SSH part 2: DEK-encrypted ssh_credentials table + Connections settings tab).
10. Larger per-user storage tier as a paid upsell (storage add-on in pay.go, tied to renewal).
11. Litestream continuous backup of container SQLite → durable storage on M2 (config-swap to
    Exoscale S3 later). Verify if built.
12. Per-chat workspace isolation (new dir per session) for clean quota counting. Verify current.
13. snapshot/ dir purge (may inflate storage — ties to bug #2).
14. Mobile + desktop builds (separate repos). Mobile PIN-decrypt.
15. Devin-style mid-task live message injection (opencode has a queue; check if it suffices).

## THINGS JOHN MAY HAVE FORGOTTEN (candidates — flagged, not assumed)
- Commit the uncommitted upload-pipeline work before anything else, or it's lost on a crash.
- The pre-existing tsc error at prompt-input.tsx:1227 (voice session.create body) — clean up.
- read-image.js MAX_B64 is 24MB but opencode auto-resizes >5MB base64 — confirm large
  uploads don't silently fail.
- /workspace/uploads/ has no retention/cleanup — will grow forever, feeds the storage bug.
- read_image via maverick/Novita may moderate NSFW — confirm it handles uncensored content.
- Whonix gateway DNS has gone down before (Proxmox pushing 1.1.1.1/9.9.9.9) — if M2 clearnet
  breaks, check the Whonix gateway CT.
- Always md5-compare M1 vs M2 main.go after an agent edit.
- Storage billing model decision (subscription-renewal-tied vs independent expiry) still open.

---

## HOW WE WORK (process — non-negotiable)
- **NEVER ASSUME.** Check, check, re-check. Know 100% before acting. If not verified on the
  machine this session, you don't know it.
- **SEARCH ONLINE BEFORE BUILDING** — OpenCode docs, GitHub issues (anomalyco/opencode,
  sst/opencode), and others with the same problem. Find the correct production-grade way.
  No hacks. Do it properly the first time.
- **PROVE EVERY FIX WITH REAL OUTPUT** (grep/curl/inspect/UI test) before claiming done.
- **Two machines, label every command M1 or M2.** M1 builds; M2 runs containers.
- **Confirm the plan before coding.** No unapproved choices. Read whole files before editing.
- **ONE step at a time, minimal words, command-only where possible.** No verbosity.
- **Complete files only**, path listed first. Fragile edits = Python anchored-patch or
  line-splice (str_replace unreliable on M1). Anchor failures = tabs/spaces or M1-vs-M2
  version diff; grep exact bytes first; imports at top only.
- **Source of truth only** — never container-only edits that wash on reload.
- **Build the whole feature, type-check, deploy, THEN test once.** Do not have John test
  half-built features.
- **Snapshots before risky multi-file work.** Know what a snapshot pre-dates.
- Own mistakes plainly, no defensiveness. John's profanity is style, not hostility — stay
  steady and precise.
- **File delivery in chat:** Claude writes idempotent Python/sh patch scripts (or full files)
  on the machine via `python3 <<'PY'` heredoc (heredocs for raw content break in Whonix);
  John runs them and pastes output. For fragile multi-line edits, splice by line number with
  asserts. `gofmt -w` Go files after a heredoc write.

---

## NEXT-CHAT STARTER PROMPT

> Continue GramsAI. Read `docs/ARCHITECTURE.md`, `docs/PITFALLS.md`, `docs/HANDOFF.md` IN FULL
> before doing anything. Operate by HANDOFF "HOW WE WORK": never assume — check/recheck/know
> 100%; search OpenCode docs + GitHub issues + others with the same problem before building;
> prove every fix with real output; one step at a time, minimal words; label every block
> M1/M2; confirm the plan before writing code; complete files only via Python anchored-patch
> or line-splice; source-of-truth only; build a whole feature then test once. Two machines:
> M1 (gateway, CT111 root@GramsAI 10.152.152.111, builds everything; GOPROXY=off for Go) and
> M2 (docker host, CT100 root@OC 10.152.152.100, no Go). Agent built on M1 then shipped to M2
> (both main.go md5-identical). Container config the container reads is the NESTED path
> /data/opencode/user-4/config/opencode/opencode.json.
>
> FIRST: the storage bug. New chats report "storage limit" while well under 1GB, deleting
> chats doesn't refresh the sidebar, storage fills fast. A prior session already fixed
> over-counting once by excluding config/+.git from the du — this is a regression or the new
> /workspace/uploads dir or snapshot/worktree bloat. DO NOT GUESS — measure `du -sb
> /data/opencode/user-4` on M2 vs what the gateway/Postgres thinks, read the quota path end
> to end, find why it doesn't recompute after wipe (which must sync container + Postgres +
> sidebar), and fix it properly. THEN: the send-gate that still lets send fire mid-upload
> (verify isUploading() covers every submit path), and the sidebar not refreshing after wipe.
>
> ALSO outstanding: commit + push the uncommitted upload-pipeline work (agent /upload,
> gateway upload.go, frontend upload/progress/path-note) — live on the servers, not in git.
> Then verify upload→read_image in the app on a real uploaded image, and extend upload-to-disk
> + progress to non-image files (PDF/docs).
>
> Confirm you've read all three docs and state the plan for the storage bug before touching
> anything.

---

## EXACT UPDATE COMMANDS (every part of the project)

```bash
# --- Gateway (Go API) — M1 ---
cd /opt/gramsai/app && GOPROXY=off go build -o /opt/gramsai/bin/api ./cmd/api && systemctl restart gramsai-api
systemctl is-active gramsai-api && curl -s --unix-socket /run/gramsai/api.sock http://x/api/health

# --- Frontend (SolidJS SPA) — M1 ---
cd /opt/gramsai/opencode-src/packages/app && bun run build        # vite build -> dist/ (~40-46s)
rm -rf /opt/gramsai/web/* && cp -r dist/* /opt/gramsai/web/        # then hard-refresh (Cmd+Shift+R)
# typecheck one file: bunx tsc --noEmit -p tsconfig.json 2>&1 | grep -E "yourfile"
# (ignore the PRE-EXISTING error at prompt-input.tsx:1227 — vite build is not blocked by it)

# --- opencode container binary (musl) — M1 ---
cd /opt/gramsai/opencode-src && bun run --cwd packages/opencode build   # -> dist/opencode-linux-x64-musl/bin/opencode (142MB), copy to M2 /root/gramsai-image/opencode

# --- Control agent (Go) — BUILD ON M1, SHIP TO M2, keep both main.go identical ---
cd /root/gramsai-agent && CGO_ENABLED=0 GOPROXY=off go build -o /usr/local/bin/gramsai-agent .
scp /usr/local/bin/gramsai-agent m2:/usr/local/bin/gramsai-agent.new && scp /root/gramsai-agent/main.go  m2:/root/gramsai-agent/main.go && ssh m2 'mv /usr/local/bin/gramsai-agent.new /usr/local/bin/gramsai-agent && chmod +x /usr/local/bin/gramsai-agent && systemctl restart gramsai-agent && sleep 1 && systemctl is-active gramsai-agent && md5sum /root/gramsai-agent/main.go'
md5sum /root/gramsai-agent/main.go   # MUST equal the M2 md5 above
# (alt if scp fails: M1 `python3 -m http.server 8890`, M2 curl; stop gramsai-agent before replace)

# --- Container image — M2 ---
ssh m2 'cd /root/gramsai-image && docker build -t gramsai/opencode:latest .'   # --no-cache when adding apk packages
# syntax-check a tool JS without running entrypoint:
ssh m2 'docker run --rm --entrypoint node -v /root/gramsai-image/FILE.js:/tmp/x.js gramsai/opencode:latest -c /tmp/x.js && echo OK'

# --- Seed config (gramsai-config.tar.gz) — M2 ---
# patch tarball opencode.json (mcp block + per-agent tools), repack preserving opencode.json + agents/

# --- Push live config to an existing user WITHOUT reseed — M2 (NESTED path!) ---
F=/data/opencode/user-4/config/opencode/opencode.json   # NOT config/opencode.json
cp "$F" "$F.bak"; python3 - "$F" <<'PYX'
# ...patch JSON...
PYX

# --- Safe container recreate (new image + config, KEEP DEK) ---
# 1. M1: redis-cli -a "$(grep -oP 'REDIS_PASSWORD=\K.*' /etc/gramsai/gramsai.env)" EXISTS memkey:4   (must be 1, else chats come back plaintext)
# 2. M2: docker rm -f oc-user-4
# 3. App: reload + open a chat -> gateway lazy-respawns via agentSpawn (re-injects DEK, uses new image)
# 4. M2 verify: docker exec oc-user-4 sh -c 'echo $GRAMSAI_DEK; grep -c read_image /root/.config/opencode/opencode.json'

# --- nginx / OpenResty — M1 ---
# edit /usr/local/openresty/nginx/conf/nginx.conf ; then:
openresty -t && openresty -s reload

# --- Git deploy — M1 ---
cd /root/GramsAI && bash gramsai-deploy.sh && bash push.sh "msg"
# if you added a file to /root/gramsai-image, add it to the explicit file list in gramsai-deploy.sh (~lines 74-76) or it won't reach the repo
```

---

## FULL SAVED MEMORY (verbatim, for the next chat to re-save)

**Purpose & context:** John, sole dev, building GramsAI (grams.chat) — privacy-focused, uncensored, crypto-only AI chat platform. Hardened fork of OpenCode (SolidJS SPA) + custom Go gateway + per-user isolated Docker containers + 14/15 locked specialties mapped to backing models via OpenRouter (DeepSeek direct for uncensored). No-KYC crypto via NOWPayments; Whonix + Cloudflare tunnel. Success = fully self-hosted, paying-user-ready launch: payment flow, account self-service, container isolation proven end-to-end.

**Architecture:** M1 = CT111 root@GramsAI 10.152.152.111 (Go gateway + build, Postgres 14 `darkai`, Redis, OpenResty, SolidJS frontend; HAS Go/bun/node). M2 = CT100 root@OC 10.152.152.100 public 185.100.87.20 (Docker host, per-user Alpine containers, gramsai-agent, searxng; NO Go, NO node on host).

**Style:** blunt, heavy profanity (style not hostility), zero tolerance for guessing / unverified claims / wasted steps / changing direction.

**State:** ~90% MVP, ~65% production-launch. Done: container network isolation; SSH tool pt1; subscribe/paywall gate; full GitHub deploy system; payment layer (NOWPayments white-label, 403 paywall, per-tier locking, QR); path sanitization; download URL path-hiding; memory system; voice phases 1+2; image gen; image analysis (read_image); upload pipeline (uncommitted).

**Active blockers:** NOWPayments IPN never tested live (Cloudflare may block POST /pay/ipn; needs real XMR); GitHub + Google OAuth secrets need rotation (compromised); partial-payment handling unimplemented.

**Horizon:** account self-service (launch-blocking: password change, billing UI, usage view, notifications, support); 2FA (TOTP + WebAuthn/passkeys); chat encryption at rest (Argon2id-wrapped DEK, deferred); Projects (sidebar folders; native worktree vs custom DB table); Connectors (GitHub; Drive dropped — conflicts no-KYC); dead-code cleanup manager.go; parked URL-bar base64 path leak + bash stdout / reasoning render path leaks; larger storage tier upsell.

**Key learnings:** never claim fixed without proof; nginx auth_request only accepts 401/403 (402 -> 500), unpaid -> 403; OpenRouter has its own moderation layer (use DeepSeek direct for uncensored); always use DATED model IDs (undated = free-tier rate-limited); migrations + subscribe.html are Go-embedded (rebuild binary); `bun run build` is the frontend build; M1->M2 transfer via ssh alias m2 or python http.server (scp broken); heredocs break in Whonix (use python3 heredoc); GitHub Push Protection fires org-level even on private repos (scrub secrets first); Docker BuildKit needs --no-cache when adding apk pkgs; domain names in configs use bash var substitution; M2 container mgmt always directly on M2; chat encryption ceiling is at-rest only; GOPROXY=off required for Go builds on M1 (Tor blocks proxy.golang.org); nested config path config/opencode/opencode.json; uploads don't persist to disk by default; opencode native file:// vision broken for custom providers.

**Tools:** Go 1.26.3, Bun, SolidJS, Python 3, Alpine. Proxmox LXC, Docker (VFS), OpenResty/nginx, Whonix, Cloudflare tunnel. Postgres 14, Redis 6. OpenRouter + DeepSeek direct. NOWPayments white-label. Repo github.com/freedomcorp24/GramsAI (private). Configs /etc/gramsai/gramsai.env, /root/GramsAI/secrets/. QR kazuhikoarase qrcode-generator.

---

## REPO LAYOUT
```
GramsAI/
|-- gramsai-deploy.sh    collect M1 + M2 source/config into this repo (run on M1).
|                        NOTE: m2/gramsai-image files are pulled by an EXPLICIT file list
|                        (~lines 74-76) — adding a new image file (e.g. read-image.js) means
|                        adding it to that list or it won't reach the repo.
|-- push.sh              commit + push to github.com/freedomcorp24/GramsAI
|-- .gitignore           excludes node_modules, binaries, dist, user data
|-- m1/                  gateway: app/ admin-web/ opencode-src(source)/ nginx/ systemd/ env/ postgres/ redis/
|-- m2/                  docker host: gramsai-agent/ gramsai-image/ systemd/ env/ iptables/ docker/ searxng/
`-- docs/                ARCHITECTURE.md  PITFALLS.md  HANDOFF.md  BOOTSTRAP.md
```
