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
