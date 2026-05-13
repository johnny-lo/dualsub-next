#!/usr/bin/env bash
# DualSub Next background daemon helper for Linux/macOS.
#
# Usage:
#   ./dualsub-bg.sh start
#   ./dualsub-bg.sh stop
#   ./dualsub-bg.sh restart
#   ./dualsub-bg.sh status
#   ./dualsub-bg.sh install-systemd
#   ./dualsub-bg.sh uninstall-systemd

set -euo pipefail

ACTION="${1:-start}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
DAEMON_BIN="${SCRIPT_DIR}/dualsub"
CONFIG_PATH="${HOME}/.config/dualsub/config.toml"
STATE_DIR="${XDG_STATE_HOME:-${HOME}/.local/state}/dualsub"
PID_PATH="${STATE_DIR}/dualsub.pid"
STDOUT_LOG="${STATE_DIR}/dualsub.stdout.log"
STDERR_LOG="${STATE_DIR}/dualsub.stderr.log"
HEALTH_URL="http://127.0.0.1:7878/healthz"
SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"
SYSTEMD_SERVICE_PATH="${SYSTEMD_USER_DIR}/dualsub.service"

health_ok() {
  command -v curl >/dev/null 2>&1 || return 1
  curl -fsS --max-time 2 "${HEALTH_URL}" >/dev/null 2>&1
}

tracked_pid() {
  [[ -f "${PID_PATH}" ]] || return 1
  local pid
  pid="$(head -n 1 "${PID_PATH}" 2>/dev/null || true)"
  [[ "${pid}" =~ ^[0-9]+$ ]] || return 1
  kill -0 "${pid}" 2>/dev/null || return 1
  printf '%s\n' "${pid}"
}

require_binary() {
  if [[ ! -x "${DAEMON_BIN}" ]]; then
    echo "dualsub binary not found or not executable at ${DAEMON_BIN}" >&2
    echo "Build it first: cd daemon && go build -o ../dualsub ./cmd/dualsub" >&2
    exit 1
  fi
}

require_config() {
  if [[ ! -f "${CONFIG_PATH}" ]]; then
    echo "config not found at ${CONFIG_PATH}" >&2
    echo "Initialize it first: ./dualsub config init" >&2
    exit 1
  fi
}

start_dualsub() {
  if health_ok; then
    echo "DualSub daemon is already running at ${HEALTH_URL}"
    return
  fi

  require_binary
  require_config
  mkdir -p "${STATE_DIR}"
  rm -f "${PID_PATH}"

  nohup "${DAEMON_BIN}" serve >>"${STDOUT_LOG}" 2>>"${STDERR_LOG}" &
  local pid=$!
  printf '%s\n' "${pid}" >"${PID_PATH}"
  sleep 1

  if health_ok; then
    echo "DualSub daemon started in background. PID: ${pid}"
  else
    echo "DualSub daemon process started, but health check is not ready yet. PID: ${pid}" >&2
  fi
  echo "stdout: ${STDOUT_LOG}"
  echo "stderr: ${STDERR_LOG}"
}

stop_dualsub() {
  local pid
  if pid="$(tracked_pid)"; then
    kill "${pid}" 2>/dev/null || true
    for _ in 1 2 3; do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 1
    done
    kill -9 "${pid}" 2>/dev/null || true
    rm -f "${PID_PATH}"
    echo "DualSub daemon stopped."
    return
  fi

  rm -f "${PID_PATH}"
  echo "DualSub daemon is not running."
}

status_dualsub() {
  local pid
  if health_ok; then
    if pid="$(tracked_pid)"; then
      echo "DualSub daemon is running. PID: ${pid}"
    else
      echo "DualSub daemon is running, but no PID file is available."
    fi
    return
  fi

  if pid="$(tracked_pid)"; then
    echo "DualSub daemon process exists, but health check failed. PID: ${pid}" >&2
    return
  fi

  echo "DualSub daemon is offline."
}

install_systemd() {
  require_binary
  require_config

  if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemctl not found; use './dualsub-bg.sh start' instead." >&2
    exit 1
  fi

  mkdir -p "${SYSTEMD_USER_DIR}" "${STATE_DIR}"
  cat >"${SYSTEMD_SERVICE_PATH}" <<EOF
[Unit]
Description=DualSub Next translation daemon
After=network-online.target

[Service]
Type=simple
WorkingDirectory=${SCRIPT_DIR}
ExecStart=${DAEMON_BIN} serve
Restart=on-failure
RestartSec=2
StandardOutput=append:${STDOUT_LOG}
StandardError=append:${STDERR_LOG}

[Install]
WantedBy=default.target
EOF

  systemctl --user daemon-reload
  systemctl --user enable --now dualsub.service
  echo "Installed and started systemd user service: ${SYSTEMD_SERVICE_PATH}"
}

uninstall_systemd() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user disable --now dualsub.service >/dev/null 2>&1 || true
    systemctl --user daemon-reload >/dev/null 2>&1 || true
  fi
  rm -f "${SYSTEMD_SERVICE_PATH}"
  echo "Removed systemd user service: ${SYSTEMD_SERVICE_PATH}"
}

case "${ACTION}" in
  start) start_dualsub ;;
  stop) stop_dualsub ;;
  restart) stop_dualsub; sleep 0.3; start_dualsub ;;
  status) status_dualsub ;;
  install-systemd) install_systemd ;;
  uninstall-systemd) uninstall_systemd ;;
  *)
    echo "Usage: $0 {start|stop|restart|status|install-systemd|uninstall-systemd}" >&2
    exit 2
    ;;
esac
