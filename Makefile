.PHONY: help start stop restart status logs dev test

help:
	@printf "Panda dev commands:\n"
	@printf "  make start    Build and start the local dev stack in the background\n"
	@printf "  make stop     Stop the local dev stack\n"
	@printf "  make restart  Stop, then start the local dev stack\n"
	@printf "  make status   Show local dev stack status\n"
	@printf "  make logs     Tail local dev stack logs\n"
	@printf "  make dev      Run the app in the foreground\n"
	@printf "  make test     Run Go tests\n"

start:
	@scripts/dev/start.sh

stop:
	@scripts/dev/stop.sh

restart:
	@scripts/dev/stop.sh
	@scripts/dev/start.sh

status:
	@scripts/dev/status.sh

logs:
	@scripts/dev/logs.sh

dev:
	@go run ./cmd/bot

test:
	@go test ./...
