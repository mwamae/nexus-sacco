# Multi-stage build for the accounting service.
#
# Build context: repo root. docker-compose.yml sets
#   context: .
#   dockerfile: services/accounting/Dockerfile

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly

COPY shared/               ./shared/
COPY services/accounting/  ./services/accounting/

WORKDIR /src/services/accounting
RUN go mod download
RUN go build -ldflags="-s -w" -o /out/accounting ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -u 10001 app
USER app
COPY --from=build /out/accounting /usr/local/bin/accounting
EXPOSE 8086
ENTRYPOINT ["/usr/local/bin/accounting"]
