# GramsAI — Pitfalls & Hard-Won Lessons

Real mistakes made during development. Read before touching the agent, containers,
or doing M1<->M2 work. Each cost real time.

---

## 1. DEK loss from building the agent off the WRONG source  (SEV: high)
**What happened:** The control-agent source exists on BOTH M1 (`/root/gramsai-agent/main.go`)
and M2 (same path). During container-isolation work we patched the M2 copy, then copied
M2's source back to M1 to build. M2's copy was STALE and lacked the `GRAMSAI_DEK`
injection. The resulting binary spawned containers WITHOUT the DEK -> encrypted chats
(`GENC1:`) couldn't decrypt -> sidebar showed "No chats yet".
**Rule:** M1's `/root/gramsai-agent/main.go` is CANONICAL. Always edit + build there.
Never copy M2->M1 to build. After any agent change, verify the binary contains the DEK:
`grep -c GRAMSAI_DEK /usr/local/bin/gramsai-agent` (must be >=1).

## 2. Recreating containers with a DEK-less agent wipes decryption  (SEV: high)
A *running* container keeps its DEK in `.Config.Env` even if the agent later loses the
injection. The damage only appears when you `docker rm -f` + respawn with the bad agent.
**Rule:** DO NOT recreate containers unless the installed agent binary has DEK injection.
Verify post-recreate: `docker exec oc-user-N sh -c 'echo $GRAMSAI_DEK'` -> must be set.

## 3. `--network host` leaked the entire infrastructure  (SEV: critical)
Containers spawned with `--network host` shared M2's network stack -> the agent could
`uname`, `lscpu`, read all IPs (public + internal), reach `10.152.152.111`, even dump
`GRAMSAI_TOKEN`/`GRAMSAI_DEK` from its own env, and describe the whole architecture.
**Fix:** `--network gramsai-iso` + DOCKER-USER iptables (allow gateway+DNS, drop rest).
**Lesson:** steering (system-prompt confidentiality) is NOT enough alone - the agent runs
bash and will reveal anything reachable. Network isolation is the real seal; steering is
a second layer. Note `env` still exposes the token/DEK to the agent (future hardening).

## 4. UFW does not work with Docker  (SEV: medium)
Docker writes its own iptables rules and bypasses UFW entirely. Container traffic is NOT
filtered by UFW.
**Rule:** put container firewall rules in the **DOCKER-USER** chain. Persist with
`iptables-persistent` (`/etc/iptables/rules.v4`).

## 5. Clean UUID URLs - FAILED TWICE, do not retry casually  (SEV: medium, time sink)
Goal: replace `/{base64(dir)}/session/{ses_xxx}` with `/chat/{uuid}` like Claude.
- **Attempt A** (redirect + history.replaceState): the directory-layout canonicalize
  effect fought the URL -> flicker/revert; permission popups went to the old URL;
  sidebar didn't update new chats without refresh.
- **Attempt B** (uuid as real route param + signal-backed cache + rewritten useSessionKey):
  clean URL showed BUT the session wasn't truly live - responses needed a page refresh,
  permission popups said "go to session" -> old URL, sidebar stale.
**Root cause:** opencode is fundamentally directory-keyed; ~18 navigate call sites build
`/{dir}/session/{id}`, and `useSessionKey`/`directory-layout` assume dir+id in the URL.
A thin alias layer can't make the session "live" without a deep refactor.
**Status:** REVERTED via Proxmox snapshot. Backend kept (`chat_aliases` table + alias/
resolve endpoints, migration 0020). Revisit ONLY with a proper deep refactor, ideally
together with Sharing (which needs a stable shareable id anyway).

## 6. Patch-script anchor failures (tabs vs spaces, wrong file version)  (SEV: low, recurring)
Idempotent Python patch scripts repeatedly failed because:
- Go files use **TABS**; anchors written with spaces don't match.
- The same file differs between M1 and M2 (or before/after a snapshot) -> anchor not found.
- Import statements inserted using a `const x = useY()` anchor landed the `import`
  mid-function-body -> "import/export may only appear at top level" build error.
**Rule:** before patching, `grep`/`sed` the EXACT bytes. Add imports only at the top,
after existing imports - never anchored to in-body code. Confirm anchor count == expected.

## 7. Proxmox snapshots saved us repeatedly  (process)
Several dead-ends were undone by reverting both CTs to a snapshot. Take a snapshot before
risky multi-file changes (UUID work, agent rebuilds, network changes). Know exactly what a
snapshot pre-dates (e.g. a snapshot before the DEK-injection work would re-lose it).

