#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
readonly RUN_DIR="$ROOT_DIR/.dev"
readonly BIN="$RUN_DIR/panda"
readonly PID_FILE="$RUN_DIR/panda.pid"
readonly LOG_FILE="$RUN_DIR/panda.log"
readonly HEALTH_TIMEOUT="${PANDA_START_HEALTH_TIMEOUT:-15}"

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

build_app() {
	local -a build_cmd=(go build)
	if [[ -n "${GO_TAGS:-}" ]]; then
		build_cmd+=(-tags "$GO_TAGS")
	fi
	build_cmd+=(-o "$BIN" ./cmd/bot)

	log "Building Panda dev binary"
	(cd "$ROOT_DIR" && "${build_cmd[@]}")
}

start_app() {
	log "Starting Panda dev stack"
	{
		printf "\n[%s] starting panda\n" "$(date "+%Y-%m-%d %H:%M:%S")"
	} >> "$LOG_FILE"

	(
		cd "$ROOT_DIR"
		nohup "$BIN" </dev/null >> "$LOG_FILE" 2>&1 &
		child_pid="$!"
		printf "%s" "$child_pid" > "$PID_FILE"
		disown "$child_pid" 2>/dev/null || true
	)
}

wait_for_health() {
	local pid="$1"
	local i

	for ((i = 1; i <= HEALTH_TIMEOUT; i++)); do
		if (cd "$ROOT_DIR" && "$BIN" healthcheck >/dev/null 2>&1); then
			return 0
		fi
		if ! is_pid_running "$pid"; then
			tail -n 40 "$LOG_FILE" >&2 || true
			error "Panda exited during startup"
		fi
		sleep 1
	done

	return 1
}

main() {
	[[ "$HEALTH_TIMEOUT" =~ ^[0-9]+$ ]] || error "PANDA_START_HEALTH_TIMEOUT must be a number of seconds"
	mkdir -p "$RUN_DIR"

	local pid
	pid="$(read_pid)"
	if [[ -n "$pid" ]]; then
		if is_pid_running "$pid"; then
			log "Panda dev stack is already running (pid $pid)"
			log "Logs: $LOG_FILE"
			return 0
		fi
		log "Removing stale pid file"
		rm -f "$PID_FILE"
	fi

	build_app
	start_app
	pid="$(read_pid)"

	if wait_for_health "$pid"; then
		log "Panda dev stack is ready (pid $pid)"
		log "Logs: $LOG_FILE"
		return 0
	fi

	"$SCRIPT_DIR/stop.sh" >/dev/null 2>&1 || true
	tail -n 40 "$LOG_FILE" >&2 || true
	error "Panda did not become healthy within ${HEALTH_TIMEOUT}s"
}

main "$@"
