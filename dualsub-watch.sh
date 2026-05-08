#!/usr/bin/env bash
# DualSub Next — daemon watcher (Linux/macOS).
#
# Starts the dualsub binary and automatically restarts it whenever
# ~/.config/dualsub/config.toml changes on disk.
#
# Usage:
#   ./dualsub-watch.sh
#
# Stop with Ctrl+C; the daemon child process is killed on exit.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
DAEMON_BIN="${SCRIPT_DIR}/dualsub"
CONFIG_PATH="${HOME}/.config/dualsub/config.toml"

if [[ ! -x "${DAEMON_BIN}" ]]; then
  echo "dualsub binary not found or not executable at ${DAEMON_BIN}" >&2
  echo "Build it first:" >&2
  echo "  cd daemon && go build -o ../dualsub ./cmd/dualsub" >&2
  exit 1
fi

if [[ ! -f "${CONFIG_PATH}" ]]; then
  echo "config not found at ${CONFIG_PATH}" >&2
  echo "Initialize it first: ./dualsub config init" >&2
  exit 1
fi

# Portable mtime read: -c on GNU stat (Linux), -f on BSD stat (macOS).
mtime() {
  if stat -c '%Y' "$1" 2>/dev/null; then
    return
  fi
  stat -f '%m' "$1"
}

DAEMON_PID=

start_daemon() {
  echo "[watch] starting dualsub..."
  "${DAEMON_BIN}" serve &
  DAEMON_PID=$!
}

stop_daemon() {
  if [[ -n "${DAEMON_PID}" ]] && kill -0 "${DAEMON_PID}" 2>/dev/null; then
    echo "[watch] stopping pid ${DAEMON_PID}..."
    kill "${DAEMON_PID}" 2>/dev/null || true
    # Give it 3 s to exit cleanly, then SIGKILL.
    for _ in 1 2 3; do
      kill -0 "${DAEMON_PID}" 2>/dev/null || break
      sleep 1
    done
    kill -9 "${DAEMON_PID}" 2>/dev/null || true
    wait "${DAEMON_PID}" 2>/dev/null || true
  fi
  DAEMON_PID=
}

cleanup() {
  stop_daemon
  echo "[watch] stopped"
}
trap cleanup EXIT INT TERM

start_daemon
echo "[watch] watching ${CONFIG_PATH}"
echo "[watch] press Ctrl+C to stop"
last_mtime="$(mtime "${CONFIG_PATH}")"

while true; do
  sleep 2

  # Bail if the daemon died on its own (e.g., bad config).
  if [[ -n "${DAEMON_PID}" ]] && ! kill -0 "${DAEMON_PID}" 2>/dev/null; then
    wait "${DAEMON_PID}" 2>/dev/null || true
    rc=$?
    echo "[watch] dualsub exited (code ${rc}) — bailing out" >&2
    DAEMON_PID=
    exit "${rc}"
  fi

  cur_mtime="$(mtime "${CONFIG_PATH}")"
  if [[ "${cur_mtime}" != "${last_mtime}" ]]; then
    last_mtime="${cur_mtime}"
    echo "[watch] config.toml changed — restarting daemon"
    stop_daemon
    sleep 0.3
    start_daemon
  fi
done
