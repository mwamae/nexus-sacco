# Multi-stage build for the member service.
#
# Build context: repo root. docker-compose.yml sets
#   context: .
#   dockerfile: services/member/Dockerfile
# so the build can COPY the in-tree shared/ + services/finance/
# modules that services/member/go.mod resolves via replace directives.

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY shared/             ./shared/
COPY services/finance/   ./services/finance/
COPY services/member/    ./services/member/

WORKDIR /src/services/member
RUN go mod download
RUN go build -ldflags="-s -w" -o /out/member ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
COPY --from=build /out/member /usr/local/bin/member
EXPOSE 8082
ENTRYPOINT ["/usr/local/bin/member"]
