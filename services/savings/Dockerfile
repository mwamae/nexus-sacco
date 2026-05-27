# Multi-stage build for the savings service.
#
# Produces two binaries:
#   /usr/local/bin/savings            — HTTP service (cmd/server)
#   /usr/local/bin/posting-dispatcher — background outbox drainer
#
# Compose decides which one each container runs by overriding ENTRYPOINT.

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -ldflags="-s -w" -o /out/savings            ./cmd/server && \
    go build -ldflags="-s -w" -o /out/posting-dispatcher ./cmd/posting-dispatcher

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -u 10001 app
USER app
COPY --from=build /out/savings            /usr/local/bin/savings
COPY --from=build /out/posting-dispatcher /usr/local/bin/posting-dispatcher
EXPOSE 8084
ENTRYPOINT ["/usr/local/bin/savings"]
