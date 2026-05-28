# Multi-stage build for the mpesa service.
#
# Build context: repo root. docker-compose.yml sets
#   context: .
#   dockerfile: services/mpesa/Dockerfile
#
# Produces two binaries:
#   /usr/local/bin/mpesa             — HTTP service (cmd/server)
#   /usr/local/bin/mpesa-distributor — background worker (cmd/distributor)
#
# Compose decides which one each container runs by overriding ENTRYPOINT.

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY shared/             ./shared/
COPY services/finance/   ./services/finance/
COPY services/mpesa/     ./services/mpesa/

WORKDIR /src/services/mpesa
RUN go mod download
RUN go build -ldflags="-s -w" -o /out/mpesa             ./cmd/server && \
    go build -ldflags="-s -w" -o /out/mpesa-distributor ./cmd/distributor

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
COPY --from=build /out/mpesa             /usr/local/bin/mpesa
COPY --from=build /out/mpesa-distributor /usr/local/bin/mpesa-distributor
EXPOSE 8087
ENTRYPOINT ["/usr/local/bin/mpesa"]
