#!/usr/bin/env python3
# Run on M1:  python3 green_send.py
# Makes the chat send button solid green (#3fb950) with a dark icon, matching
# the mock. Targets BOTH submit buttons. Logic untouched (type=submit, disabled,
# icon, aria all preserved). Idempotent.
F = "/opt/gramsai/opencode-src/packages/app/src/components/prompt-input.tsx"
s = open(F).read()
n = 0

# --- button #1 (line ~1575): replace dark class + remove dark gradient style ---
old_cls = 'class="size-7 rounded-md p-[6px] text-v2-icon-icon-muted shadow-[var(--v2-elevation-button-contrast)] disabled:opacity-50"'
new_cls = 'class="size-7 rounded-md p-[6px] !bg-[#3fb950] !text-[#04210c] shadow-[0_6px_18px_-6px_rgba(63,185,80,0.7)] hover:!brightness-110 disabled:opacity-50"'
if old_cls in s:
    s = s.replace(old_cls, new_cls); n += 1

old_style = '''                    style={{
                      "background-image":
                        "linear-gradient(180deg,var(--v2-alpha-light-20) 0%,var(--v2-alpha-light-0) 100%),linear-gradient(90deg,var(--v2-background-bg-contrast) 0%,var(--v2-background-bg-contrast) 100%)",
                    }}
'''
if old_style in s:
    s = s.replace(old_style, "")  # drop the dark gradient entirely
    n += 1

# --- button #2 (line ~1718): size-8 -> add flat green ---
old2 = '''                      variant="primary"
                      class="size-8"
                      aria-label={stopping() ? language.t("prompt.action.stop") : language.t("prompt.action.send")}'''
new2 = '''                      variant="primary"
                      class="size-8 !bg-[#3fb950] !text-[#04210c] hover:!brightness-110"
                      aria-label={stopping() ? language.t("prompt.action.stop") : language.t("prompt.action.send")}'''
if old2 in s:
    s = s.replace(old2, new2); n += 1

open(F, "w").write(s)
print("edits applied:", n)
print("dark gradient gone:", 'v2-background-bg-contrast' not in s or s.count('v2-background-bg-contrast')==0)
print("green present:", '!bg-[#3fb950]' in s)
print("DONE")
