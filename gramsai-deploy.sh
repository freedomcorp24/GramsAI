#!/usr/bin/env bash
#
# gramsai-deploy.sh — collect ALL source/config from M1 + M2 into a single repo
# and push to GitHub. Run ON M1 (root@GramsAI). Requires passwordless ssh to M2
# (Host m2 in /root/.ssh/config) and a working github.com deploy key.
#
set -euo pipefail

REPO="${REPO:-/root/GramsAI}"
GIT_REMOTE="git@github.com:freedomcorp24/GramsAI.git"
M2="m2"

say(){ printf "\n\033[1;32m==> %s\033[0m\n" "$*"; }
warn(){ printf "\033[1;33m[!] %s\033[0m\n" "$*"; }

say "Preparing repo tree at $REPO"
mkdir -p "$REPO"/{m1,m2,docs}
mkdir -p "$REPO"/m1/{app,admin-web,opencode-src,nginx,systemd,env,postgres,redis}
mkdir -p "$REPO"/m2/{gramsai-agent,gramsai-image,systemd,env,iptables,docker,searxng}

say "Collecting M1: gateway Go app (source only)"
rsync -a --delete \
  --exclude='*.test' \
  /opt/gramsai/app/ "$REPO/m1/app/"

say "Collecting M1: admin-web"
rsync -a --delete /opt/gramsai/admin-web/ "$REPO/m1/admin-web/"

say "Collecting M1: opencode-src (SOURCE only — excluding node_modules/dist/build)"
rsync -a --delete \
  --exclude='node_modules' \
  --exclude='dist' \
  --exclude='**/dist' \
  --exclude='.git' \
  --exclude='*.log' \
  --exclude='screenshot-*.png' \
  --exclude='packages/console' \
  --exclude='packages/web' \
  --exclude='**/test/**/fixtures' \
  --exclude='*.mp4' --exclude='*.mov' --exclude='*.icns' \
  --exclude='packages/opencode/bin/opencode' \
  --exclude='nix' \
  /opt/gramsai/opencode-src/ "$REPO/m1/opencode-src/"

say "Collecting M1: nginx + openresty conf"
cp /usr/local/openresty/nginx/conf/nginx.conf "$REPO/m1/nginx/nginx.conf"

say "Collecting M1: systemd units"
cp /etc/systemd/system/gramsai-api.service   "$REPO/m1/systemd/"
cp /etc/systemd/system/gramsai-admin.service "$REPO/m1/systemd/"

say "Collecting M1: env (PRIVATE — secrets included by design)"
cp /etc/gramsai/gramsai.env "$REPO/m1/env/gramsai.env"
cp /etc/gramsai/admin.env   "$REPO/m1/env/admin.env"
cp /etc/gramsai-agent.env   "$REPO/m1/env/gramsai-agent.env" 2>/dev/null || true

say "Collecting M1: postgres + redis config (data NOT included — dumped separately)"
cp /etc/postgresql/14/main/postgresql.conf "$REPO/m1/postgres/" 2>/dev/null || true
cp /etc/postgresql/14/main/pg_hba.conf     "$REPO/m1/postgres/" 2>/dev/null || true
cp /etc/redis/redis.conf                   "$REPO/m1/redis/"    2>/dev/null || true

say "Collecting M1: schema dump (structure; no user data)"
if command -v pg_dump >/dev/null 2>&1; then
  pg_dump "$(grep -oP 'DATABASE_URL=\K\S+' /etc/gramsai/gramsai.env)" --schema-only \
    > "$REPO/m1/postgres/schema.sql" 2>/dev/null \
    && say "  schema.sql written" || warn "  pg_dump schema failed (do manually)"
fi

say "Collecting M2: control-agent source (NOT the binary)"
ssh "$M2" 'tar czf - -C /root/gramsai-agent main.go go.mod' > "$REPO/m2/gramsai-agent/agent-src.tar.gz"
( cd "$REPO/m2/gramsai-agent" && tar xzf agent-src.tar.gz && rm -f agent-src.tar.gz )

say "Collecting M2: image build context (excluding 142M opencode binary — see docs to obtain)"
ssh "$M2" 'tar czf - -C /root/gramsai-image \
  Dockerfile entrypoint.sh bridge.py memory-search.js tavily-search.js \
  image-gen.js workspace-download.js gramsai-config.tar.gz' > "$REPO/m2/gramsai-image/image-ctx.tar.gz"
( cd "$REPO/m2/gramsai-image" && tar xzf image-ctx.tar.gz && rm -f image-ctx.tar.gz )

say "Collecting M2: systemd + docker drop-in"
ssh "$M2" 'cat /etc/systemd/system/gramsai-agent.service' > "$REPO/m2/systemd/gramsai-agent.service"
ssh "$M2" 'cat /etc/systemd/system/docker.service.d/*.conf 2>/dev/null' > "$REPO/m2/docker/docker-tcp.conf" || true

say "Collecting M2: env (PRIVATE)"
ssh "$M2" 'cat /etc/gramsai-agent.env' > "$REPO/m2/env/gramsai-agent.env"

say "Collecting M2: docker daemon.json + iptables rules + network"
ssh "$M2" 'cat /etc/docker/daemon.json' > "$REPO/m2/docker/daemon.json"
ssh "$M2" 'cat /etc/iptables/rules.v4' > "$REPO/m2/iptables/rules.v4"
ssh "$M2" 'cat /etc/iptables/rules.v6' > "$REPO/m2/iptables/rules.v6"
ssh "$M2" 'docker network inspect gramsai-iso 2>/dev/null' > "$REPO/m2/docker/gramsai-iso.network.json" || true

say "Collecting M2: searxng run definition (config dumped; volume DATA excluded)"
ssh "$M2" 'docker inspect searxng --format "{{json .Config}}" 2>/dev/null' > "$REPO/m2/searxng/searxng-config.json" || true
ssh "$M2" 'docker inspect searxng --format "{{json .HostConfig}}" 2>/dev/null' > "$REPO/m2/searxng/searxng-hostconfig.json" || true

say "M1 + M2 collection complete."
echo "Next: docs are in $REPO/docs/. Review, then commit + push (see deploy footer)."
