#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
readonly LOG_FILE="$ROOT_DIR/.dev/panda.log"
readonly LOG_LINES="${PANDA_LOG_LINES:-80}"

if [[ ! "$LOG_LINES" =~ ^[0-9]+$ ]]; then
	printf "[dev] ERROR: PANDA_LOG_LINES must be a number\n" >&2
	exit 1
fi

if [[ ! -f "$LOG_FILE" ]]; then
	printf "[dev] No log file yet: %s\n" "$LOG_FILE"
	exit 0
fi

tail -n "$LOG_LINES" -f "$LOG_FILE"
