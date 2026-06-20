#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
readonly RUN_DIR="$ROOT_DIR/.dev"
readonly BIN="$RUN_DIR/panda"
readonly PID_FILE="$RUN_DIR/panda.pid"
readonly LOG_FILE="$RUN_DIR/panda.log"

is_pid_running() {
	local pid="$1"
	[[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null
}

read_pid() {
	if [[ -f "$PID_FILE" ]]; then
		tr -d "[:space:]" < "$PID_FILE"
	fi
}

pid_command() {
	ps -p "$1" -o command= 2>/dev/null || true
}

main() {
	local pid
	pid="$(read_pid)"

	if [[ -z "$pid" ]]; then
		printf "[dev] Panda dev stack is stopped\n"
		return 0
	fi

	if ! is_pid_running "$pid"; then
		printf "[dev] Panda dev stack is stopped (stale pid file: %s)\n" "$PID_FILE"
		return 1
	fi

	if [[ "$(pid_command "$pid")" != *"$BIN"* ]]; then
		printf "[dev] Panda dev stack status is unknown; pid %s does not match %s\n" "$pid" "$BIN"
		return 1
	fi

	printf "[dev] Panda dev stack is running (pid %s)\n" "$pid"
	printf "[dev] Logs: %s\n" "$LOG_FILE"
}

main "$@"
