# GramsAI

Private deployment repo for **grams.chat** - a privacy-focused, uncensored, crypto-only
AI platform (hardened opencode fork + Go gateway + per-user isolated containers).

**Start here:** `docs/ARCHITECTURE.md` (how it all works), `docs/HANDOFF.md` (current
state + next steps + how we work), `docs/PITFALLS.md` (mistakes not to repeat),
`docs/BOOTSTRAP.md` (stand up a fresh deployment).

## Keeping the repo in sync
```bash
# run on M1 (needs passwordless ssh to M2 + github deploy key)
bash gramsai-deploy.sh     # collect M1 + M2 source/config into this tree
bash push.sh "your commit message"   # commit + push to github.com/freedomcorp24/GramsAI
```

## Layout
- `m1/` - gateway (Go app, admin-web, frontend source, nginx, systemd, env, pg/redis conf)
- `m2/` - docker host (control-agent source, image build context, systemd, env, iptables, docker, searxng)
- `docs/` - architecture, bootstrap, pitfalls, handoff

> PRIVATE repo - `.env` files and secrets are committed by design. Do not publish.
