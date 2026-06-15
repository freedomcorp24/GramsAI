#!/usr/bin/env python3
# Run on M1:  python3 hide_workspace_picker.py
# Hides the new-session project/workspace picker in the composer (both the
# "select project" trigger and the workspace dropdown row) WITHOUT touching
# send, model control, agent select, or newSessionWorktree="create" (per-chat
# worktrees stay). Just gates the two <Show> blocks off. Idempotent + reversible.
F = "/opt/gramsai/opencode-src/packages/app/src/components/prompt-input.tsx"
s = open(F).read()
n = 0

# 1) the "no project selected" picker trigger
a = '<Show when={newSession() && !selectedProject()}>'
b = '<Show when={false && newSession() && !selectedProject()}>'
if a in s:
    assert s.count(a) == 1, "trigger Show appears %d times" % s.count(a)
    s = s.replace(a, b); n += 1

# 2) the workspace dropdown row (project selected)
c = '<Show when={newSession() && selectedProject()}>'
d = '<Show when={false && newSession() && selectedProject()}>'
if c in s:
    assert s.count(c) == 1, "workspace Show appears %d times" % s.count(c)
    s = s.replace(c, d); n += 1

# guard: never accidentally edit if already done
if '{false && newSession()' in open(F).read():
    print(".. already hidden"); print("DONE"); raise SystemExit

open(F, "w").write(s)
print("hidden blocks:", n)
print("send button untouched:", 'data-action="prompt-submit"' in s)
print("model control untouched:", '<ComposerModelControl state={modelControlState()} />' in s)
print("DONE")
