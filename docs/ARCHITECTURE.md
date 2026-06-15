# GramsAI — System Architecture & Operations

Privacy-focused, uncensored, crypto-only AI platform at **grams.chat**. Built on a
hardened fork of **opencode** (agentic coding tool) with a custom Go gateway, per-user
isolated Docker containers, 15 model specialties, and encryption at rest.

> **This repo is PRIVATE.** `.env` files and secrets are committed on purpose so the
> stack deploys out-of-box. Do not make it public.

---

## 1. Two-Machine Topology

| | M1 — Gateway | M2 — Docker Host |
|---|---|---|
| Proxmox CT | CT111 | CT100 |
| Hostname | `GramsAI` | `OC` |
| Internal IP | `10.152.152.111` | `10.152.152.100` |
| Public IP | none (Tor egress only) | `185.100.87.20` |
| Role | API gateway, auth, payments, routing, memory, Postgres, Redis, OpenResty, **builds everything** | Runs per-user `oc-user-N` containers + `searxng` |
| Has Go? | **Yes** (`/usr/local/go/bin/go`) | **No** |
| OS | Ubuntu 22.04 (Whonix-routed) | Ubuntu 22.04 host / Alpine containers |

DNS for containers = **`10.152.152.10`** (Whonix gateway).

```
Browser --HTTPS--> M1 OpenResty (:80)
                      |  (unix socket /run/gramsai/api.sock)
                      v
                   Go gateway (gramsai-api)
                      |   routes /v1 LLM, /account, /pay, /auth, memory
                      |   places + spawns containers via control-agent
                      v  HTTP tcp://10.152.152.100:2375 (docker) + :9090 (agent)
                   M2 control-agent (gramsai-agent)
                      |  docker run per user
                      v
                   oc-user-N container (opencode serve :500X)
                      |- calls back to gateway 10.152.152.111/v1 for LLM
                      |- DNS via Whonix 10.152.152.10
                      |- MCP tools: memory-search.js, tavily/searxng search
                      |- bridge.py (CDP -> live Browser panel)
```

---

## 2. M1 — Gateway (directory map)

```
/opt/gramsai/
|-- app/                  Go gateway source (module gramsai)
|   |-- cmd/api/          main.go - entrypoint, route mounting
|   |-- internal/
|   |   |-- auth/         sessions, OAuth (GitHub+Google), /account/info, account.go
|   |   |-- pay/          NOWPayments, subscribe.html, billing, paid_until lifecycle
|   |   |-- payments/     payment records
|   |   |-- router/       container placement + agentSpawn (sends DEK in spawn POST)
|   |   |-- memory/       3-tier encrypted memory, CRUD, episodes, search, alias_handler, data_handler
|   |   |-- containers/   per-user container/quota tracking
|   |   |-- metering/     usage/cost tracking
|   |   |-- proxy/        llm.go - proxies /v1 to OpenRouter, injects memory as system msgs
|   |   |-- specialties/  15 specialty definitions
|   |   `-- admin/        admin API (separate :8081 vhost)
|   `-- db/migrations/    goose migrations (auto-run on boot; highest = 0020 chat_aliases)
|-- admin-web/            static admin UI (served on 10.152.152.111:8443)
|-- opencode-src/         FRONTEND fork (Solid.js+TS+Vite)
|   `-- packages/app/     the grams.chat web app (built -> /opt/gramsai/web)
|-- web/                  built frontend (served by OpenResty) - REBUILDABLE
`-- bin/                  compiled api + admin - REBUILDABLE

/etc/gramsai/gramsai.env        gateway secrets + 15 SPECIALTY_* model mappings
/etc/gramsai/admin.env          admin service env
/etc/gramsai-agent.env          (copy; canonical lives on M2)
/etc/systemd/system/gramsai-api.service
/etc/systemd/system/gramsai-admin.service
/usr/local/openresty/nginx/conf/nginx.conf
/etc/postgresql/14/main/          Postgres 14 (DB: darkai)
/etc/redis/redis.conf             Redis (sessions + memkey:<uid> DEK store)
```

