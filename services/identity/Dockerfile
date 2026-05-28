# Multi-stage build for the identity service.
#
# Build context: repo root. docker-compose.yml sets
#   context: .
#   dockerfile: services/identity/Dockerfile
# so the build can COPY the in-tree shared/ module that
# services/identity/go.mod resolves via `replace ../../shared`.

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

# In-tree replace targets — copy these BEFORE the service dir so
# `go mod download` (and the build) can resolve the relative path.
COPY shared/             ./shared/
COPY services/identity/  ./services/identity/

WORKDIR /src/services/identity
RUN go mod download
RUN go build -ldflags="-s -w" -o /out/identity ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
COPY --from=build /out/identity /usr/local/bin/identity
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/identity"]
