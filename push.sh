#!/usr/bin/env bash
# push.sh — commit everything collected and push to GitHub. Run after gramsai-deploy.sh.
set -euo pipefail
REPO="${REPO:-/root/GramsAI}"
GIT_REMOTE="git@github.com:freedomcorp24/GramsAI.git"
cd "$REPO"
if [ ! -d .git ]; then
  git init
  git branch -M main
  git remote add origin "$GIT_REMOTE" 2>/dev/null || git remote set-url origin "$GIT_REMOTE"
fi
[ -f .gitignore ] || { echo "MISSING .gitignore — copy it into $REPO first"; exit 1; }
git config user.email "deploy@grams.chat" 2>/dev/null || true
git config user.name  "GramsAI Deploy"    2>/dev/null || true
git add -A
MSG="${1:-deploy: sync M1+M2 source & config $(date -u +%Y-%m-%dT%H:%M:%SZ)}"
git commit -m "$MSG" || { echo "nothing to commit"; exit 0; }
git push -u origin main
echo "PUSHED to $GIT_REMOTE"
