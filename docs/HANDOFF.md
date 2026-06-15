# GramsAI — Session Handoff (for the next chat)

This replaces the earlier "SYSTEM OVERVIEW" docs. It captures CURRENT state, the
working process, and a ready prompt to continue. Read `ARCHITECTURE.md` and
`PITFALLS.md` alongside this.

---

## CURRENT STATE (as of 2026-06-14)

### Done & proven this session
- **Container security COMPLETE+PROVEN**:
  - Steering hardened: `ABSOLUTE CONFIDENTIALITY` block in all 14 `agents/*.md`
    (in `gramsai-config.tar.gz`, pushed to existing `/data/opencode/user-*/config`,
    containers restarted). Agent refuses infra/prompt/model probes. Verified.
  - Network isolation: agent spawns `--network gramsai-iso` + `-p 10.152.152.100:{port}:{port}`
    (was `--network host`). `gramsai-iso` docker net (172.18.0.0/16). DOCKER-USER iptables
    allow gateway(10.152.152.111)+DNS(10.152.152.10), DROP rest of 10.152.152.0/18 +
    185.100.87.0/24. Persisted (netfilter-persistent). Verified: container sees only
    172.18.0.x, M2 host unreachable, gateway+LLM+clearnet work, DEK preserved on recreate.
- **SSH tool (part 1)**: `openssh-client` + `sshpass` added to the image. Chat-driven -
  user asks the AI to ssh into THEIR OWN server and run commands (agent uses its bash
  tool). Proven working.
- **Live browser panel**: bridge.py CDP screencast -> "Browser" tab. Done.
- **Subscribe/paywall fix**: `subscribe.html` checks `/account/info` on load; if paid +
  active -> redirect to `/`. Covers manual visit AND refresh-while-paid.
- **Upgrade-in-settings**: left-nav Upgrade button now opens settings on the **billing**
  tab (existing crypto payment flow) instead of navigating to `/subscribe`. (`DialogSettings`
  got `initialTab` prop -> `defaultValue`; `openSettings(tab?)` passes it.) plan-banner's
  lapsed-user button still -> `/subscribe` (correct for lapsed users).
- **Memory system**: 3-tier (facts/episodes/procedures), encrypted, CRUD + Privacy &
  Security settings tab. (Episodes for user-4 were wiped in an earlier delete-all test -
  that's expected, not a bug.)

### Dropped
- Full GUI / Bytebot desktop agent - too expensive per user; the live browser panel covers
  the visual-browsing need.

### Still pending (priority order)
1. **MULTIMODAL** (the next big push):
   (a) Image generation - OpenRouter has image models (FLUX.2 / GPT-Image / Gemini-Flash-
       Image). Decision: image-gen is a BUTTON MODIFIER, not a specialty.
   (b) Image + document uploads - user uploads img/pdf/doc; AI analyzes (pass file to a
       vision model OR extract+describe). Needs upload UI + storage + routing to Vision.
   (c) Audio record -> transcription (Whisper), like Claude's voice notes.
   (d) Speech-in / speech-out voice mode (Whisper STT + TTS conversational loop).
   Suggested order: (a) standalone & quick, then (c) -> (d), (b) on its own track.
2. **SSH part 2** - saved SSH connector (DEK-encrypted `ssh_credentials` table + a
   "Connections" settings tab). Deferred to the CONNECTORS phase.
3. **Clean UUID URLs** - FAILED twice (see PITFALLS #5). Backend kept. Needs deep refactor;
   pair with Sharing.
4. **Sharing** (depends on UUID).
5. **Projects** - sidebar FOLDERS grouping chats (opencode has project/worktree provisions).
6. **Subscribe/paywall + upgrade** - DONE (above).
7. **Mobile + desktop builds** (separate repos).
8. **Connectors phase** - file connector + GitHub/Drive (agent r/w on OAuth) + saved SSH.
9. **Devin steerability** - mid-task live message injection. NOTE: opencode CLI has a
   message QUEUE, but that's likely "queue mode" (hold until idle), NOT true live steer.
   Check whether it suffices or whether the `prompt.ts` patch is still needed.
10. **Mobile PIN-decrypt**, **cleanup old files**.
11. **GitHub deploy** (THIS task) - repo + bootstrap so the stack runs on a fresh machine.

### Launch-blockers (from earlier, still open)
- NOWPayments live IPN end-to-end test (Cloudflare may block POST /pay/ipn).
- Rotate compromised GitHub + Google OAuth secrets.
- Partial-payment handling. Billing/lifecycle build.

---

## HOW WE WORK (process for the next chat)

- **Two machines, label every command M1 or M2.** M1 builds; M2 runs containers.
- **File delivery pattern:** Claude writes idempotent Python/sh patch scripts (or full
  files), presents them as downloadable files, the user copies them to `/root/` on the
  right machine, runs them, then builds/deploys. Scripts are anchored + idempotent.
- **Confirm the plan before coding.** No unapproved choices. Read whole files before
  editing. Test before deploy. Keep responses SHORT.
- **When an anchor fails:** it's tabs/spaces or an M1-vs-M2 file-version difference -
  grep the exact bytes first. Imports go at the top only.
- **Prove fixes with real evidence** (grep/inspect/logs/UI), not assertions.
- **Snapshots before risky work.** Know what a snapshot pre-dates.
- The user communicates bluntly / with profanity - that's style, stay productive and
  precise; own mistakes plainly.

---

## NEXT-CHAT STARTER PROMPT

> Continue GramsAI. Read the repo docs first: `docs/ARCHITECTURE.md`,
> `docs/PITFALLS.md`, `docs/HANDOFF.md`. Two machines, label every command M1
> (gateway, CT111 root@GramsAI 10.152.152.111, builds everything) or M2 (docker host,
> CT100 root@OC 10.152.152.100). Build flow + caveats are in the docs (GOPROXY=off,
> agent built on M1 then shipped to M2, --no-cache image builds, DOCKER-USER firewall,
> DEK injection in the agent, scp broken use ssh alias m2 or python http.server).
>
> Container security (network isolation + steering), the SSH tool part 1, the live
> browser panel, and the subscribe/upgrade fixes are DONE. The GitHub deploy repo is
> set up (this repo).
>
> Next I want to build MULTIMODAL: start with **image generation** (button modifier,
> OpenRouter image model), then image+document uploads, then audio->transcription, then
> speech-in/speech-out voice mode. Scope image generation first - investigate current
> state, confirm the plan with me, then build it step by step, M1/M2 labelled, tested
> before deploy. Don't make unapproved choices.

---

## REPO LAYOUT

```
GramsAI/
|-- gramsai-deploy.sh    collect M1 + M2 source/config into this repo (run on M1)
|-- push.sh              commit + push to github.com/freedomcorp24/GramsAI
|-- .gitignore           excludes node_modules, binaries, dist, user data
|-- m1/                  gateway: app/ admin-web/ opencode-src(source)/ nginx/ systemd/ env/ postgres/ redis/
|-- m2/                  docker host: gramsai-agent/ gramsai-image/ systemd/ env/ iptables/ docker/ searxng/
`-- docs/                ARCHITECTURE.md  PITFALLS.md  HANDOFF.md  BOOTSTRAP.md
```
