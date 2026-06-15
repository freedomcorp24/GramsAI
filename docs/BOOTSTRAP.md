# GramsAI — Bootstrap on a Fresh Machine

How to stand the stack up from this repo on new M1 + M2 containers/VMs. Assumes the
two-CT topology in ARCHITECTURE.md (M1 10.152.152.111, M2 10.152.152.100, Whonix DNS
10.152.152.10). Adjust IPs in env/conf if your network differs.

---

## Prerequisites (both)
- Proxmox CTs (or VMs) on the 10.152.152.0/18 internal net + Whonix gateway for DNS/Tor.
- M2 also needs the public IP (185.100.87.20) for clearnet.

## M1 — Gateway
```bash
# packages
apt-get update && apt-get install -y postgresql-14 redis-server git curl build-essential
# Go (1.26+) to /usr/local/go ; bun for the frontend
#   (install Go from go.dev tarball; install bun via official installer)

# OpenResty (compiled) - install per openresty.org, then drop in our conf:
cp m1/nginx/nginx.conf /usr/local/openresty/nginx/conf/nginx.conf

# restore env + systemd
mkdir -p /etc/gramsai
cp m1/env/gramsai.env /etc/gramsai/gramsai.env
cp m1/env/admin.env   /etc/gramsai/admin.env
cp m1/env/gramsai-agent.env /etc/gramsai-agent.env   # copy; canonical on M2
cp m1/systemd/gramsai-api.service   /etc/systemd/system/
cp m1/systemd/gramsai-admin.service /etc/systemd/system/
systemctl daemon-reload

# postgres/redis config
cp m1/postgres/postgresql.conf /etc/postgresql/14/main/ 2>/dev/null || true
cp m1/postgres/pg_hba.conf     /etc/postgresql/14/main/ 2>/dev/null || true
cp m1/redis/redis.conf         /etc/redis/redis.conf    2>/dev/null || true
systemctl restart postgresql redis-server

# create DB + load schema (DB name: darkai, user/pass per DATABASE_URL in gramsai.env)
sudo -u postgres createdb darkai 2>/dev/null || true
psql "$(grep -oP 'DATABASE_URL=\K\S+' /etc/gramsai/gramsai.env)" < m1/postgres/schema.sql
# (goose migrations in app/db/migrations also auto-run on api boot)

# place source + build
mkdir -p /opt/gramsai
cp -r m1/app          /opt/gramsai/app
cp -r m1/admin-web    /opt/gramsai/admin-web
cp -r m1/opencode-src /opt/gramsai/opencode-src

# gateway binary
cd /opt/gramsai/app && GOPROXY=off /usr/local/go/bin/go build -o /opt/gramsai/bin/api ./cmd/api
# admin binary (if applicable)
# frontend deps + build  (restores the 3.7G node_modules)
cd /opt/gramsai/opencode-src && bun install
cd packages/app && bun run build
mkdir -p /opt/gramsai/web && rm -rf /opt/gramsai/web/* && cp -r dist/* /opt/gramsai/web/

# start
systemctl enable --now gramsai-api gramsai-admin
/usr/local/openresty/nginx/sbin/nginx   # or restart if running
```

## M2 — Docker Host
```bash
apt-get update && apt-get install -y docker.io iptables-persistent curl

# docker remote API drop-in + Whonix DNS
mkdir -p /etc/systemd/system/docker.service.d
cp m2/docker/docker-tcp.conf /etc/systemd/system/docker.service.d/
cp m2/docker/daemon.json     /etc/docker/daemon.json
systemctl daemon-reload && systemctl restart docker

# isolated network
docker network create gramsai-iso   # should get 172.18.0.0/16 (verify; adjust firewall if not)

# firewall (container isolation) - restore + persist
cp m2/iptables/rules.v4 /etc/iptables/rules.v4
cp m2/iptables/rules.v6 /etc/iptables/rules.v6
netfilter-persistent reload   # or: iptables-restore < /etc/iptables/rules.v4

# agent env + service
cp m2/env/gramsai-agent.env /etc/gramsai-agent.env
cp m2/systemd/gramsai-agent.service /etc/systemd/system/
systemctl daemon-reload

# agent SOURCE here, but BUILD ON M1, then ship the binary to /usr/local/bin/gramsai-agent
mkdir -p /root/gramsai-agent && cp m2/gramsai-agent/* /root/gramsai-agent/
#   on M1: cd /root/gramsai-agent && GOPROXY=off go build -o /tmp/gramsai-agent .
#   ship to M2 -> /usr/local/bin/gramsai-agent (chmod +x), then:
systemctl enable --now gramsai-agent

# container image: need the 142MB musl opencode binary (build on M1), place it at
#   /root/gramsai-image/opencode  then:
mkdir -p /root/gramsai-image && cp m2/gramsai-image/* /root/gramsai-image/
cd /root/gramsai-image && docker build -t gramsai/opencode:latest .

# searxng (search backend)
docker run -d --name searxng --restart=always searxng/searxng   # add config/volumes per m2/searxng/
```

## Verify
- M1: `systemctl is-active gramsai-api` ; `curl --unix-socket /run/gramsai/api.sock http://localhost/account/info` (401 ok).
- M2: `docker network inspect gramsai-iso` ; `iptables -L DOCKER-USER -n` shows 4 rules.
- End-to-end: sign up a user, pay, open a chat, confirm the agent responds and the
  container is isolated (`docker exec oc-user-N sh -c 'echo $GRAMSAI_DEK'` set;
  M2 host unreachable from inside).

## Notes
- The 142MB `opencode` binary and `node_modules` are NOT in git (rebuilt). The musl
  opencode build comes from `opencode-src` on M1: `bun run --cwd packages/opencode build`.
- `.env` files carry live secrets (private repo). Rotate OAuth + payment keys for any new
  environment.
