#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
readonly RUN_DIR="$ROOT_DIR/.dev"
readonly BIN="$RUN_DIR/panda"
readonly PID_FILE="$RUN_DIR/panda.pid"
readonly STOP_TIMEOUT="${PANDA_STOP_TIMEOUT:-10}"

log() {
	printf "[dev] %s\n" "$*"
}

error() {
	printf "[dev] ERROR: %s\n" "$*" >&2
	exit 1
}

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

is_service_pid() {
	local command
	command="$(pid_command "$1")"
	[[ "$command" == *"$BIN"* ]]
}

stop_pid() {
	local pid="$1"
	local i

	kill -TERM "$pid" 2>/dev/null || true
	for ((i = 1; i <= STOP_TIMEOUT; i++)); do
		if ! is_pid_running "$pid"; then
			return 0
		fi
		sleep 1
	done

	log "Panda did not stop after ${STOP_TIMEOUT}s; sending SIGKILL"
	kill -KILL "$pid" 2>/dev/null || true
}

main() {
	[[ "$STOP_TIMEOUT" =~ ^[0-9]+$ ]] || error "PANDA_STOP_TIMEOUT must be a number of seconds"

	local pid
	pid="$(read_pid)"
	if [[ -z "$pid" ]]; then
		log "Panda dev stack is not running"
		return 0
	fi

	if ! is_pid_running "$pid"; then
		rm -f "$PID_FILE"
		log "Removed stale pid file"
		return 0
	fi

	if ! is_service_pid "$pid"; then
		rm -f "$PID_FILE"
		log "PID file pointed at an unrelated process; removed stale pid file and left process $pid alone"
		return 0
	fi

	log "Stopping Panda dev stack (pid $pid)"
	stop_pid "$pid"
	rm -f "$PID_FILE"
	log "Stopped Panda dev stack"
}

main "$@"
