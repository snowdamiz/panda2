FROM golang:1.26.4-bookworm AS builder

WORKDIR /src
ARG GO_TAGS=sqlite_fts5

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    gcc \
    libc6-dev \
  && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go test -tags "$GO_TAGS" ./...
RUN CGO_ENABLED=1 GOOS=linux go build -tags "$GO_TAGS" -trimpath -ldflags="-s -w" -o /out/panda ./cmd/bot

FROM debian:12.12-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    ffmpeg \
    fonts-dejavu-core \
    fonts-inter \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd --system panda \
  && useradd --system --gid panda --home-dir /nonexistent --shell /usr/sbin/nologin panda \
  && mkdir -p /data/tmp \
  && chown -R panda:panda /data

USER panda:panda
WORKDIR /app

COPY --from=builder /out/panda /app/panda
COPY --from=builder /src/panda.config.json /app/panda.config.json

ENV PORT=8080
ENV FFMPEG_PATH=/usr/bin/ffmpeg
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/app/panda", "healthcheck"]

ENTRYPOINT ["/app/panda"]