## 8. Build output buffering looks like a hang
`docker build ... | tail -8` shows nothing until the build finishes (~5-7 min, more with
`--no-cache`). It's NOT stuck. For live output, run `docker build` with no pipe.
Also: editing the Dockerfile then rebuilding may show `CACHED` for the changed layer in
some buildkit cases - use `--no-cache` to force the apk step to actually run.

## 9. scp is broken in this environment
Use passwordless ssh (set up: `Host m2` in `/root/.ssh/config`, key in authorized_keys
on M2 with `chmod 600`, `PubkeyAuthentication yes` + `PermitRootLogin prohibit-password`
in M2 sshd_config) OR the `python3 -m http.server` + `curl` method.

## 10. GOPROXY=off is REQUIRED for Go builds on M1
M1 routes egress through Tor; `proxy.golang.org` is blocked. Always
`GOPROXY=off go build ...`. Deps must already be in the module cache/vendor.

---

## 11. The container reads opencode.json at a NESTED path  (SEV: high, time sink)
The opencode.json the container actually reads is
`/data/opencode/user-N/config/opencode/opencode.json` (mounted to
`/root/.config/opencode/opencode.json`) — **NOT** `config/opencode.json` one level up. A
stray file at the higher level is silently ignored. Patching the wrong level wasted a long
time during the vision work. Always patch the NESTED path; verify with
`docker exec oc-user-N sh -c 'grep -c <key> /root/.config/opencode/opencode.json'`.
Also: the config dir is a PERSISTENT host volume, and `entrypoint.sh` only seeds if
opencode.json is ABSENT — so recreating a container does NOT reseed. Push new config by
overwriting the live nested file (or rebuild the image seed tarball AND overwrite live).

## 12. opencode native `file://` image input is BROKEN for custom providers  (SEV: high)
For custom OpenAI-compatible providers (our gateway), opencode does NOT translate a `file://`
image attachment into a usable multimodal request — the model reports "doesn't support image
input" even when it does. Confirmed by GitHub issues #11306, #20802, #15728. The Read tool
also can't pass image data to vision models (#15728).
**The working path:** `image_url` + base64 data URL sent straight to `/chat/completions`.
PROVEN by curl from inside oc-user-4 → gateway `model:"Vision"` → meta-llama/llama-4-maverick
returned a real description. So image analysis is done as an MCP TOOL (`read_image.js`) that
makes its own Vision request, NOT via opencode's native vision/attachment flag.

## 13. Uploaded images don't exist on disk  (SEV: high, root of many "can't read image" bugs)
An uploaded image lives ONLY as a `GENC1:` AES-256-GCM blob in the container SQLite `part`
table + transiently in opencode memory, and is STRIPPED before a text-only model. It is NOT
written to the worktree or anywhere on disk. So a path-based tool (read_image) cannot see a
raw upload. Fix: write uploads to `/workspace/uploads/` via a gateway→agent `/upload` pipeline,
then pass that path to read_image. (Generated images DO live on disk in worktree `images/`.)

## 14. read_image must call Vision SEPARATELY, not add tools to the deepseek request  (SEV: med)
A reported "No endpoints found that support tool use" error appeared when a tools/image array
hit the pinned deepseek provider. `generate_image` works because it's a local MCP tool that
makes its OWN gateway request (model "Image Gen") — it never puts tools/images on the active
deepseek chat request. `read_image` follows the same pattern (own `model:"Vision"` request).
So DON'T touch the gateway provider pin (`order:["DeepSeek"], require_parameters:true`) — it
is the anti-timeout pin and both image tools work through it.

## 15. str_replace / multi-line .replace() unreliable on M1  (SEV: low, recurring)
`str_replace` and Python multi-line `.replace()` of source blocks repeatedly FAIL on M1
because the Whonix terminal mangles tabs/spaces in heredocs, so the anchor never matches.
**Use line-number splicing** instead: `grep -n` the exact lines, verify with asserts that the
target lines are what you expect, then replace the range. Confirms beat guesses every time.

## 16. Sequence multi-file features; deploy before asking for a test  (SEV: process)
During the uploads session, individual pieces (upload logic, progress-bar UI, path note) were
written but the frontend was NOT rebuilt/deployed between them, so the user tested and saw
"nothing works" repeatedly. RULE: build ALL pieces of a feature, type-check, `bun run build`
+ deploy to /opt/gramsai/web, THEN have the user test ONCE. Never ask the user to test a
half-built, undeployed feature.