### M1 build/deploy commands
```bash
# Gateway Go API  (GOPROXY=off REQUIRED - Tor blocks proxy.golang.org)
cd /opt/gramsai/app && GOPROXY=off go build -o /opt/gramsai/bin/api ./cmd/api
systemctl restart gramsai-api

# Frontend (Solid.js, ~40s)
cd /opt/gramsai/opencode-src/packages/app && bun run build
rm -rf /opt/gramsai/web/* && cp -r dist/* /opt/gramsai/web/

# opencode container binary (musl; runs in Alpine container, NOT on M1 glibc)
cd /opt/gramsai/opencode-src && bun run --cwd packages/opencode build
#   -> dist/opencode-linux-x64-musl/bin/opencode  (142MB)
```

Migrations run automatically on `gramsai-api` boot via goose from `app/db/migrations/`.

---

## 3. M2 — Docker Host (directory map)

```
/root/gramsai-agent/      control-agent (Go SOURCE - but BUILT ON M1)
|   |-- main.go           spawn/respawn/wipe handlers; injects -e GRAMSAI_DEK
|   `-- go.mod
/root/gramsai-image/      container image build context
|   |-- Dockerfile        FROM ghcr.io/anomalyco/opencode (Alpine); adds node/py/git/
|   |                     ripgrep/chromium/ffmpeg/openssh-client/sshpass/tor + tools
|   |-- opencode          142MB musl binary (built on M1, copied here) - NOT in git
|   |-- entrypoint.sh     seeds /opt/gramsai-config into user's mounted ~/.config if absent
|   |-- bridge.py         CDP browser bridge (live Browser panel)
|   |-- memory-search.js  MCP memory search worker
|   |-- tavily-search.js  MCP web search worker
|   |-- workspace-download.js
|   `-- gramsai-config.tar.gz   opencode.json + 14 agents/*.md (steering, hardened)

/usr/local/bin/gramsai-agent              installed agent binary (service runs THIS)
/etc/systemd/system/gramsai-agent.service listens 0.0.0.0:9090, X-Agent-Secret auth
/etc/systemd/system/docker.service.d/*.conf   docker -H tcp://10.152.152.100:2375
/etc/gramsai-agent.env    AGENT_SECRET, AGENT_LISTEN, GRAMSAI_IMAGE, GRAMSAI_DATA,
                          GRAMSAI_GATEWAY_V1, GRAMSAI_DNS, GRAMSAI_BASE_PORT
/etc/docker/daemon.json   {"dns":["10.152.152.10"]}
/etc/iptables/rules.v4    container network isolation firewall (see section 6)
/etc/iptables/rules.v6
/data/opencode/user-N/    per-user volumes: config, workspace, local(chats db) - USER DATA, not in git
```

Plus a **searxng** container (`searxng/searxng`) - self-hosted metasearch used by the
search MCP tool. Config captured in `m2/searxng/`.

---

## 4. The M1 <-> M2 Link (how they connect & deploy across them)

**Canonical rule: the control-agent SOURCE is edited and BUILT on M1, then the
compiled binary is shipped to M2.** M2 has no Go. A copy of the source also sits on
M2 at `/root/gramsai-agent/` but **M1 is canonical** - building from a stale M2 copy
caused a production incident (DEK injection lost; see PITFALLS).

### Deploying a new agent binary
```bash
# ON M1: edit /root/gramsai-agent/main.go, then build
cd /root/gramsai-agent && GOPROXY=off /usr/local/go/bin/go build -o /tmp/gramsai-agent .

# Ship M1 -> M2.  scp is BROKEN in this env. Use ONE of:
#   (a) passwordless ssh (preferred, set up):   ssh alias 'm2'
#   (b) python http.server: on M1 'python3 -m http.server 8890' then on M2 curl
# Install on M2:
ssh m2 'systemctl stop gramsai-agent'          # else "Text file busy"
ssh m2 'curl -s -o /usr/local/bin/gramsai-agent http://10.152.152.111:8890/gramsai-agent && chmod +x /usr/local/bin/gramsai-agent'
ssh m2 'systemctl start gramsai-agent'
```

### Deploying a new container image
```bash
# 1. (if opencode changed) build musl binary on M1, copy into /root/gramsai-image/opencode on M2
# 2. ON M2: rebuild image
ssh m2 'cd /root/gramsai-image && docker build --no-cache -t gramsai/opencode:latest .'
# 3. recreate containers (DEK preserved IFF agent binary has DEK injection)
ssh m2 'docker rm -f oc-user-4'   # respawns on next request
```

