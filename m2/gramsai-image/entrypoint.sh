#!/bin/sh
set -e
WORKSPACE="${GRAMSAI_WORKSPACE:-/workspace}"

# ---- Seed GramsAI config into the user's mounted config dir if absent ----
# New users get an empty /root/.config (volume mount). Bake-and-seed the
# canonical opencode.json + 14 agents so every container is fully GramsAI.
CFG_DIR="/root/.config/opencode"
mkdir -p "$CFG_DIR"
if [ ! -f "$CFG_DIR/opencode.json" ]; then
  echo "seeding GramsAI config into $CFG_DIR"
  tar xzf /opt/gramsai-config/gramsai-config.tar.gz -C "$CFG_DIR"
fi
# Remove OpenCode's auto-created stub that pulls in default/free providers.
rm -f "$CFG_DIR/opencode.jsonc" 2>/dev/null || true

# ---- workspace git baseline (for the diff/review panel) ----
cd "$WORKSPACE"
cat > "$WORKSPACE/.gitignore" << 'GI'
.config/
.local/
.opencode/
*.db
*.db-shm
*.db-wal
__pycache__/
*.pyc
.DS_Store
# Per-chat files: never track or commit. Tracking these caused every new
# worktree to inherit all images/uploads (storage blowup) via checkout.
images/
uploads/
GI
if [ ! -d "$WORKSPACE/.git" ]; then
  git init -q "$WORKSPACE"
fi
git -C "$WORKSPACE" config user.email "agent@grams.chat"
git -C "$WORKSPACE" config user.name "GramsAI"
git -C "$WORKSPACE" config commit.gpgsign false
git -C "$WORKSPACE" config core.autocrlf false
git -C "$WORKSPACE" add -A 2>/dev/null || true
if ! git -C "$WORKSPACE" rev-parse HEAD >/dev/null 2>&1; then
  git -C "$WORKSPACE" commit -q -m "workspace baseline" 2>/dev/null || true
else
  git -C "$WORKSPACE" commit -q -m "sync baseline" 2>/dev/null || true
fi

# Start workspace download service in background
GRAMSAI_DL_PORT="${GRAMSAI_DL_PORT:-5010}" node /usr/local/bin/workspace-download.js &

# Start live browser panel bridge in background (same pattern as the dl service).
BRIDGE_PORT="${BRIDGE_PORT:-8088}" python3 /usr/local/bin/bridge.py &

exec opencode "$@"
