FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/telegram-local-agent .

FROM alpine:3.20

RUN adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=build /out/telegram-local-agent /usr/local/bin/telegram-local-agent
COPY agents.example.json /app/agents.example.json
USER app

ENTRYPOINT ["telegram-local-agent"]
