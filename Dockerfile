# ---- Build stage ----
FROM golang:1.25-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG GIT_SHA=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.gitSHA=${GIT_SHA}" -o gh-server .

# ---- Runtime stage ----
FROM alpine:3.21
RUN apk add --no-cache git git-daemon \
    && test -x "$(git --exec-path)/git-http-backend"

RUN adduser -D -h /data appuser
USER appuser

COPY --from=builder /app/gh-server /usr/local/bin/gh-server

ENV GIT_REPO_DIR=/data/repos
ENV LISTEN_MODE=production
RUN mkdir -p /data/repos

EXPOSE 8080

ENTRYPOINT ["gh-server"]
