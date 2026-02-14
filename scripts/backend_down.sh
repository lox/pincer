#!/usr/bin/env bash
set -euo pipefail

SESSION="${PINCER_TMUX_SESSION:-pincer-backend}"

if ! command -v tmux >/dev/null 2>&1; then
  echo "tmux is required but not installed" >&2
  exit 1
fi

if tmux has-session -t "${SESSION}" 2>/dev/null; then
  tmux kill-session -t "${SESSION}"
  echo "stopped tmux session '${SESSION}'"
  exit 0
fi

echo "tmux session '${SESSION}' is not running"
