FROM golang:1.22-bookworm AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN mkdir -p /out && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/telegram-local-agent ./cmd/telegram-local-agent

FROM node:22-bookworm-slim

ARG CODEX_VERSION=0.141.0
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && npm install --global "@openai/codex@${CODEX_VERSION}" \
    && useradd --create-home --uid 10001 --shell /usr/sbin/nologin app

COPY --from=build /out/telegram-local-agent /usr/local/bin/telegram-local-agent
COPY agents.json /app/agents.json
COPY agents.example.json /app/agents.example.json
RUN mkdir -p /workspace /data/memory /data/state \
    && chown -R app:app /workspace /data /home/app

ENV AGENTS_FILE=/app/agents.json \
    CODEX_BIN=codex \
    CODEX_SANDBOX=read-only \
    CODEX_WORKDIR=/workspace \
    MEMORY_DIR=/data/memory \
    STATE_DIR=/data/state

WORKDIR /workspace
USER app

ENTRYPOINT ["telegram-local-agent"]