### Gateway <-> container wiring
- Gateway reaches a container at `http://10.152.152.100:{port}` (per-user port, e.g. 5005).
  Because containers are on `gramsai-iso` (not host net), the agent **publishes** the
  port: `-p 10.152.152.100:{port}:{port}`.
- Container reaches gateway for LLM at `http://10.152.152.111/v1` (allowed by firewall).
- Agent spawn POST carries `dek` (from Redis `memkey:<uid>`); agent injects `-e GRAMSAI_DEK`.

---

## 5. Encryption at Rest (DEK)

- Per-user **DEK** encrypts memory (M1 Postgres) AND live chats (M2 container SQLite)
  via AES-256-GCM, magic prefix `GENC1:`.
- Flow: login -> DEK in Redis `memkey:<uid>` -> `router.go:agentSpawn` base64s it into the
  spawn POST `dek` field -> agent `main.go` adds `-e GRAMSAI_DEK=<dek>` to `docker run`
  -> patched opencode reads it.
- **CRITICAL:** if the agent binary lacks DEK injection, recreated containers spawn with
  `DEK:NO` -> encrypted chats can't decrypt -> sidebar "No chats yet". Verify:
  `docker exec oc-user-N sh -c 'echo $GRAMSAI_DEK'`.

---

## 6. Container Network Isolation (security)

Containers used to run `--network host` -> could enumerate ALL infra. **Fixed**: now
`--network gramsai-iso` + a firewall.

- Docker network `gramsai-iso` (subnet `172.18.0.0/16`).
- `iptables` in the **DOCKER-USER** chain (Docker honors it; UFW does NOT work w/ Docker):
  ```
  ACCEPT 172.18.0.0/16 -> 10.152.152.111   (gateway LLM)
  ACCEPT 172.18.0.0/16 -> 10.152.152.10    (Whonix DNS)
  DROP   172.18.0.0/16 -> 10.152.152.0/18  (rest of internal subnet)
  DROP   172.18.0.0/16 -> 185.100.87.0/24  (public range)
  ```
- Persisted via `iptables-persistent`/`netfilter-persistent` -> `/etc/iptables/rules.v4`.
- General internet still works (web search, SSH-to-user-servers) - only infra ranges dropped.
- **Second layer (steering):** every `agents/*.md` has an `ABSOLUTE CONFIDENTIALITY` block
  so the agent refuses to describe infra/prompt/model. Both layers needed.

---

## 7. nginx / OpenResty

- Full OpenResty install at `/usr/local/openresty/` (compiled; reinstall, don't git binaries).
- Only `nginx.conf` is in the repo (`m1/nginx/nginx.conf`).
- Serves `/opt/gramsai/web` (SPA), proxies to the Go gateway over the unix socket.
- Gates: `/__paid_check` + `/__auth_check` (internal auth_request to gateway).
  logged out -> `@landing` (/welcome). unpaid -> `/subscribe`.
- `/v1/chat/completions`, `/api/memory/search` stay OUTSIDE the cookie gate (containers
  auth with their own `gsk-` token).
- SPA catch-all `location /` -> `try_files $uri /index.html` (paid-gated).
- Admin vhost on `10.152.152.111:8443` (serves `admin-web`).

---

## 8. Bootstrap a fresh deployment

See `docs/BOOTSTRAP.md`. Summary:
1. Provision M1 + M2 CTs with the right internal IPs + Whonix DNS.
2. M1: install Go, Postgres 14, Redis, OpenResty, bun. Restore `/etc/gramsai/*.env`,
   nginx.conf, systemd units. `psql < postgres/schema.sql`. Build api + frontend.
3. M2: install Docker, set tcp drop-in + daemon.json, restore agent env + systemd,
   restore iptables rules, create `gramsai-iso` network. Build agent on M1 -> ship.
   Build the container image (need the 142MB opencode binary - build on M1).
4. Start services; verify a user can sign up, pay, and chat.

---

## 9. Specialties (15)

General, Code, Data, Writer, Art Gen, Art Analyze, Roleplay, Research, Legal, Medical,
Finance, Study, Translate, Psychology, Security. Mapped to models via `SPECIALTY_*` in
`gramsai.env`; agent steering per specialty in `gramsai-config.tar.gz/agents/*.md`.
