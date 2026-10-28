#!/bin/sh
# Link this repo's skills into Claude's and Codex's skill dirs.
# Idempotent: rerun after adding a skill. Needs real skill dirs
# (dotfiles links its own skills per entry).
set -eu

skills=$(cd "$(dirname "$0")" && pwd)

for agent in "$HOME/.claude/skills" "$HOME/.codex/skills"; do
  if [ -L "$agent" ]; then
    echo "$agent is a symlink, not a dir: update dotfiles first" >&2
    exit 1
  fi

  mkdir -p "$agent"

  for skill in "$skills"/*/; do
    skill=${skill%/}
    ln -sfn "$skill" "$agent/${skill##*/}"
  done
done
